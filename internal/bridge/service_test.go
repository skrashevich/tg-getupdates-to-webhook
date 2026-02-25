package bridge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"tg-getupdates-to-webhook/internal/config"
	"tg-getupdates-to-webhook/internal/storage"
	"tg-getupdates-to-webhook/internal/telegram"
)

func TestDeliverUpdateProxiesBackendMethod(t *testing.T) {
	t.Parallel()

	update := json.RawMessage(`{"update_id":101,"message":{"text":"ping"}}`)

	backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("X-Telegram-Bot-Api-Secret-Token"); got != "expected-secret" {
			t.Fatalf("expected secret header, got %q", got)
		}

		if got := request.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Fatalf("expected JSON content type, got %q", got)
		}

		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}

		if !jsonEqual(body, update) {
			t.Fatalf("unexpected update payload: %s", string(body))
		}

		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"method":"sendMessage","chat_id":42,"text":"pong"}`))
	}))
	defer backendServer.Close()

	fakeTG := &fakeTelegram{}
	service := NewService(config.Config{UserAgent: "test-agent"}, fakeTG, fakeStateStore{}, backendServer.Client(), discardLogger())

	bot := config.BotConfig{
		Name:        "legacy-1",
		Token:       "bot-token",
		BackendURL:  backendServer.URL,
		SecretToken: "expected-secret",
	}

	err := service.deliverUpdate(context.Background(), bot, 101, update)
	if err != nil {
		t.Fatalf("deliverUpdate returned error: %v", err)
	}

	if fakeTG.callCount != 1 {
		t.Fatalf("expected one proxied method call, got %d", fakeTG.callCount)
	}

	if fakeTG.calledMethod != "sendMessage" {
		t.Fatalf("expected sendMessage, got %q", fakeTG.calledMethod)
	}

	if !jsonEqual(fakeTG.calledPayload, []byte(`{"chat_id":42,"text":"pong"}`)) {
		t.Fatalf("unexpected proxied payload: %s", string(fakeTG.calledPayload))
	}
}

func TestDeliverUpdateReturnsErrorOnBackendNon2xx(t *testing.T) {
	t.Parallel()

	backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		http.Error(responseWriter, "backend failure", http.StatusBadGateway)
	}))
	defer backendServer.Close()

	fakeTG := &fakeTelegram{}
	service := NewService(config.Config{UserAgent: "test-agent"}, fakeTG, fakeStateStore{}, backendServer.Client(), discardLogger())

	bot := config.BotConfig{Name: "legacy-2", Token: "bot-token", BackendURL: backendServer.URL}
	err := service.deliverUpdate(context.Background(), bot, 1, json.RawMessage(`{"update_id":1}`))
	if err == nil {
		t.Fatalf("expected error for non-2xx backend response")
	}

	if fakeTG.callCount != 0 {
		t.Fatalf("unexpected Telegram call count: %d", fakeTG.callCount)
	}
}

func TestDeliverUpdateIgnoresNonJSONBackendResponse(t *testing.T) {
	t.Parallel()

	backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		_, _ = responseWriter.Write([]byte("OK"))
	}))
	defer backendServer.Close()

	fakeTG := &fakeTelegram{}
	service := NewService(config.Config{UserAgent: "test-agent"}, fakeTG, fakeStateStore{}, backendServer.Client(), discardLogger())

	bot := config.BotConfig{Name: "legacy-3", Token: "bot-token", BackendURL: backendServer.URL}
	err := service.deliverUpdate(context.Background(), bot, 2, json.RawMessage(`{"update_id":2}`))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if fakeTG.callCount != 0 {
		t.Fatalf("unexpected Telegram call count: %d", fakeTG.callCount)
	}
}

func TestDeliverUpdateIgnoresPermanentTelegramReplyError(t *testing.T) {
	t.Parallel()

	backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"method":"sendMessage","chat_id":42,"text":"pong"}`))
	}))
	defer backendServer.Close()

	fakeTG := &fakeTelegram{
		callErr: &telegram.APIError{Method: "sendMessage", Description: "Bad Request", Temporary: false},
	}
	service := NewService(config.Config{UserAgent: "test-agent"}, fakeTG, fakeStateStore{}, backendServer.Client(), discardLogger())

	bot := config.BotConfig{Name: "legacy-4", Token: "bot-token", BackendURL: backendServer.URL}
	err := service.deliverUpdate(context.Background(), bot, 3, json.RawMessage(`{"update_id":3}`))
	if err != nil {
		t.Fatalf("expected no error for permanent Telegram error, got %v", err)
	}
}

func TestDeliverUpdateReturnsErrorForTemporaryTelegramReplyError(t *testing.T) {
	t.Parallel()

	backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"method":"sendMessage","chat_id":42,"text":"pong"}`))
	}))
	defer backendServer.Close()

	fakeTG := &fakeTelegram{
		callErr: &telegram.APIError{Method: "sendMessage", Description: "Too Many Requests", RetryAfter: 2, Temporary: true},
	}
	service := NewService(config.Config{UserAgent: "test-agent"}, fakeTG, fakeStateStore{}, backendServer.Client(), discardLogger())

	bot := config.BotConfig{Name: "legacy-5", Token: "bot-token", BackendURL: backendServer.URL}
	err := service.deliverUpdate(context.Background(), bot, 3, json.RawMessage(`{"update_id":3}`))
	if err == nil {
		t.Fatalf("expected error for temporary Telegram failure")
	}
}

type fakeTelegram struct {
	callErr       error
	calledMethod  string
	calledPayload json.RawMessage
	callCount     int
}

type fakeStateStore struct{}

func (fake *fakeTelegram) DeleteWebhook(context.Context, string, bool) error {
	return nil
}

func (fake *fakeTelegram) GetUpdates(context.Context, string, telegram.GetUpdatesRequest) ([]json.RawMessage, error) {
	return nil, nil
}

func (fake *fakeTelegram) CallMethodRaw(_ context.Context, _ string, method string, payload json.RawMessage) error {
	fake.callCount++
	fake.calledMethod = method
	fake.calledPayload = append(fake.calledPayload[:0], payload...)
	return fake.callErr
}

func (fakeStateStore) LoadOffset(context.Context, string) (int64, error) {
	return 0, nil
}

func (fakeStateStore) SaveOffset(context.Context, string, int64) error {
	return nil
}

func (fakeStateStore) LogInteraction(context.Context, storage.InteractionLog) error {
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func jsonEqual(left, right []byte) bool {
	var leftValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}

	var rightValue any
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}

	return reflect.DeepEqual(leftValue, rightValue)
}
