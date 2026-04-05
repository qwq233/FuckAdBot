package bot

import (
	"fmt"
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

	// Send success notification to user via private chat.
	text := fmt.Sprintf("✅ 您已完成群组 <code>%d</code> 的人机验证，现在可以正常发言了。", chatID)
	b.Bot.SendMessage(userID, text, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
}
