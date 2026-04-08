package bot

import (
	"log"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

const manualModerationResultTTL = 15 * time.Second

func scheduleMessageDeletion(bot *gotgbot.Bot, chatID, messageID int64, delay time.Duration, label string) {
	if bot == nil || chatID == 0 || messageID == 0 || delay <= 0 {
		return
	}

	time.AfterFunc(delay, func() {
		deleteMessageIfExists(bot, chatID, messageID, label)
	})
}

func deleteMessageIfExists(bot *gotgbot.Bot, chatID, messageID int64, label string) {
	if bot == nil || chatID == 0 || messageID == 0 {
		return
	}

	if _, err := bot.DeleteMessage(chatID, messageID, nil); err != nil && !strings.Contains(strings.ToLower(err.Error()), "message to delete not found") {
		log.Printf("[bot] delete %s message error: chat=%d message=%d err=%v", label, chatID, messageID, err)
	}
}

func banChatMember(bot *gotgbot.Bot, chatID, userID int64, label string) bool {
	if bot == nil || chatID == 0 || userID == 0 {
		return false
	}

	if _, err := bot.BanChatMember(chatID, userID, &gotgbot.BanChatMemberOpts{}); err != nil {
		log.Printf("[bot] ban %s user error: chat=%d user=%d err=%v", label, chatID, userID, err)
		return false
	}

	return true
}

func sendMessageWithLog(bot *gotgbot.Bot, chatID int64, text string, opts *gotgbot.SendMessageOpts, label string) (*gotgbot.Message, error) {
	msg, err := bot.SendMessage(chatID, text, opts)
	if err != nil {
		log.Printf("[bot] send %s message error: chat=%d err=%v", label, chatID, err)
		return nil, err
	}
	return msg, nil
}

func editReplyMarkupWithLog(bot *gotgbot.Bot, message *gotgbot.Message, opts *gotgbot.EditMessageReplyMarkupOpts, label string) {
	if message == nil {
		return
	}

	if _, _, err := message.EditReplyMarkup(bot, opts); err != nil {
		log.Printf("[bot] edit %s reply markup error: chat=%d message=%d err=%v", label, message.Chat.Id, message.MessageId, err)
	}
}

func editTextWithLog(bot *gotgbot.Bot, message gotgbot.MaybeInaccessibleMessage, text string, opts *gotgbot.EditMessageTextOpts, label string) bool {
	if message == nil {
		return false
	}

	if _, _, err := message.EditText(bot, text, opts); err != nil {
		log.Printf("[bot] edit %s text error: chat=%d message=%d err=%v", label, message.GetChat().Id, message.GetMessageId(), err)
		return false
	}

	return true
}
