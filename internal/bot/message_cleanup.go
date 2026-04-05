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
		if _, err := bot.DeleteMessage(chatID, messageID, nil); err != nil && !strings.Contains(strings.ToLower(err.Error()), "message to delete not found") {
			log.Printf("[bot] delete %s message error: chat=%d message=%d err=%v", label, chatID, messageID, err)
		}
	})
}
