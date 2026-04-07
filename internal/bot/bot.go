package bot

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/callbackquery"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/message"

	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/captcha"
	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

type timerKey struct{ chatID, userID int64 }

type Bot struct {
	Bot       *gotgbot.Bot
	Config    *config.Config
	Store     store.Store
	Blacklist *blacklist.Blacklist
	Captcha   *captcha.Server

	cache    botCache
	timersMu sync.Mutex
	timers   map[timerKey][]*time.Timer
}

func New(cfg *config.Config, st store.Store, bl *blacklist.Blacklist, cs *captcha.Server) (*Bot, error) {
	b, err := gotgbot.NewBot(cfg.Bot.Token, nil)
	if err != nil {
		return nil, err
	}

	log.Printf("[bot] Authorized as @%s (ID: %d)", b.Username, b.Id)

	return &Bot{
		Bot:       b,
		Config:    cfg,
		Store:     st,
		Blacklist: bl,
		Captcha:   cs,
		timers:    make(map[timerKey][]*time.Timer),
	}, nil
}

// trackUserTimer registers a timer so it can be cancelled via cancelUserTimers.
func (b *Bot) trackUserTimer(chatID, userID int64, t *time.Timer) {
	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	key := timerKey{chatID, userID}
	b.timers[key] = append(b.timers[key], t)
}

// cancelUserTimers stops all pending timers for a (chatID, userID) pair.
func (b *Bot) cancelUserTimers(chatID, userID int64) {
	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	key := timerKey{chatID, userID}
	for _, t := range b.timers[key] {
		t.Stop()
	}
	delete(b.timers, key)
}

func (b *Bot) Start(ctx context.Context) error {
	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(_ *gotgbot.Bot, _ *ext.Context, err error) ext.DispatcherAction {
			log.Printf("[bot] handler error: %v", err)
			return ext.DispatcherActionNoop
		},
	})

	updaterErrors := newUpdaterErrorThrottler()
	updater := ext.NewUpdater(dispatcher, &ext.UpdaterOpts{
		UnhandledErrFunc: updaterErrors.Handle,
	})

	// Register command handlers (higher priority)
	dispatcher.AddHandler(handlers.NewCommand("addblocklist", b.cmdAddBlocklist))
	dispatcher.AddHandler(handlers.NewCommand("delblocklist", b.cmdDelBlocklist))
	dispatcher.AddHandler(handlers.NewCommand("listblocklist", b.cmdListBlocklist))
	dispatcher.AddHandler(handlers.NewCommand("approve", b.cmdApprove))
	dispatcher.AddHandler(handlers.NewCommand("reject", b.cmdReject))
	dispatcher.AddHandler(handlers.NewCommand("unreject", b.cmdUnreject))
	dispatcher.AddHandler(handlers.NewCommand("resetverify", b.cmdResetAllVerify))
	dispatcher.AddHandler(handlers.NewCommand("stats", b.cmdStats))
	dispatcher.AddHandler(handlers.NewCommand("lang", b.cmdLang))
	dispatcher.AddHandler(handlers.NewCommand("start", b.cmdStart))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix(moderationCallbackPrefix), b.handleModerationCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix(languagePreferenceCallbackPrefix), b.handleLanguagePreferenceCallback))

	// Register message handler for group/supergroup messages (lower priority)
	dispatcher.AddHandler(handlers.NewMessage(message.Supergroup, b.handleMessage))

	log.Printf("[bot] Starting polling...")
	err := updater.StartPolling(b.Bot, &ext.PollingOpts{
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout: pollingGetUpdatesTimeoutSeconds,
			RequestOpts: &gotgbot.RequestOpts{
				Timeout: pollingRequestTimeout,
			},
			AllowedUpdates: []string{"message", "callback_query"},
		},
	})
	if err != nil {
		return err
	}

	b.cache.startCleanup(ctx)

	log.Printf("[bot] Bot is running. Press Ctrl+C to stop.")
	go func() {
		<-ctx.Done()
		log.Printf("[bot] Shutting down...")
		if err := updater.Stop(); err != nil {
			log.Printf("[bot] updater.Stop error: %v", err)
		}
	}()
	updater.Idle()
	return nil
}
