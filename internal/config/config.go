package config

import (
	"fmt"
	"net/url"
	"os"
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
	Token  string  `toml:"token"`
	Admins []int64 `toml:"admins"`
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
	MaxWarnings  int    `toml:"max_warnings"`
	ReminderTTL  int    `toml:"reminder_ttl"`
	VerifyWindow string `toml:"verify_window"`
}

type StoreConfig struct {
	Type          string `toml:"type"`
	SQLitePath    string `toml:"sqlite_path"`
	RedisAddr     string `toml:"redis_addr"`
	RedisPassword string `toml:"redis_password"`
	RedisDB       int    `toml:"redis_db"`
}

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

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{
		Turnstile: TurnstileConfig{
			ListenAddr:    "127.0.0.1",
			ListenPort:    8080,
			VerifyTimeout: "5m",
		},
		Moderation: ModerationConfig{
			MaxWarnings:  3,
			ReminderTTL:  30,
			VerifyWindow: "5m",
		},
		Store: StoreConfig{
			Type:       "sqlite",
			SQLitePath: "./data/fuckad.db",
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

	if cfg.Bot.Token == "" {
		return nil, fmt.Errorf("bot.token is required")
	}

	if err := validateTurnstileConfig(cfg.Turnstile); err != nil {
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

func validateTurnstileConfig(cfg TurnstileConfig) error {
	if !cfg.Enabled {
		return nil
	}

	if cfg.Domain == "" {
		return fmt.Errorf("turnstile.domain is required")
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
