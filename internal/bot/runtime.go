package bot

import (
	"context"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

type VerificationURLProvider interface {
	GenerateVerifyURL(chatID, userID int64, timestamp int64, randomToken string) string
}

type pollingUpdater interface {
	StartPolling(b *gotgbot.Bot, opts *ext.PollingOpts) error
	Stop() error
	Idle()
}

type updaterFactory func(dispatcher ext.UpdateDispatcher, opts *ext.UpdaterOpts) pollingUpdater

type extUpdaterAdapter struct {
	*ext.Updater
}

func defaultUpdaterFactory(dispatcher ext.UpdateDispatcher, opts *ext.UpdaterOpts) pollingUpdater {
	return &extUpdaterAdapter{Updater: ext.NewUpdater(dispatcher, opts)}
}

func (b *Bot) ensureRuntimeState() {
	if b.timers == nil {
		b.timers = make(map[timerKey][]*time.Timer)
	}
	if b.backgroundTimers == nil {
		b.backgroundTimers = make(map[*time.Timer]struct{})
	}
	if b.newUpdater == nil {
		b.newUpdater = defaultUpdaterFactory
	}
}

func (b *Bot) SetCaptcha(provider VerificationURLProvider) {
	if b == nil {
		return
	}
	b.Captcha = provider
}

func (b *Bot) registerShutdownStop(stop func()) {
	if b == nil || stop == nil {
		return
	}

	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	b.ensureRuntimeState()
	b.shutdownStops = append(b.shutdownStops, stop)
}

func (b *Bot) stopAllBackgroundTasks() {
	if b == nil {
		return
	}

	b.timersMu.Lock()
	b.ensureRuntimeState()

	userTimers := b.timers
	backgroundTimers := b.backgroundTimers
	stopFns := b.shutdownStops

	b.timers = make(map[timerKey][]*time.Timer)
	b.backgroundTimers = make(map[*time.Timer]struct{})
	b.shutdownStops = nil
	b.timersMu.Unlock()

	for _, timers := range userTimers {
		for _, timer := range timers {
			timer.Stop()
		}
	}
	for timer := range backgroundTimers {
		timer.Stop()
	}
	for _, stop := range stopFns {
		stop()
	}
}

func (b *Bot) trackBackgroundTimer(t *time.Timer) {
	if b == nil || t == nil {
		return
	}

	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	b.ensureRuntimeState()
	b.backgroundTimers[t] = struct{}{}
}

func (b *Bot) removeBackgroundTimer(target *time.Timer) {
	if b == nil || target == nil {
		return
	}

	b.timersMu.Lock()
	defer b.timersMu.Unlock()
	if b.backgroundTimers == nil {
		return
	}
	delete(b.backgroundTimers, target)
}

func (b *Bot) scheduleBackgroundTimer(delay time.Duration, fn func()) *time.Timer {
	if b == nil || delay <= 0 || fn == nil {
		return nil
	}

	trackedTimer := make(chan *time.Timer, 1)
	timer := time.AfterFunc(delay, func() {
		timer := <-trackedTimer
		defer b.removeBackgroundTimer(timer)
		fn()
	})
	b.trackBackgroundTimer(timer)
	trackedTimer <- timer
	return timer
}

func (c *botCache) startCleanup(ctx context.Context) func() {
	stop := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(cacheCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case now := <-ticker.C:
				c.evictExpired(now)
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(stop)
		})
	}
}
