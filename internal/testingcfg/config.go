package testingcfg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/redis/go-redis/v9"
)

const (
	DefaultConfigFileName = "testing.toml"
	ConfigPathEnvVar      = "FUCKAD_TESTING_CONFIG"
	defaultRedisKeyPrefix = "fuckad:testing:"
	redisScanCount        = 256
)

type Config struct {
	Redis RedisConfig `toml:"redis"`
}

type RedisConfig struct {
	Enabled   bool   `toml:"enabled"`
	Addr      string `toml:"addr"`
	Password  string `toml:"password"`
	DB        int    `toml:"db"`
	KeyPrefix string `toml:"key_prefix"`
	Cleanup   bool   `toml:"cleanup"`
}

func Load() (Config, string, error) {
	path, found, err := discoverConfigPath()
	if err != nil || !found {
		return Config{}, path, err
	}

	cfg, err := LoadFile(path)
	if err != nil {
		return Config{}, path, err
	}

	return cfg, path, nil
}

func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read testing config: %w", err)
	}

	var cfg Config
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("parse testing config: %w", err)
	}

	if err := validateUndecodedKeys(meta.Undecoded()); err != nil {
		return Config{}, err
	}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c *Config) Normalize() {
	c.Redis.Normalize()
}

func (c Config) Validate() error {
	return c.Redis.Validate()
}

func (c *RedisConfig) Normalize() {
	c.Addr = strings.TrimSpace(c.Addr)
	c.Password = strings.TrimSpace(c.Password)
	c.KeyPrefix = strings.TrimSpace(c.KeyPrefix)
	if c.KeyPrefix == "" {
		c.KeyPrefix = defaultRedisKeyPrefix
	}
}

func (c RedisConfig) Validate() error {
	if c.DB < 0 {
		return fmt.Errorf("redis.db must be >= 0")
	}
	if c.Enabled && c.Addr == "" {
		return fmt.Errorf("redis.addr is required when redis.enabled = true")
	}
	return nil
}

func (c RedisConfig) UseRealRedis() bool {
	return c.Enabled && c.Addr != ""
}

func (c RedisConfig) ScopedKeyPrefix(parts ...string) string {
	base := ensureTrailingColon(c.KeyPrefix)
	var builder strings.Builder
	builder.Grow(len(base) + len(parts)*16)
	builder.WriteString(base)

	for _, part := range parts {
		sanitized := sanitizePrefixPart(part)
		if sanitized == "" {
			continue
		}
		builder.WriteString(sanitized)
		builder.WriteByte(':')
	}

	return builder.String()
}

func CleanupRedisPrefix(ctx context.Context, cfg RedisConfig, prefix string) error {
	if !cfg.Cleanup {
		return nil
	}

	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	defer func() { _ = client.Close() }()

	var cursor uint64
	pattern := prefix + "*"
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, redisScanCount).Result()
		if err != nil {
			return fmt.Errorf("scan redis prefix %q: %w", prefix, err)
		}

		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("delete redis prefix %q: %w", prefix, err)
			}
		}

		if next == 0 {
			return nil
		}
		cursor = next
	}
}

func RealRedisCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func discoverConfigPath() (string, bool, error) {
	if envPath := strings.TrimSpace(os.Getenv(ConfigPathEnvVar)); envPath != "" {
		path, err := filepath.Abs(envPath)
		if err != nil {
			return "", false, fmt.Errorf("resolve %s: %w", ConfigPathEnvVar, err)
		}
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", false, fmt.Errorf("%s points to a missing file: %s", ConfigPathEnvVar, path)
			}
			return "", false, fmt.Errorf("stat %s: %w", path, err)
		}
		return path, true, nil
	}

	root, found, err := findModuleRoot()
	if err != nil || !found {
		return "", false, err
	}

	path := filepath.Join(root, DefaultConfigFileName)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat testing config: %w", err)
	}

	return path, true, nil
}

func findModuleRoot() (string, bool, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false, fmt.Errorf("get working directory: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("stat go.mod in %s: %w", dir, err)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

func validateUndecodedKeys(keys []toml.Key) error {
	if len(keys) == 0 {
		return nil
	}

	unsupported := make([]string, 0, len(keys))
	for _, key := range keys {
		unsupported = append(unsupported, key.String())
	}

	return fmt.Errorf("unsupported testing config keys: %s", strings.Join(unsupported, ", "))
}

func ensureTrailingColon(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return defaultRedisKeyPrefix
	}
	if strings.HasSuffix(prefix, ":") {
		return prefix
	}
	return prefix + ":"
}

func sanitizePrefixPart(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}

	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			return r
		case r == '-', r == '_', r == ':':
			return r
		default:
			return '_'
		}
	}, part)
}
