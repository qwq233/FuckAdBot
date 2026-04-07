package bot

import (
	"log"

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

type Bot struct {
	Bot       *gotgbot.Bot
	Config    *config.Config
	Store     store.Store
	Blacklist *blacklist.Blacklist
	Captcha   *captcha.Server

	cache botCache
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
	}, nil
}

func (b *Bot) Start() error {
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

	log.Printf("[bot] Bot is running. Press Ctrl+C to stop.")
	updater.Idle()
	return nil
}
