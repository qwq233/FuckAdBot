package bot

import (
	"crypto/rand"
	"fmt"
	"log"
	"time"
	"unicode/utf8"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/qwq233/fuckadbot/internal/store"
)

func (b *Bot) handleMessage(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || msg.From == nil {
		return nil
	}

	// Skip auto-forwarded channel posts and anonymous admin messages
	if msg.IsAutomaticForward || msg.SenderChat != nil {
		return nil
	}

	user := msg.From
	chatID := msg.Chat.Id

	// --- Blacklist check (every message, including verified users) ---
	checkText := buildCheckText(user)

	// Best-effort bio fetch
	if bioChat, err := bot.GetChat(user.Id, nil); err == nil {
		if bioChat.Bio != "" {
			checkText += " " + bioChat.Bio
		}
	}

	if matched := b.Blacklist.Match(checkText); matched != "" {
		log.Printf("[bot] blacklist hit: user=%d word=%q in chat=%d", user.Id, matched, chatID)
		bot.DeleteMessage(chatID, msg.MessageId, nil)
		bot.BanChatMember(chatID, user.Id, &gotgbot.BanChatMemberOpts{})
		return nil
	}

	// --- Verification status check ---
	verified, err := b.Store.IsVerified(chatID, user.Id)
	if err != nil {
		log.Printf("[bot] store.IsVerified error: %v", err)
		return nil
	}
	if verified {
		return nil // Verified user, allow message
	}

	// Check if rejected by admin
	rejected, err := b.Store.IsRejected(chatID, user.Id)
	if err != nil {
		log.Printf("[bot] store.IsRejected error: %v", err)
		return nil
	}
	if rejected {
		// Silently delete, no reminder, no warning increment
		bot.DeleteMessage(chatID, msg.MessageId, nil)
		return nil
	}

	// --- Unverified user: delete message ---
	bot.DeleteMessage(chatID, msg.MessageId, nil)

	// Check if there's an active pending verification window
	hasPending, err := b.Store.HasActivePending(chatID, user.Id)
	if err != nil {
		log.Printf("[bot] store.HasActivePending error: %v", err)
		return nil
	}
	if hasPending {
		// Active window exists, silently delete without new reminder
		return nil
	}

	// --- No active window: check warning count and send reminder ---
	warnCount, err := b.Store.GetWarningCount(chatID, user.Id)
	if err != nil {
		log.Printf("[bot] store.GetWarningCount error: %v", err)
		return nil
	}

	maxWarnings := b.Config.Moderation.MaxWarnings
	if warnCount >= maxWarnings {
		// Already exceeded max warnings, ban
		log.Printf("[bot] banning user=%d in chat=%d: exceeded max warnings (%d)", user.Id, chatID, maxWarnings)
		bot.BanChatMember(chatID, user.Id, &gotgbot.BanChatMemberOpts{})
		return nil
	}

	timestamp := time.Now().UTC().Unix()
	randomToken, err := newVerificationRandomToken(7)
	if err != nil {
		log.Printf("[bot] generate verification random token error: %v", err)
		return nil
	}

	// Build reminder message with hidden username
	maskedName := maskName(user.FirstName)
	reminderText := fmt.Sprintf(
		`<a href="tg://user?id=%d">%s</a> 您需要完成人机验证才能发言，请点击下方按钮完成验证。(%d/%d)`,
		user.Id, maskedName, warnCount+1, maxWarnings,
	)

	// Send reminder in the same comment thread
	sendOpts := &gotgbot.SendMessageOpts{
		ParseMode: "HTML",
	}
	if msg.MessageThreadId != 0 {
		sendOpts.MessageThreadId = msg.MessageThreadId
	}
	if replyTargetMessageID := reminderReplyTargetMessageID(msg); replyTargetMessageID != 0 {
		sendOpts.ReplyParameters = &gotgbot.ReplyParameters{
			MessageId:                replyTargetMessageID,
			AllowSendingWithoutReply: true,
		}
	}

	reminderMsg, err := bot.SendMessage(chatID, reminderText, sendOpts)
	if err != nil {
		log.Printf("[bot] send reminder error: %v", err)
		return nil
	}

	// Record pending verification window
	verifyWindow := b.Config.Moderation.GetVerifyWindow()
	expireAt := time.Unix(timestamp, 0).UTC().Add(verifyWindow)
	if err := b.Store.SetPending(store.PendingVerification{
		ChatID:            chatID,
		UserID:            user.Id,
		Timestamp:         timestamp,
		RandomToken:       randomToken,
		ExpireAt:          expireAt,
		ReminderMessageID: reminderMsg.MessageId,
		MessageThreadID:   msg.MessageThreadId,
		ReplyToMessageID:  reminderReplyTargetMessageID(msg),
	}); err != nil {
		log.Printf("[bot] store.SetPending error: %v", err)
		if _, err := bot.DeleteMessage(chatID, reminderMsg.MessageId, nil); err != nil {
			log.Printf("[bot] delete reminder message after pending persist failure: %v", err)
		}
		return nil
	}

	verificationStartURL := BuildVerificationStartURL(bot.Username, chatID, user.Id, reminderMsg.MessageId)
	if _, _, err := reminderMsg.EditReplyMarkup(bot, &gotgbot.EditMessageReplyMarkupOpts{
		ReplyMarkup: BuildReminderKeyboard(verificationStartURL, chatID, user.Id),
	}); err != nil {
		log.Printf("[bot] edit reminder message reply markup error: %v", err)
	}

	// Keep the reminder visible for the full verification window unless configured longer.
	reminderTTL := b.Config.Moderation.GetReminderTTL()
	reminderMsgID := reminderMsg.MessageId
	time.AfterFunc(reminderTTL, func() {
		if _, err := bot.DeleteMessage(chatID, reminderMsgID, nil); err != nil {
			// Ignore: message may already be deleted
		}
	})

	// Schedule verification window expiry check
	capturedUserID := user.Id
	capturedChatID := chatID
	time.AfterFunc(verifyWindow, func() {
		b.onVerifyWindowExpired(bot, capturedChatID, capturedUserID)
	})

	return nil
}

// onVerifyWindowExpired is called when the 5-minute verification window expires.
func (b *Bot) onVerifyWindowExpired(bot *gotgbot.Bot, chatID, userID int64) {
	// Clear the pending record
	b.Store.ClearPending(chatID, userID)

	// Check if user verified during the window
	verified, err := b.Store.IsVerified(chatID, userID)
	if err != nil {
		log.Printf("[bot] store.IsVerified error in expiry: %v", err)
		return
	}
	if verified {
		return // User verified in time
	}

	// Increment warning count
	newCount, err := b.Store.IncrWarningCount(chatID, userID)
	if err != nil {
		log.Printf("[bot] store.IncrWarningCount error: %v", err)
		return
	}

	log.Printf("[bot] verify window expired: user=%d chat=%d warnings=%d/%d",
		userID, chatID, newCount, b.Config.Moderation.MaxWarnings)

	if newCount >= b.Config.Moderation.MaxWarnings {
		log.Printf("[bot] auto-banning user=%d in chat=%d: %d warnings", userID, chatID, newCount)
		bot.BanChatMember(chatID, userID, &gotgbot.BanChatMemberOpts{})
	}
}

// buildCheckText assembles text from user fields for blacklist matching.
func buildCheckText(user *gotgbot.User) string {
	text := user.FirstName
	if user.LastName != "" {
		text += " " + user.LastName
	}
	if user.Username != "" {
		text += " " + user.Username
	}
	return text
}

func reminderReplyTargetMessageID(msg *gotgbot.Message) int64 {
	if msg == nil {
		return 0
	}

	if msg.MessageThreadId != 0 {
		return msg.MessageThreadId
	}

	if msg.ReplyToMessage != nil {
		return msg.ReplyToMessage.MessageId
	}

	return 0
}

func newVerificationRandomToken(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	if length <= 0 {
		return "", fmt.Errorf("verification random token length must be positive")
	}

	randomBytes := make([]byte, length)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	token := make([]byte, length)
	for index, randomByte := range randomBytes {
		token[index] = alphabet[int(randomByte)%len(alphabet)]
	}

	return string(token), nil
}

// maskName returns the first rune of the name followed by "**".
func maskName(name string) string {
	if name == "" {
		return "用户"
	}
	r, _ := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return "用户"
	}
	return string(r) + "**"
}
