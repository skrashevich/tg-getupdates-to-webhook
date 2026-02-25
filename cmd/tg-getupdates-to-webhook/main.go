package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	configPath := flag.String("config", "config.json", "Path to JSON config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "path", *configPath, "error", err)
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
