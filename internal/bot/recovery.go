package bot

import (
	"log"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func (b *Bot) restorePendingVerifications(bot *gotgbot.Bot) error {
	if b == nil || b.Store == nil {
		return nil
	}

	startedAt := time.Now().UTC()
	pendingVerifications, err := b.Store.ListPendingVerifications()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	expiredCount := 0
	for _, pending := range pendingVerifications {
		pending := pending
		if !pending.ExpireAt.After(now) {
			expiredCount++
			b.onVerifyWindowExpired(bot, pending)
		}
	}

	log.Printf("[bot] restored pending verification backlog=%d expired_now=%d recovery_took=%s", len(pendingVerifications), expiredCount, time.Since(startedAt))
	return nil
}
