package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"tg-getupdates-to-webhook/internal/config"
	"tg-getupdates-to-webhook/internal/storage"
	"tg-getupdates-to-webhook/internal/telegram"
)

const maxBackendResponseBytes = 2 << 20

type TelegramAPI interface {
	DeleteWebhook(ctx context.Context, token string, dropPendingUpdates bool) error
	GetUpdates(ctx context.Context, token string, request telegram.GetUpdatesRequest) ([]json.RawMessage, error)
	CallMethodRaw(ctx context.Context, token, method string, payload json.RawMessage) error
}

type StateStore interface {
	LoadOffset(ctx context.Context, botName string) (int64, error)
	SaveOffset(ctx context.Context, botName string, offset int64) error
	LogInteraction(ctx context.Context, entry storage.InteractionLog) error
}

type Service struct {
	cfg           config.Config
	tg            TelegramAPI
	store         StateStore
	backendClient *http.Client
	logger        *slog.Logger
}

func NewService(cfg config.Config, tg TelegramAPI, store StateStore, backendClient *http.Client, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}

	if store == nil {
		store = noOpStateStore{}
	}

	if backendClient == nil {
		backendClient = &http.Client{Timeout: cfg.BackendTimeout}
	}

	return &Service{
		cfg:           cfg,
		tg:            tg,
		store:         store,
		backendClient: backendClient,
		logger:        logger,
	}
}

func (service *Service) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errChannel := make(chan error, len(service.cfg.Bots))
	var waitGroup sync.WaitGroup

	for _, bot := range service.cfg.Bots {
		botCopy := bot
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			if err := service.runBot(ctx, botCopy); err != nil && !errors.Is(err, context.Canceled) {
				errChannel <- fmt.Errorf("bot %s failed: %w", botCopy.Name, err)
				cancel()
			}
		}()
	}

	waitGroup.Wait()
	close(errChannel)

	for err := range errChannel {
		if err != nil {
			return err
		}
	}

	return nil
}

func (service *Service) runBot(ctx context.Context, bot config.BotConfig) error {
	if err := service.ensurePollingMode(ctx, bot); err != nil {
		return err
	}

	offset, err := service.store.LoadOffset(ctx, bot.Name)
	if err != nil {
		return fmt.Errorf("load offset from sqlite: %w", err)
	}

	if err := service.logInteraction(ctx, storage.InteractionLog{
		BotName:   bot.Name,
		Component: "bridge",
		Event:     "offset_loaded",
		Payload:   jsonCompactString(map[string]int64{"offset": offset}),
	}); err != nil {
		return err
	}

	service.logger.Info("bot poller started", "bot", bot.Name, "backend_url", bot.BackendURL)

	request := telegram.GetUpdatesRequest{
		Limit:          service.cfg.PollingLimit,
		Timeout:        int(service.cfg.PollingTimeout / time.Second),
		AllowedUpdates: bot.AllowedUpdates,
	}

	backoff := newBackoff(service.cfg.RetryInitialDelay, service.cfg.RetryMaxDelay)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		request.Offset = offset
		updates, err := service.tg.GetUpdates(ctx, bot.Token, request)
		if err != nil {
			if logErr := service.logInteraction(ctx, storage.InteractionLog{
				BotName:   bot.Name,
				Component: "telegram",
				Event:     "get_updates_error",
				ErrorText: err.Error(),
				Payload:   jsonCompactString(map[string]int64{"offset": request.Offset}),
			}); logErr != nil {
				return logErr
			}

			delay := nextDelay(backoff, err)
			service.logger.Error("getUpdates failed", "bot", bot.Name, "error", err, "retry_in", delay)
			if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
				return nil
			}
			continue
		}

		if err := service.logInteraction(ctx, storage.InteractionLog{
			BotName:   bot.Name,
			Component: "telegram",
			Event:     "get_updates_ok",
			Payload: jsonCompactString(map[string]any{
				"offset":       request.Offset,
				"update_count": len(updates),
			}),
		}); err != nil {
			return err
		}

		if len(updates) == 0 {
			backoff.Reset()
			continue
		}

		retryCurrentOffset := false
		for _, rawUpdate := range updates {
			updateID, err := extractUpdateID(rawUpdate)
			if err != nil {
				return fmt.Errorf("extract update_id: %w", err)
			}

			if err := service.logInteraction(ctx, storage.InteractionLog{
				BotName:   bot.Name,
				Component: "telegram",
				Event:     "update_received",
				UpdateID:  &updateID,
				Payload:   string(rawUpdate),
			}); err != nil {
				return err
			}

			if err := service.deliverUpdate(ctx, bot, updateID, rawUpdate); err != nil {
				delay := nextDelay(backoff, err)
				service.logger.Error("failed to deliver update", "bot", bot.Name, "update_id", updateID, "error", err, "retry_in", delay)
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					return nil
				}
				retryCurrentOffset = true
				break
			}

			nextOffset := updateID + 1
			if err := service.store.SaveOffset(ctx, bot.Name, nextOffset); err != nil {
				delay := nextDelay(backoff, err)
				service.logger.Error("failed to save offset", "bot", bot.Name, "offset", nextOffset, "error", err, "retry_in", delay)
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					return nil
				}
				retryCurrentOffset = true
				break
			}

			offset = nextOffset
			backoff.Reset()
		}

		if retryCurrentOffset {
			continue
		}
	}
}

func (service *Service) ensurePollingMode(ctx context.Context, bot config.BotConfig) error {
	backoff := newBackoff(service.cfg.RetryInitialDelay, service.cfg.RetryMaxDelay)

	for {
		if err := service.tg.DeleteWebhook(ctx, bot.Token, bot.DropPendingUpdates); err != nil {
			if logErr := service.logInteraction(ctx, storage.InteractionLog{
				BotName:   bot.Name,
				Component: "telegram",
				Event:     "delete_webhook_error",
				ErrorText: err.Error(),
				Payload:   jsonCompactString(map[string]bool{"drop_pending_updates": bot.DropPendingUpdates}),
			}); logErr != nil {
				return logErr
			}

			delay := nextDelay(backoff, err)
			service.logger.Error("deleteWebhook failed", "bot", bot.Name, "error", err, "retry_in", delay)
			if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
				return nil
			}
			continue
		}

		if err := service.logInteraction(ctx, storage.InteractionLog{
			BotName:   bot.Name,
			Component: "telegram",
			Event:     "delete_webhook_ok",
			Payload:   jsonCompactString(map[string]bool{"drop_pending_updates": bot.DropPendingUpdates}),
		}); err != nil {
			return err
		}

		service.logger.Info("switched bot to polling mode", "bot", bot.Name, "drop_pending_updates", bot.DropPendingUpdates)
		return nil
	}
}

func (service *Service) deliverUpdate(ctx context.Context, bot config.BotConfig, updateID int64, update json.RawMessage) error {
	if err := service.logInteraction(ctx, storage.InteractionLog{
		BotName:   bot.Name,
		Component: "backend",
		Event:     "request",
		UpdateID:  &updateID,
		Payload:   string(update),
	}); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, bot.BackendURL, bytes.NewReader(update))
	if err != nil {
		return fmt.Errorf("create backend request: %w", err)
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", service.cfg.UserAgent)
	if bot.SecretToken != "" {
		request.Header.Set("X-Telegram-Bot-Api-Secret-Token", bot.SecretToken)
	}

	response, err := service.backendClient.Do(request)
	if err != nil {
		if logErr := service.logInteraction(ctx, storage.InteractionLog{
			BotName:   bot.Name,
			Component: "backend",
			Event:     "response_error",
			UpdateID:  &updateID,
			ErrorText: err.Error(),
		}); logErr != nil {
			return logErr
		}

		return fmt.Errorf("send update to backend: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBackendResponseBytes))
	if err != nil {
		return fmt.Errorf("read backend response: %w", err)
	}

	if err := service.logInteraction(ctx, storage.InteractionLog{
		BotName:    bot.Name,
		Component:  "backend",
		Event:      "response",
		UpdateID:   &updateID,
		HTTPStatus: intPointer(response.StatusCode),
		Payload:    string(body),
	}); err != nil {
		return err
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("backend returned HTTP %d", response.StatusCode)
	}

	method, payload, hasMethod, parseErr := parseWebhookReply(body)
	if parseErr != nil {
		if logErr := service.logInteraction(ctx, storage.InteractionLog{
			BotName:   bot.Name,
			Component: "bridge",
			Event:     "webhook_reply_parse_error",
			UpdateID:  &updateID,
			ErrorText: parseErr.Error(),
			Payload:   string(body),
		}); logErr != nil {
			return logErr
		}

		service.logger.Warn("backend response is not a valid webhook reply, skipping proxy", "bot", bot.Name, "error", parseErr)
		return nil
	}

	if !hasMethod {
		return nil
	}

	if err := service.logInteraction(ctx, storage.InteractionLog{
		BotName:        bot.Name,
		Component:      "telegram",
		Event:          "method_request",
		UpdateID:       &updateID,
		TelegramMethod: method,
		Payload:        string(payload),
	}); err != nil {
		return err
	}

	err = service.tg.CallMethodRaw(ctx, bot.Token, method, payload)
	if err == nil {
		if logErr := service.logInteraction(ctx, storage.InteractionLog{
			BotName:        bot.Name,
			Component:      "telegram",
			Event:          "method_ok",
			UpdateID:       &updateID,
			TelegramMethod: method,
		}); logErr != nil {
			return logErr
		}

		return nil
	}

	if logErr := service.logInteraction(ctx, storage.InteractionLog{
		BotName:        bot.Name,
		Component:      "telegram",
		Event:          "method_error",
		UpdateID:       &updateID,
		TelegramMethod: method,
		ErrorText:      err.Error(),
	}); logErr != nil {
		return logErr
	}

	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) && !apiErr.Temporary {
		service.logger.Warn("telegram rejected proxied method, update is treated as delivered", "bot", bot.Name, "method", method, "error", err)
		return nil
	}

	return fmt.Errorf("proxy webhook response using %s: %w", method, err)
}

func parseWebhookReply(body []byte) (method string, payload json.RawMessage, hasMethod bool, parseErr error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", nil, false, nil
	}

	if trimmed[0] != '{' {
		return "", nil, false, nil
	}

	response := map[string]json.RawMessage{}
	if err := json.Unmarshal(trimmed, &response); err != nil {
		return "", nil, false, err
	}

	methodRaw, ok := response["method"]
	if !ok {
		return "", nil, false, nil
	}

	if err := json.Unmarshal(methodRaw, &method); err != nil {
		return "", nil, false, fmt.Errorf("decode method field: %w", err)
	}

	method = strings.TrimSpace(method)
	if method == "" {
		return "", nil, false, errors.New("method field is empty")
	}

	delete(response, "method")
	payload, err := json.Marshal(response)
	if err != nil {
		return "", nil, false, fmt.Errorf("marshal method payload: %w", err)
	}

	return method, payload, true, nil
}

func extractUpdateID(update json.RawMessage) (int64, error) {
	var envelope struct {
		UpdateID *int64 `json:"update_id"`
	}

	if err := json.Unmarshal(update, &envelope); err != nil {
		return 0, fmt.Errorf("decode update: %w", err)
	}

	if envelope.UpdateID == nil {
		return 0, errors.New("update_id is missing")
	}

	return *envelope.UpdateID, nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		delay = time.Second
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextDelay(backoff *exponentialBackoff, err error) time.Duration {
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter
	}

	return backoff.Next()
}

type exponentialBackoff struct {
	initial time.Duration
	max     time.Duration
	current time.Duration
}

func newBackoff(initialDelay, maxDelay time.Duration) *exponentialBackoff {
	if initialDelay <= 0 {
		initialDelay = time.Second
	}

	if maxDelay < initialDelay {
		maxDelay = initialDelay
	}

	return &exponentialBackoff{
		initial: initialDelay,
		max:     maxDelay,
	}
}

func (backoff *exponentialBackoff) Next() time.Duration {
	if backoff.current <= 0 {
		backoff.current = backoff.initial
		return backoff.current
	}

	next := backoff.current * 2
	if next > backoff.max {
		next = backoff.max
	}

	backoff.current = next
	return backoff.current
}

func (backoff *exponentialBackoff) Reset() {
	backoff.current = 0
}

func (service *Service) logInteraction(ctx context.Context, entry storage.InteractionLog) error {
	if err := service.store.LogInteraction(ctx, entry); err != nil {
		return fmt.Errorf("persist interaction log: %w", err)
	}

	return nil
}

func jsonCompactString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return string(encoded)
}

func intPointer(value int) *int {
	return &value
}

type noOpStateStore struct{}

func (noOpStateStore) LoadOffset(context.Context, string) (int64, error) {
	return 0, nil
}

func (noOpStateStore) SaveOffset(context.Context, string, int64) error {
	return nil
}

func (noOpStateStore) LogInteraction(context.Context, storage.InteractionLog) error {
	return nil
}
