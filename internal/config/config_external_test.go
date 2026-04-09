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

func TestGetVerifyTimeoutFallsBackToDefault(t *testing.T) {
	t.Parallel()

	cfg := configpkg.TurnstileConfig{VerifyTimeout: "not-a-duration"}

	if got, want := cfg.GetVerifyTimeout(), 5*time.Minute; got != want {
		t.Fatalf("GetVerifyTimeout() = %v, want %v", got, want)
	}
}

func TestGetVerifyWindowFallsBackToDefault(t *testing.T) {
	t.Parallel()

	cfg := configpkg.ModerationConfig{VerifyWindow: "not-a-duration"}

	if got, want := cfg.GetVerifyWindow(), 5*time.Minute; got != want {
		t.Fatalf("GetVerifyWindow() = %v, want %v", got, want)
	}
}

func TestPublicOriginUsesHTTPS(t *testing.T) {
	t.Parallel()

	cfg := configpkg.TurnstileConfig{Domain: "verify.example.com"}

	if got, want := cfg.PublicOrigin(), "https://verify.example.com"; got != want {
		t.Fatalf("PublicOrigin() = %q, want %q", got, want)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := configpkg.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Turnstile.ListenAddr != "127.0.0.1" || cfg.Turnstile.ListenPort != 8080 || cfg.Turnstile.VerifyTimeout != "5m" {
		t.Fatalf("turnstile defaults = %+v, want listen_addr=127.0.0.1 listen_port=8080 verify_timeout=5m", cfg.Turnstile)
	}
	if cfg.Moderation.MaxWarnings != 3 || cfg.Moderation.ReminderTTL != 30 || cfg.Moderation.VerifyWindow != "5m" || cfg.Moderation.OriginalMessageTTL != "1m" {
		t.Fatalf("moderation defaults = %+v, want max_warnings=3 reminder_ttl=30 verify_window=5m original_message_ttl=1m", cfg.Moderation)
	}
	if cfg.Store.Type != "sqlite" || cfg.Store.SQLitePath != "./data/fuckad.db" {
		t.Fatalf("store defaults = %+v, want sqlite + default path", cfg.Store)
	}
}

func TestLoadRejectsMissingBotToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
admins = [1, 2]
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want missing bot token error")
	}
	if !strings.Contains(err.Error(), "bot.token is required") {
		t.Fatalf("Load() error = %q, want missing bot token message", err)
	}
}

func TestLoadRejectsTurnstilePortOutOfRange(t *testing.T) {
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
listen_port = 70000
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid port error")
	}
	if !strings.Contains(err.Error(), "listen_port") {
		t.Fatalf("Load() error = %q, want invalid port message", err)
	}
}

func TestLoadRejectsTurnstileDomainWithScheme(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"

[turnstile]
enabled = true
site_key = "site"
secret_key = "secret"
domain = "https://verify.example.com"
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want dedicated hostname validation error")
	}
	if !strings.Contains(err.Error(), "dedicated hostname") {
		t.Fatalf("Load() error = %q, want dedicated hostname message", err)
	}
}

func TestLoadRejectsMultipleUnsupportedKeys(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"
extra = true

[turnstile]
callback_url = "https://verify.example.com/custom"
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want unsupported key error")
	}
	if !strings.Contains(err.Error(), "bot.extra") || !strings.Contains(err.Error(), "turnstile.callback_url") {
		t.Fatalf("Load() error = %q, want both unsupported keys", err)
	}
}

func TestLoadRejectsMissingTurnstileRequiredFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		extra   string
		wantKey string
	}{
		{name: "missing site key", extra: `secret_key = "secret"
domain = "verify.example.com"`, wantKey: "site_key"},
		{name: "missing secret key", extra: `site_key = "site"
domain = "verify.example.com"`, wantKey: "secret_key"},
		{name: "missing domain", extra: `site_key = "site"
secret_key = "secret"`, wantKey: "domain"},
		{name: "missing listen addr", extra: `site_key = "site"
secret_key = "secret"
domain = "verify.example.com"
listen_addr = "   "`, wantKey: "listen_addr"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "config.toml")
			content := strings.TrimSpace(`
[bot]
token = "token"

[turnstile]
enabled = true
` + tc.extra)

			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			_, err := configpkg.Load(path)
			if err == nil {
				t.Fatal("Load() error = nil, want turnstile validation error")
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Fatalf("Load() error = %q, want mention of %s", err, tc.wantKey)
			}
		})
	}
}

func TestLoadRejectsTurnstileDomainWithPortQueryFragmentOrUserInfo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		domain string
	}{
		{name: "port", domain: "verify.example.com:8443"},
		{name: "query", domain: "verify.example.com?foo=bar"},
		{name: "fragment", domain: "verify.example.com#frag"},
		{name: "user info", domain: "user@verify.example.com"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "config.toml")
			content := strings.TrimSpace(`
[bot]
token = "token"

[turnstile]
enabled = true
site_key = "site"
secret_key = "secret"
domain = "` + tc.domain + `"
`)

			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			_, err := configpkg.Load(path)
			if err == nil {
				t.Fatalf("Load() error = nil, want dedicated hostname validation error for %q", tc.domain)
			}
			if !strings.Contains(err.Error(), "hostname") && !strings.Contains(err.Error(), "port") {
				t.Fatalf("Load() error = %q, want domain validation message", err)
			}
		})
	}
}
