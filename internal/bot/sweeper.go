package bot

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func (b *Bot) startPendingSweeper(ctx context.Context, bot *gotgbot.Bot) func() {
	stop := make(chan struct{})
	var once sync.Once

	go func() {
		interval := b.pendingSweeperInterval()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		b.runPendingSweeperTick(bot)

		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				b.runPendingSweeperTick(bot)
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(stop)
		})
	}
}

func (b *Bot) runPendingSweeperTick(bot *gotgbot.Bot) {
	if b == nil || b.Store == nil {
		return
	}

	startedAt := time.Now().UTC()
	pendingVerifications, err := b.Store.ListPendingVerifications()
	if err != nil {
		log.Printf("[bot] pending sweeper list error: %v", err)
		b.recordInternalFault("store.list_pending_verifications", err)
		b.runtimeStats.recordErrorf("pending sweeper list: %v", err)
		b.runtimeStats.recordSweeperRun(startedAt, time.Since(startedAt), 0, 0)
		return
	}

	expiredCount := 0
	now := time.Now().UTC()
	for _, pending := range pendingVerifications {
		pending := pending
		if !pending.ExpireAt.After(now) {
			expiredCount++
			b.onVerifyWindowExpired(bot, pending)
			continue
		}

		if pending.OriginalMessageID != 0 {
			if err := b.deletePendingOriginalMessage(bot, &pending, false); err != nil {
				log.Printf("[bot] pending sweeper original message cleanup error: %v", err)
				b.recordInternalFault("sweeper.original_cleanup", err)
				b.runtimeStats.recordErrorf("pending sweeper original cleanup chat=%d user=%d: %v", pending.ChatID, pending.UserID, err)
			}
		}
	}

	b.runtimeStats.recordSweeperRun(startedAt, time.Since(startedAt), len(pendingVerifications), expiredCount)
}

func (b *Bot) pendingSweeperInterval() time.Duration {
	if b == nil || b.Config == nil {
		return time.Second
	}
	return b.Config.Bot.GetPendingSweeperInterval()
}
