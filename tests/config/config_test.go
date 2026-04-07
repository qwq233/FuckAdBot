package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	configpkg "github.com/qwq233/fuckadbot/internal/config"
)

func TestLoadRejectsUnsupportedCallbackURL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"

[turnstile]
enabled = true
site_key = "site"
secret_key = "secret"
domain = "verify.example.com"
callback_url = "https://verify.example.com/custom"
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want unsupported key error")
	}

	if !strings.Contains(err.Error(), "turnstile.callback_url") {
		t.Fatalf("Load() error = %q, want mention of turnstile.callback_url", err)
	}
}

func TestLoadRejectsTurnstileDomainWithPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"

[turnstile]
enabled = true
site_key = "site"
secret_key = "secret"
domain = "verify.example.com/verify"
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want domain validation error")
	}

	if !strings.Contains(err.Error(), "dedicated hostname") && !strings.Contains(err.Error(), "path") {
		t.Fatalf("Load() error = %q, want domain validation message", err)
	}
}

func TestTurnstileURLsUseFixedPaths(t *testing.T) {
	t.Parallel()

	cfg := configpkg.TurnstileConfig{Domain: "verify.example.com"}

	if got, want := cfg.VerifyURL(), "https://verify.example.com/verify"; got != want {
		t.Fatalf("VerifyURL() = %q, want %q", got, want)
	}

	if got, want := cfg.CallbackURL(), "https://verify.example.com/verify/callback"; got != want {
		t.Fatalf("CallbackURL() = %q, want %q", got, want)
	}
}

func TestReminderTTLUsesAtLeastVerifyWindow(t *testing.T) {
	t.Parallel()

	cfg := configpkg.ModerationConfig{
		ReminderTTL:  30,
		VerifyWindow: "5m",
	}

	if got, want := cfg.GetReminderTTL(), 5*time.Minute; got != want {
		t.Fatalf("GetReminderTTL() = %v, want %v", got, want)
	}
}

func TestOriginalMessageTTLDefaultsToOneMinute(t *testing.T) {
	t.Parallel()

	cfg := configpkg.ModerationConfig{
		OriginalMessageTTL: "invalid",
		VerifyWindow:       "5m",
	}

	if got, want := cfg.GetOriginalMessageTTL(), time.Minute; got != want {
		t.Fatalf("GetOriginalMessageTTL() = %v, want %v", got, want)
	}
}

func TestOriginalMessageTTLCapsToVerifyWindow(t *testing.T) {
	t.Parallel()

	cfg := configpkg.ModerationConfig{
		OriginalMessageTTL: "10m",
		VerifyWindow:       "5m",
	}

	if got, want := cfg.GetOriginalMessageTTL(), 5*time.Minute; got != want {
		t.Fatalf("GetOriginalMessageTTL() = %v, want %v", got, want)
	}
}
