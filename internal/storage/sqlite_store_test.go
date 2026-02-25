package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteStoreSaveAndLoadOffset(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bridge.sqlite3")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("close sqlite store: %v", closeErr)
		}
	})

	offset, err := store.LoadOffset(context.Background(), "bot-a")
	if err != nil {
		t.Fatalf("LoadOffset returned error: %v", err)
	}
	if offset != 0 {
		t.Fatalf("expected empty offset to be 0, got %d", offset)
	}

	if err := store.SaveOffset(context.Background(), "bot-a", 11); err != nil {
		t.Fatalf("SaveOffset returned error: %v", err)
	}

	offset, err = store.LoadOffset(context.Background(), "bot-a")
	if err != nil {
		t.Fatalf("LoadOffset after SaveOffset returned error: %v", err)
	}
	if offset != 11 {
		t.Fatalf("expected offset 11, got %d", offset)
	}

	if err := store.SaveOffset(context.Background(), "bot-a", 25); err != nil {
		t.Fatalf("SaveOffset update returned error: %v", err)
	}

	offset, err = store.LoadOffset(context.Background(), "bot-a")
	if err != nil {
		t.Fatalf("LoadOffset after update returned error: %v", err)
	}
	if offset != 25 {
		t.Fatalf("expected offset 25, got %d", offset)
	}
}

func TestSQLiteStoreLogInteraction(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bridge.sqlite3")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("close sqlite store: %v", closeErr)
		}
	})

	updateID := int64(42)
	httpStatus := 200
	entry := InteractionLog{
		BotName:        "bot-a",
		Component:      "telegram",
		Event:          "method_ok",
		UpdateID:       &updateID,
		TelegramMethod: "sendMessage",
		HTTPStatus:     &httpStatus,
		Payload:        `{"chat_id":1,"text":"pong"}`,
	}

	if err := store.LogInteraction(context.Background(), entry); err != nil {
		t.Fatalf("LogInteraction returned error: %v", err)
	}

	const query = `
SELECT bot_name, component, event, update_id, telegram_method, http_status, payload_text
FROM interaction_logs
ORDER BY id DESC
LIMIT 1;
`

	var botName string
	var component string
	var event string
	var storedUpdateID int64
	var method string
	var storedHTTPStatus int
	var payload string

	err = store.db.QueryRowContext(context.Background(), query).Scan(
		&botName,
		&component,
		&event,
		&storedUpdateID,
		&method,
		&storedHTTPStatus,
		&payload,
	)
	if err != nil {
		t.Fatalf("query interaction_logs: %v", err)
	}

	if botName != entry.BotName {
		t.Fatalf("expected bot_name %q, got %q", entry.BotName, botName)
	}
	if component != entry.Component {
		t.Fatalf("expected component %q, got %q", entry.Component, component)
	}
	if event != entry.Event {
		t.Fatalf("expected event %q, got %q", entry.Event, event)
	}
	if storedUpdateID != *entry.UpdateID {
		t.Fatalf("expected update_id %d, got %d", *entry.UpdateID, storedUpdateID)
	}
	if method != entry.TelegramMethod {
		t.Fatalf("expected method %q, got %q", entry.TelegramMethod, method)
	}
	if storedHTTPStatus != *entry.HTTPStatus {
		t.Fatalf("expected http_status %d, got %d", *entry.HTTPStatus, storedHTTPStatus)
	}
	if payload != entry.Payload {
		t.Fatalf("expected payload %q, got %q", entry.Payload, payload)
	}
}
