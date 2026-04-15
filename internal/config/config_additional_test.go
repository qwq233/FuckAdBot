package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfiguredGettersReturnExplicitValues(t *testing.T) {
	t.Parallel()

	turnstileCfg := TurnstileConfig{VerifyTimeout: "45s"}
	if got, want := turnstileCfg.GetVerifyTimeout(), 45*time.Second; got != want {
		t.Fatalf("GetVerifyTimeout() = %v, want %v", got, want)
	}

	moderationCfg := ModerationConfig{
		ReminderTTL:  600,
		VerifyWindow: "5m",
	}
	if got, want := moderationCfg.GetReminderTTL(), 10*time.Minute; got != want {
		t.Fatalf("GetReminderTTL() = %v, want %v", got, want)
	}

	botCfg := BotConfig{
		DispatcherMaxRoutines:  32,
		AdminCacheTTL:          "45s",
		AdminNegativeCacheTTL:  "15s",
		PendingSweeperInterval: "3s",
	}
	if got, want := botCfg.GetDispatcherMaxRoutines(), 32; got != want {
		t.Fatalf("GetDispatcherMaxRoutines() = %d, want %d", got, want)
	}
	if got, want := botCfg.GetAdminCacheTTL(), 45*time.Second; got != want {
		t.Fatalf("GetAdminCacheTTL() = %v, want %v", got, want)
	}
	if got, want := botCfg.GetAdminNegativeCacheTTL(), 15*time.Second; got != want {
		t.Fatalf("GetAdminNegativeCacheTTL() = %v, want %v", got, want)
	}
	if got, want := botCfg.GetPendingSweeperInterval(), 3*time.Second; got != want {
		t.Fatalf("GetPendingSweeperInterval() = %v, want %v", got, want)
	}
}

func TestStoreConfigNormalizeTrimsAndAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg := StoreConfig{
		Type:                            " SQLITE ",
		DataPath:                        " ./data-dir ",
		RedisAddr:                       " 127.0.0.1:6379 ",
		RedisPassword:                   " secret ",
		RedisKeyPrefix:                  " custom: ",
		DualWriteFlushInterval:          " 1s ",
		DualWriteBatchSize:              99,
		DualWriteMaxQueueDepth:          100,
		DualWriteEnabled:                true,
		DualWriteMaxConsecutiveFailures: 5,
	}
	cfg.Normalize()

	if got, want := cfg.Type, "sqlite"; got != want {
		t.Fatalf("Normalize().Type = %q, want %q", got, want)
	}
	if got, want := cfg.DataPath, "./data-dir"; got != want {
		t.Fatalf("Normalize().DataPath = %q, want %q", got, want)
	}
	if got, want := cfg.RedisAddr, "127.0.0.1:6379"; got != want {
		t.Fatalf("Normalize().RedisAddr = %q, want %q", got, want)
	}
	if got, want := cfg.RedisPassword, "secret"; got != want {
		t.Fatalf("Normalize().RedisPassword = %q, want %q", got, want)
	}
	if got, want := cfg.RedisKeyPrefix, "custom:"; got != want {
		t.Fatalf("Normalize().RedisKeyPrefix = %q, want %q", got, want)
	}
	if got, want := cfg.DualWriteFlushInterval, "1s"; got != want {
		t.Fatalf("Normalize().DualWriteFlushInterval = %q, want %q", got, want)
	}

	defaulted := StoreConfig{}
	defaulted.Normalize()
	if got, want := defaulted.Type, "sqlite"; got != want {
		t.Fatalf("Normalize().Type default = %q, want %q", got, want)
	}
	if got, want := defaulted.DataPath, DefaultDataPath; got != want {
		t.Fatalf("Normalize().DataPath default = %q, want %q", got, want)
	}
	if got, want := defaulted.RedisKeyPrefix, defaultRedisKeyPrefix; got != want {
		t.Fatalf("Normalize().RedisKeyPrefix default = %q, want %q", got, want)
	}
}

func TestValidateBotConfigRejectsNegativeDispatcherMaxRoutines(t *testing.T) {
	t.Parallel()

	if err := validateBotConfig(BotConfig{DispatcherMaxRoutines: -1}); err == nil {
		t.Fatal("validateBotConfig() error = nil, want negative dispatcher validation")
	}
	if err := validateBotConfig(BotConfig{DispatcherMaxRoutines: 0}); err != nil {
		t.Fatalf("validateBotConfig() error = %v, want nil for zero/default value", err)
	}
}

func TestValidateStoreConfigValidationBranches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cfg     StoreConfig
		wantErr string
	}{
		{
			name:    "unsupported type",
			cfg:     StoreConfig{Type: "memory", DataPath: DefaultDataPath},
			wantErr: "store.type",
		},
		{
			name:    "blank data path",
			cfg:     StoreConfig{Type: "sqlite", DataPath: "   "},
			wantErr: "store.data_path",
		},
		{
			name:    "negative redis db",
			cfg:     StoreConfig{Type: "sqlite", DataPath: DefaultDataPath, RedisDB: -1},
			wantErr: "store.redis_db",
		},
		{
			name:    "dual write with redis backend",
			cfg:     StoreConfig{Type: "redis", DataPath: DefaultDataPath, RedisAddr: "127.0.0.1:6379", DualWriteEnabled: true},
			wantErr: "store.dual_write_enabled",
		},
		{
			name:    "redis backend missing addr",
			cfg:     StoreConfig{Type: "redis", DataPath: DefaultDataPath},
			wantErr: "store.redis_addr",
		},
		{
			name:    "dual write missing addr",
			cfg:     StoreConfig{Type: "sqlite", DataPath: DefaultDataPath, DualWriteEnabled: true},
			wantErr: "store.redis_addr",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateStoreConfig(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateStoreConfig() error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}

	if err := validateStoreConfig(StoreConfig{Type: "redis", DataPath: DefaultDataPath, RedisAddr: "127.0.0.1:6379"}); err != nil {
		t.Fatalf("validateStoreConfig(valid redis) error = %v, want nil", err)
	}
	if err := validateStoreConfig(StoreConfig{Type: "sqlite", DataPath: DefaultDataPath, RedisAddr: "127.0.0.1:6379", DualWriteEnabled: true}); err != nil {
		t.Fatalf("validateStoreConfig(valid dual-write) error = %v, want nil", err)
	}
}

func TestGetterFallbackBranchesReturnDefaults(t *testing.T) {
	t.Parallel()

	if got, want := (&TurnstileConfig{VerifyTimeout: "bad"}).GetVerifyTimeout(), 5*time.Minute; got != want {
		t.Fatalf("GetVerifyTimeout(invalid) = %v, want %v", got, want)
	}
	if got, want := (&ModerationConfig{ReminderTTL: 0, VerifyWindow: "3m"}).GetReminderTTL(), 3*time.Minute; got != want {
		t.Fatalf("GetReminderTTL(zero) = %v, want %v", got, want)
	}
	if got, want := (&ModerationConfig{OriginalMessageTTL: "bad", VerifyWindow: "5m"}).GetOriginalMessageTTL(), time.Minute; got != want {
		t.Fatalf("GetOriginalMessageTTL(invalid) = %v, want %v", got, want)
	}
	if got, want := (&ModerationConfig{OriginalMessageTTL: "10m", VerifyWindow: "3m"}).GetOriginalMessageTTL(), 3*time.Minute; got != want {
		t.Fatalf("GetOriginalMessageTTL(capped) = %v, want %v", got, want)
	}

	defaultBot := &BotConfig{
		DispatcherMaxRoutines:  0,
		AdminCacheTTL:          "bad",
		AdminNegativeCacheTTL:  "0s",
		PendingSweeperInterval: "-1s",
	}
	if got, want := defaultBot.GetDispatcherMaxRoutines(), defaultDispatcherMaxRoutines; got != want {
		t.Fatalf("GetDispatcherMaxRoutines(default) = %d, want %d", got, want)
	}
	if got, want := defaultBot.GetAdminCacheTTL(), defaultAdminCacheTTL; got != want {
		t.Fatalf("GetAdminCacheTTL(default) = %v, want %v", got, want)
	}
	if got, want := defaultBot.GetAdminNegativeCacheTTL(), defaultAdminNegativeCacheTTL; got != want {
		t.Fatalf("GetAdminNegativeCacheTTL(default) = %v, want %v", got, want)
	}
	if got, want := defaultBot.GetPendingSweeperInterval(), defaultPendingSweeperInterval; got != want {
		t.Fatalf("GetPendingSweeperInterval(default) = %v, want %v", got, want)
	}
}

func TestLoadAndTurnstileValidationBranches(t *testing.T) {
	t.Parallel()

	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil || !strings.Contains(err.Error(), "read config file") {
		t.Fatalf("Load(missing) error = %v, want read config failure", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[bot]\nadmins = [7]\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "bot.token is required") {
		t.Fatalf("Load(missing token) error = %v, want bot.token validation", err)
	}

	base := TurnstileConfig{
		Enabled:    true,
		SiteKey:    "site",
		SecretKey:  "secret",
		Domain:     "verify.example.com",
		ListenAddr: "127.0.0.1",
		ListenPort: 8080,
	}

	withScheme := base
	withScheme.Domain = "https://verify.example.com"
	if err := validateTurnstileConfig(withScheme); err == nil || !strings.Contains(err.Error(), "dedicated hostname") {
		t.Fatalf("validateTurnstileConfig(with scheme) error = %v, want dedicated-hostname validation", err)
	}

	withPort := base
	withPort.Domain = "verify.example.com:8443"
	if err := validateTurnstileConfig(withPort); err == nil || !strings.Contains(err.Error(), "must not include a port") {
		t.Fatalf("validateTurnstileConfig(with port) error = %v, want port validation", err)
	}
}
