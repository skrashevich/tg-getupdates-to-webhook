# tg-getupdates-to-webhook

<!-- badges:start -->
[![GitHub stars](https://img.shields.io/github/stars/skrashevich/tg-getupdates-to-webhook?style=flat-square)](https://github.com/skrashevich/tg-getupdates-to-webhook/stargazers)
[![Last commit](https://img.shields.io/github/last-commit/skrashevich/tg-getupdates-to-webhook?style=flat-square)](https://github.com/skrashevich/tg-getupdates-to-webhook/commits/main)
<!-- badges:end -->


Compatibility bridge for legacy Telegram bots that only support webhook handlers.
The service reads updates via Telegram `getUpdates`, forwards them to your existing backend as webhook-like `POST`, and proxies webhook-style method responses back to Telegram.

## Features

- Multi-bot support in one process (`token` + `backend_url` per bot)
- Long polling via `getUpdates` with per-bot offset state
- SQLite persistence:
  - `bot_offsets` table for durable offsets
  - `interaction_logs` table for full request/response interaction traces
- Health endpoint `GET /healthz` with error counters and lag metrics
- Retry with exponential backoff for temporary failures
- Built-in bootstrap command `init-config` for webhook -> polling migration
- Can be used as a Go library (all packages are importable)

## Using as a library

All packages (`config`, `telegram`, `storage`, `bridge`, `bootstrap`) are importable from external Go projects.

```bash
go get github.com/skrashevich/tg-getupdates-to-webhook
```

### Packages

| Package | Description |
|---------|-------------|
| `config` | Load and validate JSON configuration with env var expansion |
| `telegram` | Telegram Bot API client (`getUpdates`, `deleteWebhook`, `getMe`, generic method calls) |
| `storage` | SQLite-based offset persistence and interaction logging |
| `bridge` | Core bridge service: polls Telegram, delivers updates to backend, proxies webhook replies |
| `bootstrap` | Config generation: reads webhook info, switches bot to polling, writes config file |

### Example: embed bridge in your application

```go
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/skrashevich/tg-getupdates-to-webhook/bridge"
	"github.com/skrashevich/tg-getupdates-to-webhook/config"
	"github.com/skrashevich/tg-getupdates-to-webhook/storage"
	"github.com/skrashevich/tg-getupdates-to-webhook/telegram"
)

func main() {
	cfg, _ := config.Load("config.json")
	tg := telegram.NewClient(cfg.TelegramTimeout, cfg.UserAgent)
	store, _ := storage.NewSQLiteStore(cfg.SQLitePath)
	defer store.Close()

	svc := bridge.NewService(cfg, tg, store, &http.Client{Timeout: cfg.BackendTimeout}, slog.Default())

	// HealthHandler returns an http.Handler you can mount on your own mux
	http.Handle("/healthz", svc.HealthHandler())

	svc.Run(context.Background())
}
```

### Example: use only the Telegram client

```go
tg := telegram.NewClient(10*time.Second, "my-app/1.0")

info, _ := tg.GetWebhookInfo(ctx, token)
me, _ := tg.GetMe(ctx, token)
updates, _ := tg.GetUpdates(ctx, token, telegram.GetUpdatesRequest{Offset: 0, Limit: 100, Timeout: 50})
```

### Key interfaces

The `bridge` package defines two interfaces for dependency injection:

- `bridge.TelegramAPI` — Telegram API operations (implement to mock or replace the client)
- `bridge.StateStore` — offset persistence and interaction logging (implement for custom storage)

## Prerequisites

- Go 1.24+
- Network access to:
  - `https://api.telegram.org`
  - your legacy backend endpoints
- Writable location for SQLite DB (`sqlite_path`)

## Configuration

Copy template:

```bash
cp config.example.json config.json
```

Environment variables inside config are expanded automatically, for example:

```json
"token": "${BOT_1_TOKEN}"
```

### Top-level keys

- `polling_timeout_seconds` - long polling timeout for `getUpdates` (default `50`)
- `polling_limit` - max updates per poll, `1..100` (default `100`)
- `backend_timeout_seconds` - timeout for backend webhook request (default `10`)
- `telegram_timeout_seconds` - timeout for Telegram API calls except long poll (default `10`)
- `retry_initial_delay_ms` - initial retry delay (default `1000`)
- `retry_max_delay_ms` - max retry delay (default `30000`)
- `sqlite_path` - SQLite file path (default `bridge.sqlite3`)
- `health_listen_addr` - bind address for health server (default `:9090`)
- `user_agent` - User-Agent for outbound HTTP calls
- `bots` - list of bot configs

### Bot fields

- `name` - unique bot identifier (required, used as key in `bot_offsets`)
- `token` - Telegram bot token (required)
- `backend_url` - legacy webhook endpoint (required, `http` or `https`)
- `secret_token` - optional value passed as `X-Telegram-Bot-Api-Secret-Token`
- `allowed_updates` - optional Telegram update filter
- `drop_pending_updates` - passed to `deleteWebhook` at startup

## Bootstrap (init-config)

Use `init-config` to generate first working config from an existing bot token.

What it does:

1. Calls `getWebhookInfo`
2. Reads current webhook URL and uses it as `backend_url` in generated config
3. Calls `deleteWebhook` to switch bot to polling mode (`getUpdates`)
4. Verifies webhook is removed
5. Writes config JSON

Important: Telegram Bot API has no method named `webUpdates`; polling mode is configured by removing webhook.

### Basic bootstrap

```bash
go run ./cmd/tg-getupdates-to-webhook init-config -token "<BOT_TOKEN>" -output ./config.json
```

By default token is written as `${ENV_VAR}` placeholder (safer than inline token). Command prints which env var to export.

### Useful bootstrap options

```bash
# Drop pending updates while switching from webhook to polling
go run ./cmd/tg-getupdates-to-webhook init-config -token "<BOT_TOKEN>" -drop-pending-updates

# Force overwrite existing config
go run ./cmd/tg-getupdates-to-webhook init-config -token "<BOT_TOKEN>" -force

# Write token directly into config (not recommended)
go run ./cmd/tg-getupdates-to-webhook init-config -token "<BOT_TOKEN>" -inline-token

# If webhook URL is empty, provide backend explicitly
go run ./cmd/tg-getupdates-to-webhook init-config -token "<BOT_TOKEN>" -backend-url "https://legacy.example/webhook"
```

### Bootstrap help

```bash
./bin/tg-getupdates-to-webhook init-config -h
```

## Run

### Run from source

```bash
go run ./cmd/tg-getupdates-to-webhook -config ./config.json
```

### Build binary and run

```bash
go build -o ./bin/tg-getupdates-to-webhook ./cmd/tg-getupdates-to-webhook
./bin/tg-getupdates-to-webhook -config ./config.json
```

### Help output

```bash
./bin/tg-getupdates-to-webhook -h
```

The root help includes full bootstrap/run workflow and monitoring overview.

For command-specific detailed help:

```bash
./bin/tg-getupdates-to-webhook run -h
./bin/tg-getupdates-to-webhook init-config -h
```

## Runtime behavior

- On startup, bridge calls `deleteWebhook` for every bot (Telegram polling/webhook are mutually exclusive)
- Offset is loaded from SQLite before polling starts
- Offset is advanced and persisted only after successful update delivery
- Backend non-2xx response is treated as delivery failure (retry, offset unchanged)
- If backend returns JSON with `method`, bridge calls `/bot<TOKEN>/<method>` with remaining JSON fields
- Temporary Telegram API failures are retried; permanent method call failures are logged and treated as delivered

## Operations

### Graceful shutdown

- Process handles `SIGINT`/`SIGTERM`
- Polling stops, health server is shutdown, SQLite connection is closed

### SQLite files

- DB file is created automatically at `sqlite_path`
- Keep DB on persistent storage if you need restart-safe offsets and audit logs

### Backup example

```bash
cp ./data/bridge.sqlite3 ./data/bridge.sqlite3.bak
```

### Inspect offsets

```bash
sqlite3 ./data/bridge.sqlite3 'SELECT bot_name, update_offset, updated_at FROM bot_offsets;'
```

### Inspect latest interaction logs

```bash
sqlite3 ./data/bridge.sqlite3 'SELECT id, bot_name, created_at, component, event, update_id, telegram_method, http_status FROM interaction_logs ORDER BY id DESC LIMIT 50;'
```

## Monitoring

Health endpoint is available at:

```text
http://<health_listen_addr>/healthz
```

Example request:

```bash
curl -s http://127.0.0.1:9090/healthz
```

Response contains:

- `status` - `ok` or `degraded`
- `metrics.telegram_errors_total` - total Telegram-side errors observed in runtime
- `metrics.backend_errors_total` - total backend-side errors observed in runtime
- `metrics.bots[]` per bot:
  - `offset`
  - `last_update_id`
  - `lag_by_offset`
  - `telegram_errors`
  - `backend_errors`

`lag_by_offset` formula:

```text
max(0, (last_update_id + 1) - offset)
```

### Suggested alerts

- `status=degraded` for sustained period (for example >5m)
- monotonically increasing `backend_errors_total`
- monotonically increasing `telegram_errors_total`
- `lag_by_offset > 0` for critical bots for sustained period

## Test

```bash
go test ./...
```
