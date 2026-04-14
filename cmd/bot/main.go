package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/bot"
	"github.com/qwq233/fuckadbot/internal/captcha"
	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

type appBot interface {
	Start(ctx context.Context) error
	HandleVerificationSuccess(token captcha.VerifiedToken)
	SetCaptcha(provider bot.VerificationURLProvider)
	Errors() <-chan error
	RecordInternalFault(component string, err error)
}

type captchaService interface {
	bot.VerificationURLProvider
	Start() error
	Shutdown(ctx context.Context) error
	Errors() <-chan error
}

type appDeps struct {
	loadConfig    func(path string) (*config.Config, error)
	newStore      func(cfg *config.Config) (store.Store, error)
	loadBlacklist func(st store.Store) (map[int64][]string, error)
	newBot        func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error)
	newCaptcha    func(cfg *config.Config, st store.Store, onVerify func(token captcha.VerifiedToken)) captchaService
}

func main() {
	configPath := flag.String("config", "config.toml", "path to config file")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *configPath); err != nil {
		log.Fatalf("Application error: %v", err)
	}
}

func run(ctx context.Context, configPath string) error {
	return runWithDeps(ctx, configPath, appDeps{})
}

func runWithDeps(ctx context.Context, configPath string, deps appDeps) error {
	deps = deps.withDefaults()

	cfg, err := deps.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log.Printf("[main] Config loaded from %s", configPath)

	st, err := deps.newStore(cfg)
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}
	defer st.Close()
	log.Printf("[main] Store initialized (type=%s)", cfg.Store.Type)

	bl := blacklist.New()
	bl.Load(cfg.Blacklist.Words)

	allWords, err := deps.loadBlacklist(st)
	if err != nil {
		log.Printf("[main] Warning: failed to load blacklist from store: %v", err)
	} else {
		if globalWords, ok := allWords[0]; ok {
			bl.Load(globalWords)
		}
		for chatID, words := range allWords {
			if chatID == 0 {
				continue
			}
			bl.LoadGroup(chatID, words)
		}
	}
	log.Printf("[main] Blacklist loaded (%d global words)", len(bl.List()))

	b, err := deps.newBot(cfg, st, bl)
	if err != nil {
		return fmt.Errorf("create bot: %w", err)
	}

	appCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	go func() {
		select {
		case err := <-b.Errors():
			if err != nil {
				cancel(fmt.Errorf("bot stopped unexpectedly: %w", err))
			}
		case <-appCtx.Done():
		}
	}()

	if reporter, ok := st.(store.ErrorReporter); ok {
		go func() {
			select {
			case err := <-reporter.Errors():
				if err != nil {
					b.RecordInternalFault("store.runtime", err)
				}
			case <-appCtx.Done():
			}
		}()
	}

	if cfg.Turnstile.Enabled {
		cs := deps.newCaptcha(cfg, st, func(token captcha.VerifiedToken) {
			log.Printf("[captcha] User %d verified in chat %d", token.UserID, token.ChatID)
			b.HandleVerificationSuccess(token)
		})
		b.SetCaptcha(cs)

		if err := cs.Start(); err != nil {
			return fmt.Errorf("start captcha server: %w", err)
		}
		defer shutdownCaptchaServer(cs)

		go func() {
			select {
			case err := <-cs.Errors():
				if err != nil {
					b.RecordInternalFault("captcha.server", err)
				}
			case <-appCtx.Done():
			}
		}()
	}

	if err := b.Start(appCtx); err != nil {
		return err
	}

	if cause := context.Cause(appCtx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}

	log.Printf("[main] Shutdown complete.")
	return nil
}

func shutdownCaptchaServer(cs captchaService) {
	if cs == nil {
		return
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := cs.Shutdown(shutCtx); err != nil {
		log.Printf("[captcha] Shutdown error: %v", err)
	}
}

func (d appDeps) withDefaults() appDeps {
	if d.loadConfig == nil {
		d.loadConfig = config.Load
	}
	if d.newStore == nil {
		d.newStore = newStoreFromConfig
	}
	if d.loadBlacklist == nil {
		d.loadBlacklist = func(st store.Store) (map[int64][]string, error) {
			return st.GetAllBlacklistWords()
		}
	}
	if d.newBot == nil {
		d.newBot = func(cfg *config.Config, st store.Store, bl *blacklist.Blacklist) (appBot, error) {
			return bot.New(cfg, st, bl, nil)
		}
	}
	if d.newCaptcha == nil {
		d.newCaptcha = func(cfg *config.Config, st store.Store, onVerify func(token captcha.VerifiedToken)) captchaService {
			return captcha.NewServer(&cfg.Turnstile, st, cfg.Moderation.GetVerifyWindow(), cfg.Bot.Token, onVerify)
		}
	}
	return d
}

func newStoreFromConfig(cfg *config.Config) (store.Store, error) {
	return store.NewFromConfig(cfg.Store)
}
