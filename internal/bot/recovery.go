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

	pendingVerifications, err := b.Store.ListPendingVerifications()
	if err != nil {
		return err
	}
	if len(pendingVerifications) == 0 {
		return nil
	}

	now := time.Now().UTC()
	for _, pending := range pendingVerifications {
		pending := pending
		if !pending.ExpireAt.After(now) {
			b.onVerifyWindowExpired(bot, pending)
			continue
		}

		b.scheduleOriginalMessageDeletion(bot, pending)
		b.scheduleUserTimer(pending.ChatID, pending.UserID, pending.ExpireAt.Sub(now), func() {
			b.onVerifyWindowExpired(bot, pending)
		})
	}

	log.Printf("[bot] restored %d pending verification records after startup", len(pendingVerifications))
	return nil
}
