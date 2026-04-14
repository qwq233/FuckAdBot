package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	VerifyPath   = "/verify"
	CallbackPath = "/verify/callback"
)

type Config struct {
	Bot        BotConfig        `toml:"bot"`
	Turnstile  TurnstileConfig  `toml:"turnstile"`
	Blacklist  BlacklistConfig  `toml:"blacklist"`
	Moderation ModerationConfig `toml:"moderation"`
	Store      StoreConfig      `toml:"store"`
}

type BotConfig struct {
	Token                  string  `toml:"token"`
	Admins                 []int64 `toml:"admins"`
	DispatcherMaxRoutines  int     `toml:"dispatcher_max_routines"`
	AdminCacheTTL          string  `toml:"admin_cache_ttl"`
	AdminNegativeCacheTTL  string  `toml:"admin_negative_cache_ttl"`
	PendingSweeperInterval string  `toml:"pending_sweeper_interval"`
}

type TurnstileConfig struct {
	Enabled    bool   `toml:"enabled"`
	SiteKey    string `toml:"site_key"`
	SecretKey  string `toml:"secret_key"`
	Domain     string `toml:"domain"`
	ListenAddr string `toml:"listen_addr"`
	ListenPort int    `toml:"listen_port"`
	// VerifyTimeout is the duration string for verification window per reminder.
	VerifyTimeout string `toml:"verify_timeout"`
}

type BlacklistConfig struct {
	Words []string `toml:"words"`
}

type ModerationConfig struct {
	MaxWarnings        int    `toml:"max_warnings"`
	ReminderTTL        int    `toml:"reminder_ttl"`
	VerifyWindow       string `toml:"verify_window"`
	OriginalMessageTTL string `toml:"original_message_ttl"`
}

type StoreConfig struct {
	Type             string `toml:"type"`
	DataPath         string `toml:"data_path"`
	RedisAddr        string `toml:"redis_addr"`
	RedisPassword    string `toml:"redis_password"`
	RedisDB          int    `toml:"redis_db"`
	RedisKeyPrefix   string `toml:"redis_key_prefix"`
	DualWriteEnabled bool   `toml:"dual_write_enabled"`
	// Legacy compatibility fields. Runtime tuning is now fixed in code.
	DualWriteFlushInterval          string `toml:"dual_write_flush_interval"`
	DualWriteBatchSize              int    `toml:"dual_write_batch_size"`
	DualWriteMaxConsecutiveFailures int    `toml:"dual_write_max_consecutive_failures"`
	DualWriteMaxQueueDepth          int    `toml:"dual_write_max_queue_depth"`
}

const (
	DefaultDataPath                   = "./data"
	DefaultSQLiteDatabaseName         = "fuckad.db"
	DefaultDualWriteQueueDatabaseName = "redis-sync-queue.db"
	defaultRedisKeyPrefix             = "fuckad:"
	defaultDispatcherMaxRoutines      = 16
	defaultAdminCacheTTL              = 30 * time.Second
	defaultAdminNegativeCacheTTL      = 10 * time.Second
	defaultPendingSweeperInterval     = time.Second
)

func (c *TurnstileConfig) GetVerifyTimeout() time.Duration {
	d, err := time.ParseDuration(c.VerifyTimeout)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

func (c *TurnstileConfig) PublicOrigin() string {
	return "https://" + c.Domain
}

func (c *TurnstileConfig) VerifyURL() string {
	return c.PublicOrigin() + VerifyPath
}

func (c *TurnstileConfig) CallbackURL() string {
	return c.PublicOrigin() + CallbackPath
}

func (c *ModerationConfig) GetVerifyWindow() time.Duration {
	d, err := time.ParseDuration(c.VerifyWindow)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

func (c *ModerationConfig) GetReminderTTL() time.Duration {
	ttl := time.Duration(c.ReminderTTL) * time.Second
	if ttl <= 0 {
		return c.GetVerifyWindow()
	}

	verifyWindow := c.GetVerifyWindow()
	if ttl < verifyWindow {
		return verifyWindow
	}

	return ttl
}

func (c *ModerationConfig) GetOriginalMessageTTL() time.Duration {
	ttl, err := time.ParseDuration(c.OriginalMessageTTL)
	if err != nil || ttl <= 0 {
		ttl = time.Minute
	}

	verifyWindow := c.GetVerifyWindow()
	if ttl > verifyWindow {
		return verifyWindow
	}

	return ttl
}

func (c *BotConfig) GetDispatcherMaxRoutines() int {
	if c.DispatcherMaxRoutines <= 0 {
		return defaultDispatcherMaxRoutines
	}
	return c.DispatcherMaxRoutines
}

func (c *BotConfig) GetAdminCacheTTL() time.Duration {
	d, err := time.ParseDuration(c.AdminCacheTTL)
	if err != nil || d <= 0 {
		return defaultAdminCacheTTL
	}
	return d
}

func (c *BotConfig) GetAdminNegativeCacheTTL() time.Duration {
	d, err := time.ParseDuration(c.AdminNegativeCacheTTL)
	if err != nil || d <= 0 {
		return defaultAdminNegativeCacheTTL
	}
	return d
}

func (c *BotConfig) GetPendingSweeperInterval() time.Duration {
	d, err := time.ParseDuration(c.PendingSweeperInterval)
	if err != nil || d <= 0 {
		return defaultPendingSweeperInterval
	}
	return d
}

func (c *StoreConfig) Normalize() {
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	c.DataPath = strings.TrimSpace(c.DataPath)
	c.RedisAddr = strings.TrimSpace(c.RedisAddr)
	c.RedisPassword = strings.TrimSpace(c.RedisPassword)
	c.RedisKeyPrefix = strings.TrimSpace(c.RedisKeyPrefix)
	c.DualWriteFlushInterval = strings.TrimSpace(c.DualWriteFlushInterval)

	if c.Type == "" {
		c.Type = "sqlite"
	}
	if c.DataPath == "" {
		c.DataPath = DefaultDataPath
	}
	if c.RedisKeyPrefix == "" {
		c.RedisKeyPrefix = defaultRedisKeyPrefix
	}
}

func (c StoreConfig) SQLitePath() string {
	return filepath.Join(c.DataPath, DefaultSQLiteDatabaseName)
}

func (c StoreConfig) DualWriteQueuePath() string {
	return filepath.Join(c.DataPath, DefaultDualWriteQueueDatabaseName)
}

func (c StoreConfig) HasLegacyDualWriteTuning() bool {
	return strings.TrimSpace(c.DualWriteFlushInterval) != "" ||
		c.DualWriteBatchSize != 0 ||
		c.DualWriteMaxConsecutiveFailures != 0 ||
		c.DualWriteMaxQueueDepth != 0
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{
		Bot: BotConfig{
			DispatcherMaxRoutines:  defaultDispatcherMaxRoutines,
			AdminCacheTTL:          defaultAdminCacheTTL.String(),
			AdminNegativeCacheTTL:  defaultAdminNegativeCacheTTL.String(),
			PendingSweeperInterval: defaultPendingSweeperInterval.String(),
		},
		Turnstile: TurnstileConfig{
			ListenAddr:    "127.0.0.1",
			ListenPort:    8080,
			VerifyTimeout: "5m",
		},
		Moderation: ModerationConfig{
			MaxWarnings:        3,
			ReminderTTL:        30,
			VerifyWindow:       "5m",
			OriginalMessageTTL: "1m",
		},
		Store: StoreConfig{
			Type:           "sqlite",
			DataPath:       DefaultDataPath,
			RedisKeyPrefix: defaultRedisKeyPrefix,
		},
	}

	meta, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := validateUndecodedKeys(meta.Undecoded()); err != nil {
		return nil, err
	}

	cfg.Turnstile.Domain = strings.ToLower(strings.TrimSpace(cfg.Turnstile.Domain))
	cfg.Store.Normalize()

	if cfg.Bot.Token == "" {
		return nil, fmt.Errorf("bot.token is required")
	}

	if err := validateBotConfig(cfg.Bot); err != nil {
		return nil, err
	}
	if err := validateTurnstileConfig(cfg.Turnstile); err != nil {
		return nil, err
	}
	if err := validateStoreConfig(cfg.Store); err != nil {
		return nil, err
	}

	return cfg, nil
}

func validateUndecodedKeys(keys []toml.Key) error {
	if len(keys) == 0 {
		return nil
	}

	unsupported := make([]string, 0, len(keys))
	for _, key := range keys {
		unsupported = append(unsupported, key.String())
	}

	return fmt.Errorf("unsupported config keys: %s", strings.Join(unsupported, ", "))
}

func validateBotConfig(cfg BotConfig) error {
	if cfg.DispatcherMaxRoutines < 0 {
		return fmt.Errorf("bot.dispatcher_max_routines must be >= 0")
	}

	return nil
}

func validateTurnstileConfig(cfg TurnstileConfig) error {
	if !cfg.Enabled {
		return nil
	}

	if strings.TrimSpace(cfg.SiteKey) == "" {
		return fmt.Errorf("turnstile.site_key is required")
	}

	if strings.TrimSpace(cfg.SecretKey) == "" {
		return fmt.Errorf("turnstile.secret_key is required")
	}

	if cfg.Domain == "" {
		return fmt.Errorf("turnstile.domain is required")
	}

	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return fmt.Errorf("turnstile.listen_addr is required")
	}

	if cfg.ListenPort <= 0 || cfg.ListenPort > 65535 {
		return fmt.Errorf("turnstile.listen_port must be between 1 and 65535")
	}

	if strings.Contains(cfg.Domain, "://") {
		return fmt.Errorf("turnstile.domain must be a dedicated hostname only; callback path is fixed to %s", CallbackPath)
	}

	parsed, err := url.Parse("https://" + cfg.Domain)
	if err != nil {
		return fmt.Errorf("invalid turnstile.domain: %w", err)
	}

	if parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("turnstile.domain must contain only a dedicated hostname without path, query, fragment, or user info")
	}

	if parsed.Port() != "" {
		return fmt.Errorf("turnstile.domain must not include a port")
	}

	if parsed.Hostname() == "" {
		return fmt.Errorf("turnstile.domain must contain a valid hostname")
	}

	return nil
}

func validateStoreConfig(cfg StoreConfig) error {
	switch cfg.Type {
	case "sqlite", "redis":
	default:
		return fmt.Errorf("store.type must be either sqlite or redis")
	}

	if strings.TrimSpace(cfg.DataPath) == "" {
		return fmt.Errorf("store.data_path is required")
	}

	if cfg.RedisDB < 0 {
		return fmt.Errorf("store.redis_db must be >= 0")
	}

	if cfg.DualWriteEnabled && cfg.Type != "sqlite" {
		return fmt.Errorf("store.dual_write_enabled requires store.type = sqlite")
	}

	if cfg.Type == "redis" || cfg.DualWriteEnabled {
		if strings.TrimSpace(cfg.RedisAddr) == "" {
			return fmt.Errorf("store.redis_addr is required when redis is enabled")
		}
	}

	return nil
}
