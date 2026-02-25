package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	defaultBusyTimeoutMs = 5000
	maxStoredTextLength  = 1 << 20
)

type SQLiteStore struct {
	db *sql.DB
}

type InteractionLog struct {
	BotName        string
	Component      string
	Event          string
	UpdateID       *int64
	TelegramMethod string
	HTTPStatus     *int
	Payload        string
	ErrorText      string
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return nil, errors.New("sqlite path is required")
	}

	if cleanPath != ":memory:" {
		directory := filepath.Dir(cleanPath)
		if directory != "." {
			if err := os.MkdirAll(directory, 0o755); err != nil {
				return nil, fmt.Errorf("create sqlite directory: %w", err)
			}
		}
	}

	dsn := makeDSN(cleanPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &SQLiteStore{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (store *SQLiteStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *SQLiteStore) LoadOffset(ctx context.Context, botName string) (int64, error) {
	botName = strings.TrimSpace(botName)
	if botName == "" {
		return 0, errors.New("bot name is required")
	}

	const query = `
SELECT update_offset
FROM bot_offsets
WHERE bot_name = ?;
`

	var offset int64
	err := store.db.QueryRowContext(ctx, query, botName).Scan(&offset)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load offset: %w", err)
	}

	return offset, nil
}

func (store *SQLiteStore) SaveOffset(ctx context.Context, botName string, offset int64) error {
	botName = strings.TrimSpace(botName)
	if botName == "" {
		return errors.New("bot name is required")
	}

	const statement = `
INSERT INTO bot_offsets (bot_name, update_offset, updated_at)
VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT(bot_name) DO UPDATE SET
  update_offset = excluded.update_offset,
  updated_at = excluded.updated_at;
`

	if _, err := store.db.ExecContext(ctx, statement, botName, offset); err != nil {
		return fmt.Errorf("save offset: %w", err)
	}

	return nil
}

func (store *SQLiteStore) LogInteraction(ctx context.Context, entry InteractionLog) error {
	botName := strings.TrimSpace(entry.BotName)
	if botName == "" {
		return errors.New("bot name is required")
	}

	component := strings.TrimSpace(entry.Component)
	if component == "" {
		return errors.New("component is required")
	}

	event := strings.TrimSpace(entry.Event)
	if event == "" {
		return errors.New("event is required")
	}

	const statement = `
INSERT INTO interaction_logs (
  bot_name,
  component,
  event,
  update_id,
  telegram_method,
  http_status,
  payload_text,
  error_text
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);
`

	var updateID any
	if entry.UpdateID != nil {
		updateID = *entry.UpdateID
	}

	var httpStatus any
	if entry.HTTPStatus != nil {
		httpStatus = *entry.HTTPStatus
	}

	telegramMethod := nullIfBlank(entry.TelegramMethod)
	payload := nullIfBlank(limitString(entry.Payload, maxStoredTextLength))
	errorText := nullIfBlank(limitString(entry.ErrorText, maxStoredTextLength))

	if _, err := store.db.ExecContext(ctx, statement, botName, component, event, updateID, telegramMethod, httpStatus, payload, errorText); err != nil {
		return fmt.Errorf("log interaction: %w", err)
	}

	return nil
}

func (store *SQLiteStore) init(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS bot_offsets (
  bot_name TEXT PRIMARY KEY,
  update_offset INTEGER NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS interaction_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bot_name TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  component TEXT NOT NULL,
  event TEXT NOT NULL,
  update_id INTEGER,
  telegram_method TEXT,
  http_status INTEGER,
  payload_text TEXT,
  error_text TEXT
);

CREATE INDEX IF NOT EXISTS idx_interaction_logs_bot_created
ON interaction_logs(bot_name, created_at);
`

	if _, err := store.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize sqlite schema: %w", err)
	}

	return nil
}

func makeDSN(path string) string {
	if path == ":memory:" {
		return fmt.Sprintf("file::memory:?cache=shared&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", defaultBusyTimeoutMs)
	}

	return fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path, defaultBusyTimeoutMs)
}

func nullIfBlank(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func limitString(value string, limit int) string {
	if limit <= 0 {
		return ""
	}

	if len(value) <= limit {
		return value
	}

	const suffix = "...(truncated)"
	if limit <= len(suffix) {
		return suffix[:limit]
	}

	return value[:limit-len(suffix)] + suffix
}
