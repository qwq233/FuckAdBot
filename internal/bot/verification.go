package bot

import (
	"fmt"
	"log"
	"time"

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

	sendOpts := &gotgbot.SendMessageOpts{ParseMode: "HTML"}
	if pending.MessageThreadID != 0 {
		sendOpts.MessageThreadId = pending.MessageThreadID
	}
	if pending.ReplyToMessageID != 0 {
		sendOpts.ReplyParameters = &gotgbot.ReplyParameters{
			MessageId:                pending.ReplyToMessageID,
			AllowSendingWithoutReply: true,
		}
	}

	text := fmt.Sprintf(`<a href="tg://user?id=%d">该用户</a> 已完成验证，现在可以正常发言了。`, userID)
	msg, err := b.Bot.SendMessage(chatID, text, sendOpts)
	if err != nil {
		log.Printf("[bot] send verification success message error: %v", err)
		return
	}

	time.AfterFunc(15*time.Second, func() {
		if _, err := b.Bot.DeleteMessage(chatID, msg.MessageId, nil); err != nil {
			log.Printf("[bot] delete verification success message error: %v", err)
		}
	})
}
