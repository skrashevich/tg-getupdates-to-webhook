package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tg-getupdates-to-webhook/internal/bootstrap"
	"tg-getupdates-to-webhook/internal/telegram"
)

const (
	defaultInitOutputPath      = "config.json"
	defaultInitSQLitePath      = "./data/bridge.sqlite3"
	defaultInitHealthAddr      = ":9090"
	defaultInitUserAgent       = "tg-getupdates-to-webhook/1.0"
	defaultInitTelegramTimeout = 15
	defaultInitRequestTimeout  = 45
)

type initConfigOptions struct {
	Request               bootstrap.GenerateRequest
	TelegramTimeoutSecond int
	RequestTimeoutSecond  int
}

func runInitConfig(args []string, stdout io.Writer, stderr io.Writer) int {
	options, shouldRun, exitCode := parseInitConfigFlags(args, stdout, stderr)
	if !shouldRun {
		return exitCode
	}

	userAgent := strings.TrimSpace(options.Request.UserAgent)
	if userAgent == "" {
		userAgent = defaultInitUserAgent
	}

	client := telegram.NewClient(
		time.Duration(options.TelegramTimeoutSecond)*time.Second,
		userAgent+"/init-config",
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(options.RequestTimeoutSecond)*time.Second)
	defer cancel()

	result, err := bootstrap.GenerateConfigAndSwitch(ctx, client, options.Request)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "init-config failed: %v\n", err)
		return 1
	}

	printInitResult(stdout, result)
	return 0
}

func parseInitConfigFlags(args []string, stdout io.Writer, stderr io.Writer) (initConfigOptions, bool, int) {
	flagSet := flag.NewFlagSet("init-config", flag.ContinueOnError)
	flagSet.SetOutput(stderr)

	var options initConfigOptions
	flagSet.StringVar(&options.Request.Token, "token", "", "Telegram bot token (required)")
	flagSet.StringVar(&options.Request.OutputPath, "output", defaultInitOutputPath, "Path for generated config JSON")
	flagSet.BoolVar(&options.Request.Force, "force", false, "Overwrite output file if it already exists")
	flagSet.StringVar(&options.Request.BotName, "name", "", "Bot name in generated config (auto-detected by default)")
	flagSet.StringVar(&options.Request.BackendURLOverride, "backend-url", "", "Override backend_url if webhook URL is empty")
	flagSet.StringVar(&options.Request.SecretToken, "secret-token", "", "Secret token forwarded as X-Telegram-Bot-Api-Secret-Token")
	flagSet.BoolVar(&options.Request.DropPendingUpdates, "drop-pending-updates", false, "Drop pending updates when deleting webhook")
	flagSet.BoolVar(&options.Request.InlineToken, "inline-token", false, "Write token directly into generated config")
	flagSet.StringVar(&options.Request.TokenEnvName, "token-env", "", "Env var name for token placeholder (default derived from bot name)")
	flagSet.StringVar(&options.Request.SQLitePath, "sqlite-path", defaultInitSQLitePath, "sqlite_path for generated config")
	flagSet.StringVar(&options.Request.HealthListenAddr, "health-listen-addr", defaultInitHealthAddr, "health_listen_addr for generated config")
	flagSet.StringVar(&options.Request.UserAgent, "user-agent", defaultInitUserAgent, "user_agent for generated config")
	flagSet.IntVar(&options.TelegramTimeoutSecond, "telegram-timeout-seconds", defaultInitTelegramTimeout, "Timeout for Telegram API calls in init command")
	flagSet.IntVar(&options.RequestTimeoutSecond, "request-timeout-seconds", defaultInitRequestTimeout, "Overall timeout for init command")
	flagSet.BoolVar(&options.Request.PreferAllowedUpdate, "prefer-webhook-allowed-updates", true, "Copy allowed_updates from current webhook into generated config")

	binaryName := filepath.Base(os.Args[0])
	flagSet.Usage = func() {
		printInitHelp(stdout, binaryName)
	}

	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return initConfigOptions{}, false, 0
		}

		printInitHelp(stderr, binaryName)
		return initConfigOptions{}, false, 2
	}

	if flagSet.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected positional arguments: %s\n\n", strings.Join(flagSet.Args(), " "))
		printInitHelp(stderr, binaryName)
		return initConfigOptions{}, false, 2
	}

	if strings.TrimSpace(options.Request.Token) == "" {
		_, _ = fmt.Fprintln(stderr, "-token is required")
		printInitHelp(stderr, binaryName)
		return initConfigOptions{}, false, 2
	}

	if options.TelegramTimeoutSecond <= 0 {
		_, _ = fmt.Fprintln(stderr, "-telegram-timeout-seconds must be positive")
		return initConfigOptions{}, false, 2
	}

	if options.RequestTimeoutSecond <= 0 {
		_, _ = fmt.Fprintln(stderr, "-request-timeout-seconds must be positive")
		return initConfigOptions{}, false, 2
	}

	return options, true, 0
}

func printInitResult(output io.Writer, result bootstrap.GenerateResult) {
	_, _ = fmt.Fprintln(output, "init-config completed successfully")
	_, _ = fmt.Fprintf(output, "- Config path: %s\n", result.OutputPath)
	_, _ = fmt.Fprintf(output, "- Bot name: %s\n", result.BotName)
	_, _ = fmt.Fprintf(output, "- Backend URL: %s\n", result.BackendURL)
	_, _ = fmt.Fprintf(output, "- Pending updates before switch: %d\n", result.PendingUpdateCount)
	if result.HadWebhookConfigured {
		_, _ = fmt.Fprintln(output, "- Previous mode: webhook")
	} else {
		_, _ = fmt.Fprintln(output, "- Previous mode: polling or empty webhook")
	}
	_, _ = fmt.Fprintln(output, "- Current mode: polling (webhook removed)")

	if result.TokenEnvName != "" {
		_, _ = fmt.Fprintln(output, "")
		_, _ = fmt.Fprintln(output, "Set token env var before running bridge:")
		_, _ = fmt.Fprintf(output, "  export %s='<telegram-bot-token>'\n", result.TokenEnvName)
	}
}

func printInitHelp(output io.Writer, binaryName string) {
	_, _ = fmt.Fprintf(output, `Generate primary config for a single legacy bot and switch Telegram delivery to polling mode.

Usage:
  %s init-config -token <bot-token> [options]

What command does:
  1) Calls getWebhookInfo using provided token
  2) Uses current webhook URL as backend_url (or -backend-url override)
  3) Calls deleteWebhook (switches bot from webhook mode to getUpdates mode)
  4) Verifies webhook is removed
  5) Writes generated config JSON

Important:
  - Telegram Bot API has no method named "webUpdates".
  - Polling mode is configured by deleting webhook.
  - By default token is NOT stored in config; command writes ${ENV_VAR} placeholder.

Options:
  -token string
        Telegram bot token (required)
  -output string
        Path for generated config JSON (default "config.json")
  -force
        Overwrite output file if exists
  -name string
        Bot name in generated config (auto-detected via getMe by default)
  -backend-url string
        backend_url override if webhook URL is empty
  -secret-token string
        secret_token field in generated config
  -drop-pending-updates
        Pass drop_pending_updates=true to deleteWebhook
  -inline-token
        Write token directly to config instead of ${ENV_VAR}
  -token-env string
        Env var name for token placeholder (default derived from bot name)
  -sqlite-path string
        sqlite_path in generated config (default "./data/bridge.sqlite3")
  -health-listen-addr string
        health_listen_addr in generated config (default ":9090")
  -user-agent string
        user_agent in generated config (default "tg-getupdates-to-webhook/1.0")
  -telegram-timeout-seconds int
        Per-request timeout for Telegram calls (default 15)
  -request-timeout-seconds int
        Overall command timeout (default 45)
  -prefer-webhook-allowed-updates
        Copy allowed_updates from webhook info to generated config (default true)

Examples:
  %s init-config -token "123456:ABC" -output ./config.json
  %s init-config -token "123456:ABC" -drop-pending-updates -force
  %s init-config -token "123456:ABC" -inline-token -name legacy-bot

`, binaryName, binaryName, binaryName, binaryName)
}
