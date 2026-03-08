package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/tg-getupdates-to-webhook/telegram"
)

func TestGenerateConfigAndSwitchSuccess(t *testing.T) {
	t.Parallel()

	outputPath := filepath.Join(t.TempDir(), "config.json")
	fakeAPI := &fakeTelegramAPI{
		webhookInfo: telegram.WebhookInfo{
			URL:                "https://legacy.example/webhook",
			PendingUpdateCount: 7,
			AllowedUpdates:     []string{"message", "callback_query"},
		},
		webhookInfoAfterDelete: telegram.WebhookInfo{},
		botUser: telegram.BotUser{
			ID:       100500,
			Username: "Legacy_Bot",
		},
	}

	result, err := GenerateConfigAndSwitch(context.Background(), fakeAPI, GenerateRequest{
		Token:               "123:ABC",
		OutputPath:          outputPath,
		DropPendingUpdates:  true,
		PreferAllowedUpdate: true,
	})
	if err != nil {
		t.Fatalf("GenerateConfigAndSwitch returned error: %v", err)
	}

	if !fakeAPI.deleteCalled {
		t.Fatalf("expected deleteWebhook to be called")
	}

	if !fakeAPI.deleteDropPending {
		t.Fatalf("expected drop_pending_updates=true")
	}

	if result.BotName != "legacy-bot" {
		t.Fatalf("expected bot name legacy-bot, got %q", result.BotName)
	}

	if result.TokenEnvName != "LEGACY_BOT_TOKEN" {
		t.Fatalf("expected token env LEGACY_BOT_TOKEN, got %q", result.TokenEnvName)
	}

	if !result.HadWebhookConfigured {
		t.Fatalf("expected HadWebhookConfigured=true")
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	var generated generatedConfig
	if err := json.Unmarshal(content, &generated); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}

	if len(generated.Bots) != 1 {
		t.Fatalf("expected one bot in config, got %d", len(generated.Bots))
	}

	bot := generated.Bots[0]
	if bot.BackendURL != "https://legacy.example/webhook" {
		t.Fatalf("expected backend_url from webhook, got %q", bot.BackendURL)
	}

	if bot.Token != "${LEGACY_BOT_TOKEN}" {
		t.Fatalf("expected token placeholder, got %q", bot.Token)
	}

	if !stringSlicesEqual(bot.AllowedUpdates, []string{"message", "callback_query"}) {
		t.Fatalf("expected allowed_updates copied from webhook info")
	}

	if !bot.DropPendingUpdates {
		t.Fatalf("expected drop_pending_updates=true")
	}
}

func TestGenerateConfigAndSwitchRequiresWebhookOrOverride(t *testing.T) {
	t.Parallel()

	fakeAPI := &fakeTelegramAPI{
		webhookInfo:            telegram.WebhookInfo{},
		webhookInfoAfterDelete: telegram.WebhookInfo{},
		botUser:                telegram.BotUser{ID: 1, Username: "bot"},
	}

	_, err := GenerateConfigAndSwitch(context.Background(), fakeAPI, GenerateRequest{
		Token:      "123:ABC",
		OutputPath: filepath.Join(t.TempDir(), "config.json"),
	})
	if err == nil {
		t.Fatalf("expected error when webhook URL is missing")
	}

	if !strings.Contains(err.Error(), "webhook URL is empty") {
		t.Fatalf("unexpected error: %v", err)
	}

	if fakeAPI.deleteCalled {
		t.Fatalf("deleteWebhook should not be called on validation failure")
	}
}

func TestGenerateConfigAndSwitchUsesBackendOverride(t *testing.T) {
	t.Parallel()

	outputPath := filepath.Join(t.TempDir(), "config.json")
	fakeAPI := &fakeTelegramAPI{
		webhookInfo:            telegram.WebhookInfo{},
		webhookInfoAfterDelete: telegram.WebhookInfo{},
		botUser: telegram.BotUser{
			ID:        2,
			FirstName: "Legacy Bot",
		},
	}

	result, err := GenerateConfigAndSwitch(context.Background(), fakeAPI, GenerateRequest{
		Token:              "123:ABC",
		OutputPath:         outputPath,
		BackendURLOverride: "https://backend.example/webhook",
		InlineToken:        true,
	})
	if err != nil {
		t.Fatalf("GenerateConfigAndSwitch returned error: %v", err)
	}

	if result.BackendURL != "https://backend.example/webhook" {
		t.Fatalf("expected backend override to be used, got %q", result.BackendURL)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	var generated generatedConfig
	if err := json.Unmarshal(content, &generated); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}

	if generated.Bots[0].Token != "123:ABC" {
		t.Fatalf("expected inline token to be written")
	}
}

func TestGenerateConfigAndSwitchFailsWhenOutputExistsWithoutForce(t *testing.T) {
	t.Parallel()

	outputPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(outputPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("prepare existing file: %v", err)
	}

	fakeAPI := &fakeTelegramAPI{
		webhookInfo: telegram.WebhookInfo{
			URL: "https://legacy.example/webhook",
		},
		webhookInfoAfterDelete: telegram.WebhookInfo{},
		botUser:                telegram.BotUser{ID: 1, Username: "bot"},
	}

	_, err := GenerateConfigAndSwitch(context.Background(), fakeAPI, GenerateRequest{
		Token:      "123:ABC",
		OutputPath: outputPath,
	})
	if err == nil {
		t.Fatalf("expected output exists error")
	}

	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unexpected error: %v", err)
	}

	if fakeAPI.deleteCalled {
		t.Fatalf("deleteWebhook should not be called when output file exists")
	}
}

type fakeTelegramAPI struct {
	webhookInfo            telegram.WebhookInfo
	webhookInfoAfterDelete telegram.WebhookInfo
	botUser                telegram.BotUser

	getWebhookErr error
	deleteErr     error
	getMeErr      error

	getWebhookCalls   int
	deleteCalled      bool
	deleteDropPending bool
}

func (fake *fakeTelegramAPI) GetWebhookInfo(context.Context, string) (telegram.WebhookInfo, error) {
	fake.getWebhookCalls++
	if fake.getWebhookErr != nil {
		return telegram.WebhookInfo{}, fake.getWebhookErr
	}

	if fake.getWebhookCalls == 1 {
		return fake.webhookInfo, nil
	}

	return fake.webhookInfoAfterDelete, nil
}

func (fake *fakeTelegramAPI) DeleteWebhook(_ context.Context, _ string, dropPending bool) error {
	if fake.deleteErr != nil {
		return fake.deleteErr
	}

	fake.deleteCalled = true
	fake.deleteDropPending = dropPending
	return nil
}

func (fake *fakeTelegramAPI) GetMe(context.Context, string) (telegram.BotUser, error) {
	if fake.getMeErr != nil {
		return telegram.BotUser{}, fake.getMeErr
	}

	return fake.botUser, nil
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

func TestResolveTokenValueRejectsInvalidEnvName(t *testing.T) {
	t.Parallel()

	_, _, err := resolveTokenValue(GenerateRequest{TokenEnvName: "1TOKEN"}, "bot", "abc")
	if err == nil {
		t.Fatalf("expected invalid env name error")
	}

	if !strings.Contains(err.Error(), "invalid token environment variable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
