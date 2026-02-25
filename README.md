# tg-getupdates-to-webhook

Compatibility bridge for legacy Telegram bots that only support webhook mode. This service polls updates via `getUpdates` and forwards each update to your existing webhook backend, then proxies webhook-style method responses back to Telegram.

## Why this exists

Some legacy bots only know how to handle webhook delivery and return inline Bot API calls in webhook responses. This bridge allows you to keep those backends unchanged while switching Telegram delivery to polling.

## What it does

- Supports multiple bots in one process (each with its own token and backend URL)
- Uses long polling (`getUpdates`) per bot
- Forwards every update as JSON `POST` to configured backend URL
- Proxies webhook response payloads with `method` field back to Telegram Bot API
- Persists per-bot offsets in SQLite (`bot_offsets` table)
- Persists interaction logs in SQLite (`interaction_logs` table)
- Uses retry with exponential backoff for temporary failures

## Important behavior

- On startup, bridge calls `deleteWebhook` for every bot (Telegram polling/webhook modes are mutually exclusive)
- Polling offset is restored from SQLite on startup
- Update offset advances only after successful backend delivery
- Non-2xx backend response is treated as delivery failure and retried
- If backend returns JSON with `method`, bridge calls `/bot<TOKEN>/<method>` using remaining JSON fields as request payload
- Permanent Telegram errors on proxied reply (e.g. bad request) are logged and not retried for that update

## Configuration

Copy `config.example.json` to `config.json` and adjust values.

Environment variables inside config are expanded (for example `"token": "${BOT_1_TOKEN}"`).

Important config fields:

- `sqlite_path` - path to SQLite DB file (created automatically)
- `bots[].name` - unique bot identifier (used as key in `bot_offsets`)

## Run

```bash
go run ./cmd/tg-getupdates-to-webhook -config config.json
```

## Test

```bash
go test ./...
```
