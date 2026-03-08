package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.telegram.org"

type Client struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
}

type GetUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

type WebhookInfo struct {
	URL                          string   `json:"url"`
	HasCustomCertificate         bool     `json:"has_custom_certificate"`
	PendingUpdateCount           int      `json:"pending_update_count"`
	IPAddress                    string   `json:"ip_address,omitempty"`
	LastErrorDate                int64    `json:"last_error_date,omitempty"`
	LastErrorMessage             string   `json:"last_error_message,omitempty"`
	LastSynchronizationErrorDate int64    `json:"last_synchronization_error_date,omitempty"`
	MaxConnections               int      `json:"max_connections,omitempty"`
	AllowedUpdates               []string `json:"allowed_updates,omitempty"`
}

type BotUser struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

type APIError struct {
	Method      string
	Description string
	ErrorCode   int
	StatusCode  int
	RetryAfter  time.Duration
	Temporary   bool
}

func (errorValue *APIError) Error() string {
	status := ""
	if errorValue.StatusCode > 0 {
		status = fmt.Sprintf(" (status %d)", errorValue.StatusCode)
	}

	code := ""
	if errorValue.ErrorCode > 0 {
		code = fmt.Sprintf(" (error_code %d)", errorValue.ErrorCode)
	}

	retry := ""
	if errorValue.RetryAfter > 0 {
		retry = fmt.Sprintf(" (retry_after %s)", errorValue.RetryAfter)
	}

	if errorValue.Description == "" {
		return fmt.Sprintf("telegram %s failed%s%s%s", errorValue.Method, status, code, retry)
	}

	return fmt.Sprintf("telegram %s failed: %s%s%s%s", errorValue.Method, errorValue.Description, status, code, retry)
}

func NewClient(timeout time.Duration, userAgent string) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		userAgent = "tg-getupdates-to-webhook/1.0"
	}

	return &Client{
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		userAgent: userAgent,
	}
}

func (client *Client) DeleteWebhook(ctx context.Context, token string, dropPendingUpdates bool) error {
	requestBody, err := json.Marshal(map[string]bool{"drop_pending_updates": dropPendingUpdates})
	if err != nil {
		return fmt.Errorf("marshal deleteWebhook request: %w", err)
	}

	_, err = client.call(ctx, token, "deleteWebhook", requestBody)
	if err != nil {
		return err
	}

	return nil
}

func (client *Client) GetWebhookInfo(ctx context.Context, token string) (WebhookInfo, error) {
	var webhookInfo WebhookInfo
	if err := client.callAndDecode(ctx, token, "getWebhookInfo", []byte("{}"), &webhookInfo); err != nil {
		return WebhookInfo{}, err
	}

	return webhookInfo, nil
}

func (client *Client) GetMe(ctx context.Context, token string) (BotUser, error) {
	var botUser BotUser
	if err := client.callAndDecode(ctx, token, "getMe", []byte("{}"), &botUser); err != nil {
		return BotUser{}, err
	}

	return botUser, nil
}

func (client *Client) GetUpdates(ctx context.Context, token string, request GetUpdatesRequest) ([]json.RawMessage, error) {
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal getUpdates request: %w", err)
	}

	result, err := client.call(ctx, token, "getUpdates", requestBody)
	if err != nil {
		return nil, err
	}

	trimmed := bytes.TrimSpace(result)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	var updates []json.RawMessage
	if err := json.Unmarshal(result, &updates); err != nil {
		return nil, fmt.Errorf("decode getUpdates result: %w", err)
	}

	return updates, nil
}

func (client *Client) CallMethodRaw(ctx context.Context, token, method string, payload json.RawMessage) error {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		trimmedPayload = []byte("{}")
	}

	_, err := client.call(ctx, token, method, trimmedPayload)
	if err != nil {
		return err
	}

	return nil
}

func (client *Client) callAndDecode(ctx context.Context, token, method string, payload []byte, target any) error {
	result, err := client.call(ctx, token, method, payload)
	if err != nil {
		return err
	}

	trimmed := bytes.TrimSpace(result)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	if err := json.Unmarshal(trimmed, target); err != nil {
		return fmt.Errorf("decode %s result: %w", method, err)
	}

	return nil
}

func (client *Client) call(ctx context.Context, token, method string, payload []byte) (json.RawMessage, error) {
	token = strings.TrimSpace(token)
	method = strings.TrimSpace(method)
	if token == "" {
		return nil, errors.New("telegram token is empty")
	}
	if method == "" {
		return nil, errors.New("telegram method is empty")
	}

	if len(payload) == 0 {
		payload = []byte("{}")
	}

	endpoint := fmt.Sprintf("%s/bot%s/%s", strings.TrimRight(client.baseURL, "/"), token, method)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request for %s: %w", method, err)
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", client.userAgent)

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, &APIError{Method: method, Description: err.Error(), Temporary: true}
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read response for %s: %w", method, err)
	}

	var envelope apiEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		errorDescription := strings.TrimSpace(string(body))
		if errorDescription == "" {
			errorDescription = err.Error()
		}

		apiErr := &APIError{
			Method:      method,
			Description: errorDescription,
			StatusCode:  response.StatusCode,
			Temporary:   response.StatusCode >= 500,
		}
		return nil, apiErr
	}

	if envelope.OK {
		return envelope.Result, nil
	}

	apiErr := &APIError{
		Method:      method,
		Description: envelope.Description,
		ErrorCode:   envelope.ErrorCode,
		StatusCode:  response.StatusCode,
	}

	if envelope.Parameters != nil && envelope.Parameters.RetryAfter > 0 {
		apiErr.RetryAfter = time.Duration(envelope.Parameters.RetryAfter) * time.Second
	}

	if apiErr.ErrorCode == 429 || apiErr.RetryAfter > 0 || response.StatusCode >= 500 {
		apiErr.Temporary = true
	}

	return nil, apiErr
}

type apiEnvelope struct {
	OK          bool                `json:"ok"`
	Result      json.RawMessage     `json:"result"`
	Description string              `json:"description"`
	ErrorCode   int                 `json:"error_code"`
	Parameters  *responseParameters `json:"parameters"`
}

type responseParameters struct {
	RetryAfter      int   `json:"retry_after"`
	MigrateToChatID int64 `json:"migrate_to_chat_id"`
}
