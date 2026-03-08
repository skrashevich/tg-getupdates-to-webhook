package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"tg-getupdates-to-webhook/internal/telegram"
)

const (
	defaultPollingTimeoutSeconds  = 50
	defaultPollingLimit           = 100
	defaultBackendTimeoutSeconds  = 10
	defaultTelegramTimeoutSeconds = 10
	defaultRetryInitialDelayMs    = 1000
	defaultRetryMaxDelayMs        = 30000
	defaultGeneratedSQLitePath    = "./data/bridge.sqlite3"
	defaultGeneratedHealthAddr    = ":9090"
	defaultGeneratedUserAgent     = "tg-getupdates-to-webhook/1.0"
)

var (
	invalidNameChars = regexp.MustCompile(`[^a-z0-9]+`)
	invalidEnvChars  = regexp.MustCompile(`[^A-Z0-9_]+`)
	validEnvName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type TelegramAPI interface {
	GetWebhookInfo(ctx context.Context, token string) (telegram.WebhookInfo, error)
	DeleteWebhook(ctx context.Context, token string, dropPendingUpdates bool) error
	GetMe(ctx context.Context, token string) (telegram.BotUser, error)
}

type GenerateRequest struct {
	Token               string
	OutputPath          string
	Force               bool
	DropPendingUpdates  bool
	BotName             string
	BackendURLOverride  string
	SecretToken         string
	InlineToken         bool
	TokenEnvName        string
	SQLitePath          string
	HealthListenAddr    string
	UserAgent           string
	AllowedUpdates      []string
	PreferAllowedUpdate bool
}

type GenerateResult struct {
	OutputPath            string
	BotName               string
	BackendURL            string
	TokenEnvName          string
	PendingUpdateCount    int
	HadWebhookConfigured  bool
	PollingModeConfigured bool
}

func GenerateConfigAndSwitch(ctx context.Context, api TelegramAPI, request GenerateRequest) (GenerateResult, error) {
	token := strings.TrimSpace(request.Token)
	if token == "" {
		return GenerateResult{}, errors.New("token is required")
	}

	outputPath := strings.TrimSpace(request.OutputPath)
	if outputPath == "" {
		return GenerateResult{}, errors.New("output path is required")
	}

	webhookInfo, err := api.GetWebhookInfo(ctx, token)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("getWebhookInfo failed: %w", err)
	}

	backendURL := strings.TrimSpace(request.BackendURLOverride)
	if backendURL == "" {
		backendURL = strings.TrimSpace(webhookInfo.URL)
	}
	if backendURL == "" {
		return GenerateResult{}, errors.New("webhook URL is empty; provide -backend-url explicitly")
	}

	if err := validateBackendURL(backendURL); err != nil {
		return GenerateResult{}, err
	}

	botUser, err := api.GetMe(ctx, token)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("getMe failed: %w", err)
	}

	botName, err := resolveBotName(request.BotName, botUser)
	if err != nil {
		return GenerateResult{}, err
	}

	tokenValue, tokenEnvName, err := resolveTokenValue(request, botName, token)
	if err != nil {
		return GenerateResult{}, err
	}

	allowedUpdates := cloneStringSlice(request.AllowedUpdates)
	if len(allowedUpdates) == 0 && request.PreferAllowedUpdate {
		allowedUpdates = cloneStringSlice(webhookInfo.AllowedUpdates)
	}

	generated := buildConfig(
		request,
		generatedBot{
			Name:               botName,
			Token:              tokenValue,
			BackendURL:         backendURL,
			SecretToken:        strings.TrimSpace(request.SecretToken),
			AllowedUpdates:     allowedUpdates,
			DropPendingUpdates: request.DropPendingUpdates,
		},
	)

	if err := prepareOutputPath(outputPath, request.Force); err != nil {
		return GenerateResult{}, err
	}

	if err := api.DeleteWebhook(ctx, token, request.DropPendingUpdates); err != nil {
		return GenerateResult{}, fmt.Errorf("deleteWebhook failed: %w", err)
	}

	webhookAfterDelete, err := api.GetWebhookInfo(ctx, token)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("verify webhook state failed: %w", err)
	}

	if strings.TrimSpace(webhookAfterDelete.URL) != "" {
		return GenerateResult{}, errors.New("webhook is still configured after deleteWebhook")
	}

	if err := writeJSON(outputPath, generated); err != nil {
		return GenerateResult{}, err
	}

	return GenerateResult{
		OutputPath:            outputPath,
		BotName:               botName,
		BackendURL:            backendURL,
		TokenEnvName:          tokenEnvName,
		PendingUpdateCount:    webhookInfo.PendingUpdateCount,
		HadWebhookConfigured:  strings.TrimSpace(webhookInfo.URL) != "",
		PollingModeConfigured: true,
	}, nil
}

func buildConfig(request GenerateRequest, bot generatedBot) generatedConfig {
	sqlitePath := strings.TrimSpace(request.SQLitePath)
	if sqlitePath == "" {
		sqlitePath = defaultGeneratedSQLitePath
	}

	healthAddr := strings.TrimSpace(request.HealthListenAddr)
	if healthAddr == "" {
		healthAddr = defaultGeneratedHealthAddr
	}

	userAgent := strings.TrimSpace(request.UserAgent)
	if userAgent == "" {
		userAgent = defaultGeneratedUserAgent
	}

	return generatedConfig{
		PollingTimeoutSeconds:  defaultPollingTimeoutSeconds,
		PollingLimit:           defaultPollingLimit,
		BackendTimeoutSeconds:  defaultBackendTimeoutSeconds,
		TelegramTimeoutSeconds: defaultTelegramTimeoutSeconds,
		RetryInitialDelayMs:    defaultRetryInitialDelayMs,
		RetryMaxDelayMs:        defaultRetryMaxDelayMs,
		SQLitePath:             sqlitePath,
		HealthListenAddr:       healthAddr,
		UserAgent:              userAgent,
		Bots:                   []generatedBot{bot},
	}
}

func prepareOutputPath(outputPath string, force bool) error {
	if !force {
		_, err := os.Stat(outputPath)
		if err == nil {
			return fmt.Errorf("output file already exists: %s (use -force to overwrite)", outputPath)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check output path: %w", err)
		}
	}

	directory := filepath.Dir(outputPath)
	if directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}

	return nil
}

func writeJSON(outputPath string, payload generatedConfig) error {
	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal generated config: %w", err)
	}
	content = append(content, '\n')

	if err := os.WriteFile(outputPath, content, 0o600); err != nil {
		return fmt.Errorf("write generated config: %w", err)
	}

	return nil
}

func validateBackendURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("backend URL is invalid: %w", err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("backend URL must use http or https")
	}

	if strings.TrimSpace(parsed.Host) == "" {
		return errors.New("backend URL must include host")
	}

	return nil
}

func resolveBotName(explicitName string, botUser telegram.BotUser) (string, error) {
	name := sanitizeName(explicitName)
	if name != "" {
		return name, nil
	}

	candidates := []string{
		botUser.Username,
		strings.TrimSpace(strings.Join([]string{botUser.FirstName, botUser.LastName}, " ")),
		fmt.Sprintf("bot-%d", botUser.ID),
	}

	for _, candidate := range candidates {
		name = sanitizeName(candidate)
		if name != "" {
			return name, nil
		}
	}

	return "", errors.New("failed to derive bot name from getMe response")
}

func sanitizeName(raw string) string {
	trimmed := strings.TrimSpace(strings.ToLower(raw))
	if trimmed == "" {
		return ""
	}

	replaced := invalidNameChars.ReplaceAllString(trimmed, "-")
	return strings.Trim(replaced, "-")
}

func resolveTokenValue(request GenerateRequest, botName, token string) (value string, envName string, err error) {
	if request.InlineToken {
		return token, "", nil
	}

	envName = strings.TrimSpace(request.TokenEnvName)
	if envName == "" {
		envName = defaultTokenEnvName(botName)
	}

	if !validEnvName.MatchString(envName) {
		return "", "", fmt.Errorf("invalid token environment variable name: %q", envName)
	}

	return fmt.Sprintf("${%s}", envName), envName, nil
}

func defaultTokenEnvName(botName string) string {
	upper := strings.ToUpper(strings.ReplaceAll(botName, "-", "_"))
	upper = invalidEnvChars.ReplaceAllString(upper, "_")
	upper = strings.Trim(upper, "_")

	if upper == "" {
		upper = "BOT"
	}

	if upper[0] >= '0' && upper[0] <= '9' {
		upper = "BOT_" + upper
	}

	if strings.HasSuffix(upper, "_TOKEN") {
		return upper
	}

	return upper + "_TOKEN"
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

type generatedConfig struct {
	PollingTimeoutSeconds  int            `json:"polling_timeout_seconds"`
	PollingLimit           int            `json:"polling_limit"`
	BackendTimeoutSeconds  int            `json:"backend_timeout_seconds"`
	TelegramTimeoutSeconds int            `json:"telegram_timeout_seconds"`
	RetryInitialDelayMs    int            `json:"retry_initial_delay_ms"`
	RetryMaxDelayMs        int            `json:"retry_max_delay_ms"`
	SQLitePath             string         `json:"sqlite_path"`
	HealthListenAddr       string         `json:"health_listen_addr"`
	UserAgent              string         `json:"user_agent"`
	Bots                   []generatedBot `json:"bots"`
}

type generatedBot struct {
	Name               string   `json:"name"`
	Token              string   `json:"token"`
	BackendURL         string   `json:"backend_url"`
	SecretToken        string   `json:"secret_token,omitempty"`
	AllowedUpdates     []string `json:"allowed_updates,omitempty"`
	DropPendingUpdates bool     `json:"drop_pending_updates"`
}
