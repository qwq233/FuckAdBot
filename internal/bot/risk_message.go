package bot

import (
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	storepkg "github.com/qwq233/fuckadbot/internal/store"
)

type messageRisk struct {
	kind            string
	guestBotUserIDs []int64
}

func detectMessageRisk(msg *gotgbot.Message) messageRisk {
	if msg == nil {
		return messageRisk{}
	}

	var risk messageRisk
	if messageContainsTMe(msg) {
		risk.kind = storepkg.PendingRiskTMe
	}
	if msg.ViaBot != nil {
		risk.kind = firstRiskKind(risk.kind, storepkg.PendingRiskInline)
	}
	if msg.GuestQueryId != "" || msg.GuestBotCallerUser != nil || msg.GuestBotCallerChat != nil {
		risk.kind = storepkg.PendingRiskGuestBot
	}

	if (msg.GuestBotCallerUser != nil || msg.GuestBotCallerChat != nil) && msg.From != nil && msg.From.Id != 0 {
		risk.guestBotUserIDs = appendUniqueInt64(risk.guestBotUserIDs, msg.From.Id)
	}

	return risk
}

func moderationSubjectUser(msg *gotgbot.Message) *gotgbot.User {
	if msg == nil {
		return nil
	}
	if user := guestBotCallerUser(msg); user != nil {
		return user
	}
	return msg.From
}

func guestBotCallerUser(msg *gotgbot.Message) *gotgbot.User {
	if msg == nil {
		return nil
	}
	if msg.GuestBotCallerUser != nil {
		return msg.GuestBotCallerUser
	}
	if msg.GuestBotCallerChat != nil && msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && !msg.ReplyToMessage.From.IsBot {
		return msg.ReplyToMessage.From
	}
	return nil
}

func firstRiskKind(current, next string) string {
	if current != "" {
		return current
	}
	return next
}

func messageContainsTMe(msg *gotgbot.Message) bool {
	if containsTMe(msg.Text) || containsTMe(msg.Caption) {
		return true
	}
	for _, entity := range msg.Entities {
		if containsTMe(entity.Url) {
			return true
		}
	}
	for _, entity := range msg.CaptionEntities {
		if containsTMe(entity.Url) {
			return true
		}
	}
	return false
}

func containsTMe(value string) bool {
	return strings.Contains(strings.ToLower(value), "t.me")
}

func appendUniqueInt64(values []int64, value int64) []int64 {
	if value == 0 {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
