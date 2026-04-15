package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/captcha"
	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

type shutdownErrorCaptchaService struct {
	shutdownCalls int
	deadlineSet   bool
}

type startErrorCaptchaService struct {
	startCalls int
	startErr   error
}

func (s *startErrorCaptchaService) GenerateVerifyURL(chatID, userID int64, timestamp int64, randomToken string) string {
	return "https://example.invalid/verify"
}

func (s *startErrorCaptchaService) Start() error {
	s.startCalls++
	return s.startErr
}

func (s *startErrorCaptchaService) Shutdown(ctx context.Context) error {
	return nil
}

func (s *startErrorCaptchaService) Errors() <-chan error {
	return nil
}

type reportingStore struct {
	store.Store
	errCh chan error
}

func (s *reportingStore) Errors() <-chan error {
	return s.errCh
}

func (s *shutdownErrorCaptchaService) GenerateVerifyURL(chatID, userID int64, timestamp int64, randomToken string) string {
	return "https://example.invalid/verify"
}

func (s *shutdownErrorCaptchaService) Start() error {
	return nil
}

func (s *shutdownErrorCaptchaService) Shutdown(ctx context.Context) error {
	s.shutdownCalls++
	_, s.deadlineSet = ctx.Deadline()
	return errors.New("shutdown failed")
}

func (s *shutdownErrorCaptchaService) Errors() <-chan error {
	return nil
}

func TestRunWrapsDefaultConfigLoadFailures(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), "__codex_missing_config__.toml")
	if err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("run() error = %v, want wrapped load config failure", err)
	}
}

func TestAppDepsWithDefaultsPopulatesMissingFuncsAndPreservesProvidedOnes(t *testing.T) {
	t.Parallel()

	customLoadConfig := func(path string) (*config.Config, error) {
		return nil, nil
	}

	deps := appDeps{loadConfig: customLoadConfig}
	got := deps.withDefaults()

	if reflect.ValueOf(got.loadConfig).Pointer() != reflect.ValueOf(customLoadConfig).Pointer() {
		t.Fatal("withDefaults() replaced an explicitly provided loadConfig func")
	}
	if got.newStore == nil || got.loadBlacklist == nil || got.newBot == nil || got.newCaptcha == nil {
		t.Fatalf("withDefaults() left nil dependencies: %+v", got)
	}
}

func TestShutdownCaptchaServerHandlesNilAndShutdownErrors(t *testing.T) {
	t.Parallel()

	shutdownCaptchaServer(nil)

	service := &shutdownErrorCaptchaService{}
	shutdownCaptchaServer(service)

	if service.shutdownCalls != 1 {
		t.Fatalf("Shutdown() calls = %d, want 1", service.shutdownCalls)
	}
	if !service.deadlineSet {
		t.Fatal("Shutdown() context had no deadline")
	}
}

func TestRunWithDepsWrapsStoreInitializationFailures(t *testing.T) {
	t.Parallel()

	configPath := writeConfigFile(t, false)
	deps := defaultTestDeps()
	deps.newStore = func(cfg *config.Config) (store.Store, error) {
		return nil, errors.New("store init failed")
	}

	err := runWithDeps(context.Background(), configPath, deps)
	if err == nil || !strings.Contains(err.Error(), "init store: store init failed") {
		t.Fatalf("runWithDeps() error = %v, want wrapped init store failure", err)
	}
}

func TestRunWithDepsWrapsLoadConfigFailures(t *testing.T) {
	t.Parallel()

	deps := defaultTestDeps()
	deps.loadConfig = func(path string) (*config.Config, error) {
		return nil, errors.New("parse failed")
	}

	err := runWithDeps(context.Background(), "ignored.toml", deps)
	if err == nil || !strings.Contains(err.Error(), "load config: parse failed") {
		t.Fatalf("runWithDeps() error = %v, want wrapped load config failure", err)
	}
}

func TestRunWithDepsWrapsBotConstructionFailures(t *testing.T) {
	t.Parallel()

	configPath := writeConfigFile(t, false)
	deps := defaultTestDeps()
	deps.newBot = func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error) {
		return nil, errors.New("create bot failed")
	}

	err := runWithDeps(context.Background(), configPath, deps)
	if err == nil || !strings.Contains(err.Error(), "create bot: create bot failed") {
		t.Fatalf("runWithDeps() error = %v, want wrapped create bot failure", err)
	}
}

func TestRunWithDepsReportsStoreRuntimeFailuresThroughBotFuse(t *testing.T) {
	t.Parallel()

	configPath := writeConfigFile(t, false)
	deps := defaultTestDeps()

	fakeBot := &fakeAppBot{
		started:      make(chan struct{}),
		tripOnRecord: true,
		errCh:        make(chan error, 1),
	}
	var reporter *reportingStore

	deps.newStore = func(cfg *config.Config) (store.Store, error) {
		st, err := newStoreFromConfig(cfg)
		if err != nil {
			return nil, err
		}
		reporter = &reportingStore{
			Store: st,
			errCh: make(chan error, 1),
		}
		return reporter, nil
	}
	deps.newBot = func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error) {
		return fakeBot, nil
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithDeps(context.Background(), configPath, deps)
	}()

	<-fakeBot.started
	reporter.errCh <- errors.New("queue stalled")

	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "bot stopped unexpectedly") {
		t.Fatalf("runWithDeps() error = %v, want bot fuse failure", err)
	}
	if len(fakeBot.recordedFaults) != 1 || !strings.Contains(fakeBot.recordedFaults[0], "store.runtime: queue stalled") {
		t.Fatalf("recorded faults = %v, want store.runtime fault", fakeBot.recordedFaults)
	}
}

func TestRunWithDepsLogsBlacklistLoadFailuresAndContinuesStartup(t *testing.T) {
	t.Parallel()

	configPath := writeConfigFile(t, false)
	deps := defaultTestDeps()
	var globalWords []string
	deps.loadBlacklist = func(st store.Store) (map[int64][]string, error) {
		return nil, errors.New("blacklist unavailable")
	}
	deps.newBot = func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error) {
		globalWords = bl.List()
		return &fakeAppBot{startErr: errors.New("bot start failed")}, nil
	}

	err := runWithDeps(context.Background(), configPath, deps)
	if err == nil || !strings.Contains(err.Error(), "bot start failed") {
		t.Fatalf("runWithDeps() error = %v, want bot start failure after blacklist warning", err)
	}
	if !contains(globalWords, "config-word") {
		t.Fatalf("global blacklist = %v, want config blacklist words loaded despite store error", globalWords)
	}
}

func TestRunWithDepsWrapsCaptchaStartFailures(t *testing.T) {
	t.Parallel()

	configPath := writeConfigFile(t, true)
	deps := defaultTestDeps()

	fakeBot := &fakeAppBot{}
	startFailCaptcha := &startErrorCaptchaService{startErr: errors.New("bind failed")}

	deps.newBot = func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error) {
		return fakeBot, nil
	}
	deps.newCaptcha = func(cfg *config.Config, st store.Store, onVerify func(token captcha.VerifiedToken)) captchaService {
		return startFailCaptcha
	}

	err := runWithDeps(context.Background(), configPath, deps)
	if err == nil || !strings.Contains(err.Error(), "start captcha server: bind failed") {
		t.Fatalf("runWithDeps() error = %v, want wrapped captcha start failure", err)
	}
	if startFailCaptcha.startCalls != 1 {
		t.Fatalf("captcha Start() calls = %d, want 1", startFailCaptcha.startCalls)
	}
	if fakeBot.captcha != startFailCaptcha {
		t.Fatal("bot captcha provider was not attached before Start()")
	}
}

func TestRunWithDepsReturnsNilOnGracefulCancellation(t *testing.T) {
	t.Parallel()

	configPath := writeConfigFile(t, false)
	deps := defaultTestDeps()
	fakeBot := &fakeAppBot{started: make(chan struct{})}
	deps.newBot = func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error) {
		return fakeBot, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithDeps(ctx, configPath, deps)
	}()

	<-fakeBot.started
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("runWithDeps() error = %v, want nil on graceful cancellation", err)
	}
	if fakeBot.stopCause == nil || !errors.Is(fakeBot.stopCause, context.Canceled) {
		t.Fatalf("bot stop cause = %v, want context canceled", fakeBot.stopCause)
	}
}

func TestAppDepsWithDefaultsDefaultClosuresProduceWorkingImplementations(t *testing.T) {
	t.Parallel()

	configPath := writeConfigFile(t, true)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	st, err := newStoreFromConfig(cfg)
	if err != nil {
		t.Fatalf("newStoreFromConfig() error = %v", err)
	}
	defer st.Close()

	if err := st.AddBlacklistWord(0, "store-word", "tester"); err != nil {
		t.Fatalf("AddBlacklistWord() error = %v", err)
	}

	deps := (appDeps{}).withDefaults()
	allWords, err := deps.loadBlacklist(st)
	if err != nil {
		t.Fatalf("default loadBlacklist() error = %v", err)
	}
	if got := allWords[0]; !contains(got, "store-word") {
		t.Fatalf("default loadBlacklist()[0] = %v, want store blacklist entry", got)
	}

	defaultBot, err := deps.newBot(cfg, st, blacklist.New())
	if err == nil || !strings.Contains(err.Error(), "getMe") {
		t.Fatalf("default newBot() error = %v, want bot initialization error after closure execution", err)
	}
	if defaultBot == nil {
		t.Fatal("default newBot() = nil, want bot instance plus initialization error")
	}

	defaultCaptcha := deps.newCaptcha(cfg, st, func(token captcha.VerifiedToken) {})
	if defaultCaptcha == nil {
		t.Fatal("default newCaptcha() = nil, want captcha implementation")
	}
	if got := defaultCaptcha.GenerateVerifyURL(-100123, 42, 123, "token-a"); !strings.Contains(got, config.VerifyPath) {
		t.Fatalf("default captcha verify URL = %q, want %q path", got, config.VerifyPath)
	}
}
