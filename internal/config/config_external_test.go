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
	if cfg.Bot.DispatcherMaxRoutines != 16 || cfg.Bot.AdminCacheTTL != "30s" || cfg.Bot.AdminNegativeCacheTTL != "10s" || cfg.Bot.PendingSweeperInterval != "1s" {
		t.Fatalf("bot defaults = %+v, want dispatcher/admin cache/sweeper defaults", cfg.Bot)
	}
	if cfg.Store.Type != "sqlite" || cfg.Store.DataPath != "./data" {
		t.Fatalf("store defaults = %+v, want sqlite + default data path", cfg.Store)
	}
	if got, want := cfg.Store.SQLitePath(), filepath.Join(".", "data", "fuckad.db"); got != want {
		t.Fatalf("Store.SQLitePath() = %q, want %q", got, want)
	}
	if got, want := cfg.Store.DualWriteQueuePath(), filepath.Join(".", "data", "redis-sync-queue.db"); got != want {
		t.Fatalf("Store.DualWriteQueuePath() = %q, want %q", got, want)
	}
}

func TestBotGetterFallbacks(t *testing.T) {
	t.Parallel()

	botCfg := configpkg.BotConfig{}
	if got, want := botCfg.GetDispatcherMaxRoutines(), 16; got != want {
		t.Fatalf("GetDispatcherMaxRoutines() = %d, want %d", got, want)
	}
	if got, want := botCfg.GetAdminCacheTTL(), 30*time.Second; got != want {
		t.Fatalf("GetAdminCacheTTL() = %v, want %v", got, want)
	}
	if got, want := botCfg.GetAdminNegativeCacheTTL(), 10*time.Second; got != want {
		t.Fatalf("GetAdminNegativeCacheTTL() = %v, want %v", got, want)
	}
	if got, want := botCfg.GetPendingSweeperInterval(), time.Second; got != want {
		t.Fatalf("GetPendingSweeperInterval() = %v, want %v", got, want)
	}
}

func TestLoadRejectsRemovedTurnstileHardeningKeys(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"

[turnstile]
read_header_timeout = "bad"
max_header_bytes = -1
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want unsupported hardening keys error")
	}
	if !strings.Contains(err.Error(), "turnstile.read_header_timeout") || !strings.Contains(err.Error(), "turnstile.max_header_bytes") {
		t.Fatalf("Load() error = %q, want removed key names", err)
	}
}

func TestLoadRejectsRedisDualWriteCombination(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"

[store]
type = "redis"
dual_write_enabled = true
redis_addr = "127.0.0.1:6379"
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid dual-write combination")
	}
	if !strings.Contains(err.Error(), "dual_write_enabled") {
		t.Fatalf("Load() error = %q, want dual_write_enabled validation message", err)
	}
}

func TestLoadRejectsRedisWithoutAddress(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"

[store]
type = "redis"
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := configpkg.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want missing redis_addr validation error")
	}
	if !strings.Contains(err.Error(), "store.redis_addr") {
		t.Fatalf("Load() error = %q, want redis_addr validation message", err)
	}
}

func TestLoadAcceptsDualWriteSQLiteMode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"

[store]
type = "sqlite"
data_path = "./runtime"
dual_write_enabled = true
redis_addr = "127.0.0.1:6379"
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := configpkg.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.Store.DualWriteEnabled {
		t.Fatal("DualWriteEnabled = false, want true")
	}
	if got, want := cfg.Store.SQLitePath(), filepath.Join(".", "runtime", "fuckad.db"); got != want {
		t.Fatalf("Store.SQLitePath() = %q, want %q", got, want)
	}
	if got, want := cfg.Store.DualWriteQueuePath(), filepath.Join(".", "runtime", "redis-sync-queue.db"); got != want {
		t.Fatalf("Store.DualWriteQueuePath() = %q, want %q", got, want)
	}
}

func TestLoadAcceptsLegacyDualWriteTuningKeysForCompatibility(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	content := strings.TrimSpace(`
[bot]
token = "token"

[store]
type = "sqlite"
dual_write_enabled = true
redis_addr = "127.0.0.1:6379"
dual_write_flush_interval = "5s"
dual_write_batch_size = 50
`)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := configpkg.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.Store.HasLegacyDualWriteTuning() {
		t.Fatal("HasLegacyDualWriteTuning() = false, want true")
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
