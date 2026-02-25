package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"tg-getupdates-to-webhook/internal/bridge"
	"tg-getupdates-to-webhook/internal/config"
	"tg-getupdates-to-webhook/internal/storage"
	"tg-getupdates-to-webhook/internal/telegram"
)

func main() {
	configPath := flag.String("config", "config.json", "Path to JSON config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}

	tgClient := telegram.NewClient(cfg.TelegramTimeout, cfg.UserAgent)
	sqliteStore, err := storage.NewSQLiteStore(cfg.SQLitePath)
	if err != nil {
		logger.Error("failed to initialize sqlite store", "path", cfg.SQLitePath, "error", err)
		os.Exit(1)
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

	logger.Info("starting bridge", "bot_count", len(cfg.Bots))
	if err := service.Run(ctx); err != nil {
		logger.Error("bridge stopped with error", "error", err)
		os.Exit(1)
	}

	logger.Info("bridge stopped")
}
