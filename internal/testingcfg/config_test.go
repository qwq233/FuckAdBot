package testingcfg

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLoadReturnsNoConfigWhenFileMissing(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/test\n\ngo 1.26\n")
	t.Chdir(root)

	cfg, path, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if path != "" {
		t.Fatalf("Load() path = %q, want empty", path)
	}
	if cfg.Redis.UseRealRedis() {
		t.Fatal("Load() unexpectedly enabled real redis")
	}
}

func TestLoadParsesTestingConfigFromModuleRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/test\n\ngo 1.26\n")
	writeFile(t, filepath.Join(root, DefaultConfigFileName), `
[redis]
enabled = true
addr = "127.0.0.1:6379"
password = "secret"
db = 3
key_prefix = "bench:test"
cleanup = true
`)

	child := filepath.Join(root, "internal", "store")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Chdir(child)

	cfg, path, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got, want := path, filepath.Join(root, DefaultConfigFileName); got != want {
		t.Fatalf("Load() path = %q, want %q", got, want)
	}
	if !cfg.Redis.UseRealRedis() {
		t.Fatal("Load() did not enable real redis")
	}
	if got, want := cfg.Redis.KeyPrefix, "bench:test"; got != want {
		t.Fatalf("Redis.KeyPrefix = %q, want %q", got, want)
	}
}

func TestLoadRejectsEnabledRedisWithoutAddress(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/test\n\ngo 1.26\n")
	writeFile(t, filepath.Join(root, DefaultConfigFileName), `
[redis]
enabled = true
`)
	t.Chdir(root)

	_, _, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
}

func TestCleanupRedisPrefixRemovesOnlyMatchingKeys(t *testing.T) {
	t.Parallel()

	redisSrv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisSrv.Addr()})
	defer func() { _ = client.Close() }()

	if err := client.Set(context.Background(), "bench:test:keep", "1", 0).Err(); err != nil {
		t.Fatalf("Set(keep) error = %v", err)
	}
	if err := client.Set(context.Background(), "bench:test:delete", "1", 0).Err(); err != nil {
		t.Fatalf("Set(delete) error = %v", err)
	}
	if err := client.Set(context.Background(), "other:test:keep", "1", 0).Err(); err != nil {
		t.Fatalf("Set(other) error = %v", err)
	}

	cfg := RedisConfig{
		Enabled: true,
		Addr:    redisSrv.Addr(),
		Cleanup: true,
	}

	if err := CleanupRedisPrefix(context.Background(), cfg, "bench:test:"); err != nil {
		t.Fatalf("CleanupRedisPrefix() error = %v", err)
	}

	if got := redisSrv.Exists("bench:test:keep"); got {
		t.Fatal("CleanupRedisPrefix() did not remove first matching key")
	}
	if got := redisSrv.Exists("bench:test:delete"); got {
		t.Fatal("CleanupRedisPrefix() did not remove second matching key")
	}
	if got := redisSrv.Exists("other:test:keep"); !got {
		t.Fatal("CleanupRedisPrefix() removed non-matching key")
	}
}

func TestLoadUsesEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-testing.toml")
	writeFile(t, path, `
[redis]
enabled = true
addr = "127.0.0.1:6379"
`)
	t.Setenv(ConfigPathEnvVar, path)

	cfg, loadedPath, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := loadedPath, path; got != want {
		t.Fatalf("Load() path = %q, want %q", got, want)
	}
	if !cfg.Redis.UseRealRedis() {
		t.Fatal("Load() did not enable real redis from env override")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
