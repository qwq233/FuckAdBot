package main

import (
	"log"

	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/bot"
	"github.com/qwq233/fuckadbot/internal/captcha"
	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

func main() {
	const configPath = "config.toml"

	// Load config
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("[main] Config loaded from %s", configPath)

	// Initialize store
	var st store.Store
	switch cfg.Store.Type {
	case "sqlite":
		st, err = store.NewSQLiteStore(cfg.Store.SQLitePath)
		if err != nil {
			log.Fatalf("Failed to init SQLite store: %v", err)
		}
	default:
		log.Fatalf("Unsupported store type: %s", cfg.Store.Type)
	}
	defer st.Close()
	log.Printf("[main] Store initialized (type=%s)", cfg.Store.Type)

	// Initialize blacklist
	bl := blacklist.New()
	// Load from config file (global)
	bl.Load(cfg.Blacklist.Words)
	// Load from store (runtime-added words, both global and group-scoped)
	allWords, err := st.GetAllBlacklistWords()
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

	// Create bot instance (needed for captcha callback)
	b, err := bot.New(cfg, st, bl, nil)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	// Initialize captcha server with verification callback
	var cs *captcha.Server
	if cfg.Turnstile.Enabled {
		cs = captcha.NewServer(&cfg.Turnstile, st, cfg.Moderation.GetVerifyWindow(), cfg.Bot.Token, func(chatID, userID int64) {
			log.Printf("[captcha] User %d verified in chat %d", userID, chatID)
			b.HandleVerificationSuccess(chatID, userID)
		})
		b.Captcha = cs

		go func() {
			if err := cs.Start(); err != nil {
				log.Fatalf("Captcha server error: %v", err)
			}
		}()
	}

	// Start bot (blocking)
	if err := b.Start(); err != nil {
		log.Fatalf("Bot error: %v", err)
	}
}
