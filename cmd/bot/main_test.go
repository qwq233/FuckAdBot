package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/qwq233/fuckadbot/internal/blacklist"
	botpkg "github.com/qwq233/fuckadbot/internal/bot"
	"github.com/qwq233/fuckadbot/internal/captcha"
	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

type fakeAppBot struct {
	started        chan struct{}
	startErr       error
	stopCause      error
	captcha        botpkg.VerificationURLProvider
	errCh          chan error
	tripOnRecord   bool
	recordedFaults []string
}

func (b *fakeAppBot) Start(ctx context.Context) error {
	if b.started != nil {
		close(b.started)
	}
	if b.startErr != nil {
		return b.startErr
	}

	<-ctx.Done()
	b.stopCause = context.Cause(ctx)
	return nil
}

func (b *fakeAppBot) HandleVerificationSuccess(token captcha.VerifiedToken) {}

func (b *fakeAppBot) SetCaptcha(provider botpkg.VerificationURLProvider) {
	b.captcha = provider
}

func (b *fakeAppBot) Errors() <-chan error {
	if b.errCh == nil {
		b.errCh = make(chan error, 1)
	}
	return b.errCh
}

func (b *fakeAppBot) RecordInternalFault(component string, err error) {
	if err == nil {
		return
	}

	b.recordedFaults = append(b.recordedFaults, fmt.Sprintf("%s: %v", component, err))
	if !b.tripOnRecord {
		return
	}
	b.Errors()

	select {
	case b.errCh <- fmt.Errorf("%s: %w", component, err):
	default:
	}
}

type fakeCaptchaService struct {
	startCalls    int
	shutdownCalls int
	errCh         chan error
}

func (s *fakeCaptchaService) GenerateVerifyURL(chatID, userID int64, timestamp int64, randomToken string) string {
	return "https://example.invalid/verify"
}

func (s *fakeCaptchaService) Start() error {
	s.startCalls++
	return nil
}

func (s *fakeCaptchaService) Shutdown(ctx context.Context) error {
	s.shutdownCalls++
	return nil
}

func (s *fakeCaptchaService) Errors() <-chan error {
	if s.errCh == nil {
		s.errCh = make(chan error, 1)
	}
	return s.errCh
}

func TestRunWithDepsLoadsBlacklistAndReturnsBotStartError(t *testing.T) {
	configPath := writeConfigFile(t, false)
	deps := defaultTestDeps()

	var globalWords []string
	var groupWords []string

	deps.newStore = func(cfg *config.Config) (store.Store, error) {
		st, err := newStoreFromConfig(cfg)
		if err != nil {
			return nil, err
		}
		if err := st.AddBlacklistWord(0, "store-word", "test"); err != nil {
			st.Close()
			return nil, err
		}
		if err := st.AddBlacklistWord(-100123, "group-word", "test"); err != nil {
			st.Close()
			return nil, err
		}
		return st, nil
	}
	deps.newBot = func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error) {
		globalWords = bl.List()
		groupWords = bl.ListGroup(-100123)
		return &fakeAppBot{startErr: errors.New("bot start failed")}, nil
	}

	err := runWithDeps(context.Background(), configPath, deps)
	if err == nil || !strings.Contains(err.Error(), "bot start failed") {
		t.Fatalf("runWithDeps() error = %v, want wrapped bot start failure", err)
	}
	if !contains(globalWords, "config-word") || !contains(globalWords, "store-word") {
		t.Fatalf("global blacklist = %v, want config and store words merged", globalWords)
	}
	if !contains(groupWords, "group-word") {
		t.Fatalf("group blacklist = %v, want store group word", groupWords)
	}
}

func TestRunWithDepsCancelsBotAndShutsDownCaptchaOnServeFailure(t *testing.T) {
	configPath := writeConfigFile(t, true)
	deps := defaultTestDeps()

	fakeBot := &fakeAppBot{started: make(chan struct{}), tripOnRecord: true, errCh: make(chan error, 1)}
	fakeCaptcha := &fakeCaptchaService{errCh: make(chan error, 1)}

	deps.newBot = func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error) {
		return fakeBot, nil
	}
	deps.newCaptcha = func(cfg *config.Config, st store.Store, onVerify func(token captcha.VerifiedToken)) captchaService {
		return fakeCaptcha
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithDeps(context.Background(), configPath, deps)
	}()

	<-fakeBot.started
	fakeCaptcha.errCh <- errors.New("serve failed")

	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "bot stopped unexpectedly") {
		t.Fatalf("runWithDeps() error = %v, want bot fatal propagated from captcha serve failure", err)
	}
	if fakeCaptcha.startCalls != 1 {
		t.Fatalf("captcha Start() calls = %d, want 1", fakeCaptcha.startCalls)
	}
	if fakeCaptcha.shutdownCalls != 1 {
		t.Fatalf("captcha Shutdown() calls = %d, want 1", fakeCaptcha.shutdownCalls)
	}
	if fakeBot.captcha != fakeCaptcha {
		t.Fatal("bot captcha provider was not attached")
	}
	if len(fakeBot.recordedFaults) != 1 || !strings.Contains(fakeBot.recordedFaults[0], "captcha.server: serve failed") {
		t.Fatalf("recorded faults = %v, want captcha.server fuse input", fakeBot.recordedFaults)
	}
	if fakeBot.stopCause == nil || !strings.Contains(fakeBot.stopCause.Error(), "bot stopped unexpectedly") || !strings.Contains(fakeBot.stopCause.Error(), "captcha.server: serve failed") {
		t.Fatalf("bot stop cause = %v, want wrapped bot fatal cause", fakeBot.stopCause)
	}
}

func defaultTestDeps() appDeps {
	return appDeps{
		loadConfig: config.Load,
		newStore:   newStoreFromConfig,
		loadBlacklist: func(st store.Store) (map[int64][]string, error) {
			return st.GetAllBlacklistWords()
		},
	}
}

func writeConfigFile(t *testing.T, enableTurnstile bool) string {
	t.Helper()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	content := `[bot]
token = "123:test-token"
admins = [7]

[turnstile]
enabled = ` + boolString(enableTurnstile) + `
site_key = "site-key"
secret_key = "secret-key"
domain = "verify.example.com"
listen_addr = "127.0.0.1"
listen_port = 8080
verify_timeout = "5m"

[blacklist]
words = ["config-word"]

[moderation]
max_warnings = 3
reminder_ttl = 30
original_message_ttl = "1m"
verify_window = "5m"

[store]
type = "sqlite"
data_path = "` + filepath.ToSlash(tempDir) + `"
`

	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return configPath
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestNewStoreFromConfigSupportsAllConfiguredModes(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		st, err := newStoreFromConfig(&config.Config{
			Store: config.StoreConfig{
				Type:     "sqlite",
				DataPath: t.TempDir(),
			},
		})
		if err != nil {
			t.Fatalf("newStoreFromConfig(sqlite) error = %v", err)
		}
		defer st.Close()
	})

	t.Run("redis", func(t *testing.T) {
		t.Parallel()

		redisSrv := miniredis.RunT(t)
		st, err := newStoreFromConfig(&config.Config{
			Store: config.StoreConfig{
				Type:      "redis",
				DataPath:  t.TempDir(),
				RedisAddr: redisSrv.Addr(),
			},
		})
		if err != nil {
			t.Fatalf("newStoreFromConfig(redis) error = %v", err)
		}
		defer st.Close()
	})

	t.Run("dual write", func(t *testing.T) {
		t.Parallel()

		redisSrv := miniredis.RunT(t)
		st, err := newStoreFromConfig(&config.Config{
			Store: config.StoreConfig{
				Type:                            "sqlite",
				DataPath:                        t.TempDir(),
				RedisAddr:                       redisSrv.Addr(),
				DualWriteEnabled:                true,
				DualWriteFlushInterval:          "1s",
				DualWriteBatchSize:              10,
				DualWriteMaxConsecutiveFailures: 5,
				DualWriteMaxQueueDepth:          100,
			},
		})
		if err != nil {
			t.Fatalf("newStoreFromConfig(dual write) error = %v", err)
		}
		defer st.Close()
	})
}
