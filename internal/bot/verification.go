package bot

import (
	"log"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/qwq233/fuckadbot/internal/captcha"
	"github.com/qwq233/fuckadbot/internal/store"
)

func (b *Bot) HandleVerificationSuccess(token captcha.VerifiedToken) {
	result, err := b.Store.ResolvePendingByToken(
		token.ChatID,
		token.UserID,
		token.Timestamp,
		token.RandomToken,
		store.PendingActionApprove,
		b.Config.Moderation.MaxWarnings,
	)
	if err != nil {
		log.Printf("[bot] resolve verification success error: %v", err)
		b.recordInternalFault("store.resolve_pending_by_token", err)
		return
	}
	if !result.Matched {
		return
	}

	b.cancelUserTimers(token.ChatID, token.UserID)

	pending := result.Pending
	if pending == nil {
		return
	}

	if pending.ReminderMessageID != 0 {
		deleteMessageIfExists(b.Bot, token.ChatID, pending.ReminderMessageID, "verification reminder after verification")
	}
	if pending.PrivateMessageID != 0 {
		deleteMessageIfExists(b.Bot, token.UserID, pending.PrivateMessageID, "private verification after verification")
	}

	userLanguage := userLanguageFromPending(pending)
	text := tr(userLanguage, "verification_success", token.ChatID)
	successMsg, err := sendMessageWithLog(b.Bot, token.UserID, text, &gotgbot.SendMessageOpts{ParseMode: "HTML"}, "verification success private")
	if err != nil {
		return
	}

	b.scheduleMessageDeletion(b.Bot, token.UserID, successMsg.MessageId, manualModerationResultTTL, "verification success private")
}
