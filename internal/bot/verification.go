package bot

import (
	"log"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func (b *Bot) HandleVerificationSuccess(chatID, userID int64) {
	pending, err := b.Store.GetPending(chatID, userID)
	if err != nil {
		log.Printf("[bot] store.GetPending error during verification success: %v", err)
	}

	if err := b.approveUser(chatID, userID); err != nil {
		log.Printf("[bot] approveUser error during verification success: %v", err)
		return
	}

	if pending == nil {
		return
	}

	if pending.ReminderMessageID != 0 {
		if _, err := b.Bot.DeleteMessage(chatID, pending.ReminderMessageID, nil); err != nil {
			log.Printf("[bot] delete reminder message after verification error: %v", err)
		}
	}
	if pending.PrivateMessageID != 0 {
		if _, err := b.Bot.DeleteMessage(userID, pending.PrivateMessageID, nil); err != nil {
			log.Printf("[bot] delete private verification message after verification error: %v", err)
		}
	}

	userLanguage := userLanguageFromPending(pending)
	text := tr(userLanguage, "verification_success", chatID)
	successMsg, err := b.Bot.SendMessage(userID, text, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	if err != nil {
		log.Printf("[bot] send verification success private message error: %v", err)
		return
	}

	scheduleMessageDeletion(b.Bot, userID, successMsg.MessageId, manualModerationResultTTL, "verification success private")
}
