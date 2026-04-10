package bot

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/callbackquery"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/message"

	"github.com/qwq233/fuckadbot/internal/blacklist"
	"github.com/qwq233/fuckadbot/internal/config"
	"github.com/qwq233/fuckadbot/internal/store"
)

type timerKey struct{ chatID, userID int64 }

const dispatcherMaxRoutines = 16

type Bot struct {
	Bot       *gotgbot.Bot
	Config    *config.Config
	Store     store.Store
	Blacklist *blacklist.Blacklist
	Captcha   VerificationURLProvider

	cache            botCache
	timersMu         sync.Mutex
	timers           map[timerKey][]*time.Timer
	backgroundTimers map[*time.Timer]struct{}
	shutdownStops    []func()
	newUpdater       updaterFactory
}

func New(cfg *config.Config, st store.Store, bl *blacklist.Blacklist, cs VerificationURLProvider) (*Bot, error) {
	return newWithTelegramFactory(cfg, st, bl, cs, gotgbot.NewBot)
}

func newWithTelegramFactory(cfg *config.Config, st store.Store, bl *blacklist.Blacklist, cs VerificationURLProvider, newTelegramBot func(token string, opts *gotgbot.BotOpts) (*gotgbot.Bot, error)) (*Bot, error) {
	b, err := newTelegramBot(cfg.Bot.Token, nil)
	if err != nil {
		return nil, err
	}

	log.Printf("[bot] Authorized as @%s (ID: %d)", b.Username, b.Id)

	botInstance := &Bot{
		Bot:       b,
		Config:    cfg,
		Store:     st,
		Blacklist: bl,
		Captcha:   cs,
	}
	botInstance.ensureRuntimeState()
	return botInstance, nil
}

// trackUserTimer registers a timer so it can be cancelled via cancelUserTimers.
func (b *Bot) trackUserTimer(chatID, userID int64, t *time.Timer) {
	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	b.ensureRuntimeState()
	key := timerKey{chatID, userID}
	b.timers[key] = append(b.timers[key], t)
}

func (b *Bot) removeTrackedTimer(chatID, userID int64, target *time.Timer) {
	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	if b.timers == nil {
		return
	}

	key := timerKey{chatID, userID}
	timers := b.timers[key]
	if len(timers) == 0 {
		return
	}

	filtered := timers[:0]
	for _, timer := range timers {
		if timer == target {
			continue
		}
		filtered = append(filtered, timer)
	}

	if len(filtered) == 0 {
		delete(b.timers, key)
		return
	}

	b.timers[key] = filtered
}

// cancelUserTimers stops all pending timers for a (chatID, userID) pair.
func (b *Bot) cancelUserTimers(chatID, userID int64) {
	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	if b.timers == nil {
		return
	}
	key := timerKey{chatID, userID}
	for _, t := range b.timers[key] {
		t.Stop()
	}
	delete(b.timers, key)
}

func (b *Bot) cancelAllTimersForUser(userID int64) {
	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	if b.timers == nil {
		return
	}

	for key, timers := range b.timers {
		if key.userID != userID {
			continue
		}
		for _, t := range timers {
			t.Stop()
		}
		delete(b.timers, key)
	}
}

func (b *Bot) scheduleUserTimer(chatID, userID int64, delay time.Duration, fn func()) *time.Timer {
	if b == nil || delay <= 0 || fn == nil {
		return nil
	}

	trackedTimer := make(chan *time.Timer, 1)
	timer := time.AfterFunc(delay, func() {
		timer := <-trackedTimer
		defer b.removeTrackedTimer(chatID, userID, timer)
		fn()
	})
	b.trackUserTimer(chatID, userID, timer)
	trackedTimer <- timer
	return timer
}

func (b *Bot) Start(ctx context.Context) (err error) {
	b.ensureRuntimeState()

	runCtx, cancelRun := context.WithCancel(ctx)
	defer func() {
		if err != nil {
			cancelRun()
			b.stopAllBackgroundTasks()
		}
	}()

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(_ *gotgbot.Bot, _ *ext.Context, err error) ext.DispatcherAction {
			log.Printf("[bot] handler error: %v", err)
			return ext.DispatcherActionNoop
		},
		MaxRoutines: dispatcherMaxRoutines,
	})

	updaterErrors := newUpdaterErrorThrottler()
	updater := b.newUpdater(dispatcher, &ext.UpdaterOpts{
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

	if err := b.restorePendingVerifications(b.Bot); err != nil {
		return fmt.Errorf("restore pending verifications: %w", err)
	}

	log.Printf("[bot] Starting polling...")
	err = updater.StartPolling(b.Bot, &ext.PollingOpts{
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

	b.registerShutdownStop(b.cache.startCleanup(runCtx))

	log.Printf("[bot] Bot is running. Press Ctrl+C to stop.")
	var stopUpdaterOnce sync.Once
	stopUpdater := func() {
		stopUpdaterOnce.Do(func() {
			if stopErr := updater.Stop(); stopErr != nil {
				log.Printf("[bot] updater.Stop error: %v", stopErr)
			}
		})
	}
	go func() {
		<-ctx.Done()
		log.Printf("[bot] Shutting down...")
		cancelRun()
		stopUpdater()
		b.stopAllBackgroundTasks()
	}()
	updater.Idle()
	cancelRun()
	stopUpdater()
	b.stopAllBackgroundTasks()
	return nil
}
