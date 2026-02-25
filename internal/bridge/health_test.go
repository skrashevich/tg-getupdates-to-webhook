package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"tg-getupdates-to-webhook/internal/config"
)

func TestHealthHandlerReturnsCountersAndLag(t *testing.T) {
	t.Parallel()

	service := NewService(
		config.Config{Bots: []config.BotConfig{{Name: "bot-a"}}},
		&fakeTelegram{},
		fakeStateStore{},
		http.DefaultClient,
		discardLogger(),
	)

	service.metrics.setOffset("bot-a", 5)
	service.metrics.observeUpdate("bot-a", 9)
	service.metrics.incTelegramError("bot-a", errors.New("telegram failed"))
	service.metrics.incBackendError("bot-a", errors.New("backend failed"))

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	service.HealthHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", recorder.Code)
	}

	var response healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	if response.Status != "degraded" {
		t.Fatalf("expected degraded status, got %q", response.Status)
	}

	if response.Metrics.TelegramErrorsTotal != 1 {
		t.Fatalf("expected telegram_errors_total 1, got %d", response.Metrics.TelegramErrorsTotal)
	}

	if response.Metrics.BackendErrorsTotal != 1 {
		t.Fatalf("expected backend_errors_total 1, got %d", response.Metrics.BackendErrorsTotal)
	}

	if len(response.Metrics.Bots) != 1 {
		t.Fatalf("expected one bot metric row, got %d", len(response.Metrics.Bots))
	}

	bot := response.Metrics.Bots[0]
	if bot.Name != "bot-a" {
		t.Fatalf("expected bot-a metric row, got %q", bot.Name)
	}

	if bot.Offset != 5 {
		t.Fatalf("expected offset 5, got %d", bot.Offset)
	}

	if bot.LastUpdateID == nil || *bot.LastUpdateID != 9 {
		t.Fatalf("expected last_update_id 9, got %#v", bot.LastUpdateID)
	}

	if bot.LagByOffset != 5 {
		t.Fatalf("expected lag_by_offset 5, got %d", bot.LagByOffset)
	}

	if bot.TelegramErrors != 1 {
		t.Fatalf("expected telegram_errors 1, got %d", bot.TelegramErrors)
	}

	if bot.BackendErrors != 1 {
		t.Fatalf("expected backend_errors 1, got %d", bot.BackendErrors)
	}
}

func TestHealthHandlerRejectsNonGet(t *testing.T) {
	t.Parallel()

	service := NewService(
		config.Config{Bots: []config.BotConfig{{Name: "bot-a"}}},
		&fakeTelegram{},
		fakeStateStore{},
		http.DefaultClient,
		discardLogger(),
	)

	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	recorder := httptest.NewRecorder()

	service.HealthHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected HTTP 405, got %d", recorder.Code)
	}
}
