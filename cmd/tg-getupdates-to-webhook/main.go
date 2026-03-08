package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tg-getupdates-to-webhook/internal/bridge"
	"tg-getupdates-to-webhook/internal/config"
	"tg-getupdates-to-webhook/internal/storage"
	"tg-getupdates-to-webhook/internal/telegram"
)

func main() {
	os.Exit(run())
}

func run() int {
	return runCommand(os.Args[1:], os.Stdout, os.Stderr)
}

func runCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	binaryName := filepath.Base(os.Args[0])

	if len(args) == 0 {
		return runBridge(args, stdout, stderr)
	}

	switch args[0] {
	case "run":
		return runBridge(args[1:], stdout, stderr)
	case "init-config":
		return runInitConfig(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printRootHelp(stdout, binaryName)
		return 0
	default:
		if strings.HasPrefix(args[0], "-") {
			return runBridge(args, stdout, stderr)
		}

		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printRootHelp(stderr, binaryName)
		return 2
	}
}

func runBridge(args []string, stdout io.Writer, stderr io.Writer) int {
	configPath, shouldRun, exitCode := parseRunFlags(args, stdout, stderr)
	if !shouldRun {
		return exitCode
	}

	logger := slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("failed to load config", "path", configPath, "error", err)
		return 1
	}

	tgClient := telegram.NewClient(cfg.TelegramTimeout, cfg.UserAgent)
	sqliteStore, err := storage.NewSQLiteStore(cfg.SQLitePath)
	if err != nil {
		logger.Error("failed to initialize sqlite store", "path", cfg.SQLitePath, "error", err)
		return 1
	}
	defer func() {
		if closeErr := sqliteStore.Close(); closeErr != nil {
			logger.Error("failed to close sqlite store", "error", closeErr)
		}
	}()

	backendClient := &http.Client{Timeout: cfg.BackendTimeout}
	service := bridge.NewService(cfg, tgClient, sqliteStore, backendClient, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	healthServer := &http.Server{
		Addr:              cfg.HealthListenAddr,
		Handler:           service.HealthHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("health server started", "addr", cfg.HealthListenAddr)
		if err := healthServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server stopped with error", "error", err)
			stop()
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := healthServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("failed to shutdown health server", "error", err)
		}
	}()

	logger.Info("starting bridge", "bot_count", len(cfg.Bots), "health_addr", cfg.HealthListenAddr)
	if err := service.Run(ctx); err != nil {
		logger.Error("bridge stopped with error", "error", err)
		return 1
	}

	logger.Info("bridge stopped")
	return 0
}

func parseRunFlags(args []string, stdout io.Writer, stderr io.Writer) (string, bool, int) {
	flagSet := flag.NewFlagSet("run", flag.ContinueOnError)
	flagSet.SetOutput(stderr)

	configPath := flagSet.String("config", "config.json", "Path to JSON config file")
	flagSet.Usage = func() {
		printRunHelp(stdout, filepath.Base(os.Args[0]))
	}

	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", false, 0
		}

		printRunHelp(stderr, filepath.Base(os.Args[0]))
		return "", false, 2
	}

	if flagSet.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected positional arguments: %s\n\n", strings.Join(flagSet.Args(), " "))
		printRunHelp(stderr, filepath.Base(os.Args[0]))
		return "", false, 2
	}

	return *configPath, true, 0
}

func printRootHelp(output io.Writer, binaryName string) {
	_, _ = fmt.Fprintf(output, `tg-getupdates-to-webhook

Compatibility bridge for legacy Telegram bots that only support webhook handlers.

The tool has two workflows:
  1) Bootstrap config from existing Telegram webhook (init-config)
  2) Run bridge service in polling mode (run)

Usage:
  %s [command] [options]

Commands:
  run          Start bridge service (default command)
  init-config  Generate primary config from bot token and switch bot to polling mode
  help         Show this help

Bootstrap workflow (recommended first run):
  %s init-config -token "<BOT_TOKEN>" -output ./config.json
    - Reads current webhook URL via getWebhookInfo
    - Uses this URL as backend_url in generated config
    - Deletes webhook (switches bot to getUpdates/polling mode)
    - Verifies webhook was removed
    - Writes config JSON

Run workflow:
  %s run -config ./config.json
  # or (default command)
  %s -config ./config.json

Core config keys:
  polling_timeout_seconds   Long polling timeout for getUpdates
  polling_limit             Max updates per getUpdates call (1..100)
  sqlite_path               SQLite path (offsets + interaction logs)
  health_listen_addr        Health endpoint bind address (for example :9090)
  bots[].name               Unique bot identifier
  bots[].token              Bot token (supports ${ENV_VAR})
  bots[].backend_url        Legacy backend webhook endpoint

Monitoring:
  GET http://<health_listen_addr>/healthz
  Includes telegram/backend error counters and lag_by_offset per bot

Quick Start:
  1) Generate config:
       %s init-config -token "<BOT_TOKEN>" -output ./config.json
  2) Export token env var shown by init-config output
  3) Start bridge:
      %s run -config ./config.json

Show detailed command help:
  %s run -h
  %s init-config -h

`, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName)
}

func printRunHelp(output io.Writer, binaryName string) {
	_, _ = fmt.Fprintf(output, `tg-getupdates-to-webhook

Run bridge service for configured bots.

Usage:
  %s run -config ./config.json
  %s -config ./config.json

Flags:
  -config string
        Path to JSON config file (default "config.json")
  -h, -help
        Show this help

Quick Start:
  1) Copy config.example.json to config.json
  2) Fill bots[].name, bots[].token, bots[].backend_url
  3) Export token env vars used in config (for example BOT_1_TOKEN)
  4) Start service:
       go run ./cmd/tg-getupdates-to-webhook -config ./config.json

Configuration keys:
  polling_timeout_seconds   Telegram long polling timeout for getUpdates (default 50)
  polling_limit             Max updates per getUpdates call, 1..100 (default 100)
  backend_timeout_seconds   Timeout for backend webhook POST (default 10)
  telegram_timeout_seconds  Timeout for Telegram API calls except long polling (default 10)
  retry_initial_delay_ms    Initial retry backoff delay on temporary failures (default 1000)
  retry_max_delay_ms        Max retry backoff delay (default 30000)
  sqlite_path               SQLite file path for bot_offsets and interaction_logs
  health_listen_addr        Health HTTP bind address, for example ":9090"
  user_agent                User-Agent header for outbound HTTP calls
  bots                      Array of bot configs:
      name                  Unique bot identifier (used as key in bot_offsets)
      token                 Telegram bot token
      backend_url           Legacy backend webhook endpoint (http/https)
      secret_token          Optional X-Telegram-Bot-Api-Secret-Token header
      allowed_updates       Optional getUpdates filter
      drop_pending_updates  deleteWebhook behavior on startup

Runtime behavior:
  - Service calls deleteWebhook on startup for each bot (polling/webhook are exclusive)
  - Offset is loaded from SQLite on startup and updated after successful delivery
  - Non-2xx backend response triggers retry, offset is not advanced
  - Temporary Telegram errors trigger retry; permanent method call errors are logged

Monitoring:
  - Health endpoint: GET /healthz on health_listen_addr
  - Response includes:
      status (ok|degraded)
      metrics.telegram_errors_total
      metrics.backend_errors_total
      metrics.bots[].offset
      metrics.bots[].last_update_id
      metrics.bots[].lag_by_offset
  - lag_by_offset is computed as max(0, (last_update_id + 1) - offset)

Example:
  curl -s http://127.0.0.1:9090/healthz

`, binaryName, binaryName)
}
