package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultPollingTimeoutSeconds  = 50
	defaultPollingLimit           = 100
	defaultBackendTimeoutSeconds  = 10
	defaultTelegramTimeoutSeconds = 10
	defaultRetryInitialDelayMs    = 1000
	defaultRetryMaxDelayMs        = 30000
	defaultSQLitePath             = "bridge.sqlite3"
	defaultHealthListenAddr       = ":9090"
	defaultUserAgent              = "tg-getupdates-to-webhook/1.0"
)

type Config struct {
	PollingTimeout    time.Duration
	PollingLimit      int
	BackendTimeout    time.Duration
	TelegramTimeout   time.Duration
	RetryInitialDelay time.Duration
	RetryMaxDelay     time.Duration
	SQLitePath        string
	HealthListenAddr  string
	UserAgent         string
	Bots              []BotConfig
}

type BotConfig struct {
	Name               string   `json:"name"`
	Token              string   `json:"token"`
	BackendURL         string   `json:"backend_url"`
	SecretToken        string   `json:"secret_token"`
	AllowedUpdates     []string `json:"allowed_updates"`
	DropPendingUpdates bool     `json:"drop_pending_updates"`
}

type fileConfig struct {
	PollingTimeoutSeconds  int         `json:"polling_timeout_seconds"`
	PollingLimit           int         `json:"polling_limit"`
	BackendTimeoutSeconds  int         `json:"backend_timeout_seconds"`
	TelegramTimeoutSeconds int         `json:"telegram_timeout_seconds"`
	RetryInitialDelayMs    int         `json:"retry_initial_delay_ms"`
	RetryMaxDelayMs        int         `json:"retry_max_delay_ms"`
	SQLitePath             string      `json:"sqlite_path"`
	HealthListenAddr       string      `json:"health_listen_addr"`
	UserAgent              string      `json:"user_agent"`
	Bots                   []BotConfig `json:"bots"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	expanded := os.ExpandEnv(string(data))
	raw, err := parseConfig(expanded)
	if err != nil {
		return Config{}, err
	}

	normalizeDefaults(&raw)
	if err := validateBots(raw.Bots); err != nil {
		return Config{}, err
	}

	cfg := Config{
		PollingTimeout:    time.Duration(raw.PollingTimeoutSeconds) * time.Second,
		PollingLimit:      raw.PollingLimit,
		BackendTimeout:    time.Duration(raw.BackendTimeoutSeconds) * time.Second,
		TelegramTimeout:   time.Duration(raw.TelegramTimeoutSeconds) * time.Second,
		RetryInitialDelay: time.Duration(raw.RetryInitialDelayMs) * time.Millisecond,
		RetryMaxDelay:     time.Duration(raw.RetryMaxDelayMs) * time.Millisecond,
		SQLitePath:        raw.SQLitePath,
		HealthListenAddr:  raw.HealthListenAddr,
		UserAgent:         raw.UserAgent,
		Bots:              raw.Bots,
	}

	return cfg, nil
}

func parseConfig(input string) (fileConfig, error) {
	var raw fileConfig
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&raw); err != nil {
		return fileConfig{}, fmt.Errorf("parse config JSON: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fileConfig{}, errors.New("config has trailing JSON data")
	}

	return raw, nil
}

func normalizeDefaults(raw *fileConfig) {
	raw.PollingTimeoutSeconds = positiveOrDefault(raw.PollingTimeoutSeconds, defaultPollingTimeoutSeconds)
	raw.PollingLimit = rawPollingLimit(raw.PollingLimit)
	raw.BackendTimeoutSeconds = positiveOrDefault(raw.BackendTimeoutSeconds, defaultBackendTimeoutSeconds)
	raw.TelegramTimeoutSeconds = positiveOrDefault(raw.TelegramTimeoutSeconds, defaultTelegramTimeoutSeconds)
	raw.RetryInitialDelayMs = positiveOrDefault(raw.RetryInitialDelayMs, defaultRetryInitialDelayMs)
	raw.RetryMaxDelayMs = positiveOrDefault(raw.RetryMaxDelayMs, defaultRetryMaxDelayMs)

	if raw.RetryMaxDelayMs < raw.RetryInitialDelayMs {
		raw.RetryMaxDelayMs = raw.RetryInitialDelayMs
	}

	raw.SQLitePath = strings.TrimSpace(raw.SQLitePath)
	if raw.SQLitePath == "" {
		raw.SQLitePath = defaultSQLitePath
	}

	raw.HealthListenAddr = strings.TrimSpace(raw.HealthListenAddr)
	if raw.HealthListenAddr == "" {
		raw.HealthListenAddr = defaultHealthListenAddr
	}

	raw.UserAgent = strings.TrimSpace(raw.UserAgent)
	if raw.UserAgent == "" {
		raw.UserAgent = defaultUserAgent
	}

	for index := range raw.Bots {
		raw.Bots[index].Name = strings.TrimSpace(raw.Bots[index].Name)
		raw.Bots[index].Token = strings.TrimSpace(raw.Bots[index].Token)
		raw.Bots[index].BackendURL = strings.TrimSpace(raw.Bots[index].BackendURL)
		raw.Bots[index].SecretToken = strings.TrimSpace(raw.Bots[index].SecretToken)
		if raw.Bots[index].Name == "" {
			raw.Bots[index].Name = fmt.Sprintf("bot-%d", index+1)
		}
	}
}

func validateBots(bots []BotConfig) error {
	if len(bots) == 0 {
		return errors.New("config.bots must contain at least one bot")
	}

	names := make(map[string]struct{}, len(bots))

	for index, bot := range bots {
		if bot.Name == "" {
			return fmt.Errorf("bots[%d].name is required", index)
		}

		if _, exists := names[bot.Name]; exists {
			return fmt.Errorf("bots[%d].name %q is duplicated", index, bot.Name)
		}
		names[bot.Name] = struct{}{}

		if bot.Token == "" {
			return fmt.Errorf("bots[%d].token is required", index)
		}

		if bot.BackendURL == "" {
			return fmt.Errorf("bots[%d].backend_url is required", index)
		}

		parsed, err := url.Parse(bot.BackendURL)
		if err != nil {
			return fmt.Errorf("bots[%d].backend_url is invalid: %w", index, err)
		}

		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("bots[%d].backend_url must use http or https", index)
		}

		if parsed.Host == "" {
			return fmt.Errorf("bots[%d].backend_url must include host", index)
		}
	}

	return nil
}

func positiveOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func rawPollingLimit(value int) int {
	if value < 1 || value > 100 {
		return defaultPollingLimit
	}
	return value
}
