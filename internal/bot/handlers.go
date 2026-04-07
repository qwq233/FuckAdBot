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
	userLanguage := b.requestLanguageForUser(user)

	// --- Bot admins bypass all checks ---
	if b.isBotAdmin(user.Id) {
		return nil
	}

	// --- Blacklist check (every message, including verified users) ---
	checkText := buildCheckText(user)

	// Best-effort bio fetch
	if bioChat := b.cachedUserChat(user.Id, func(userID int64) (*gotgbot.ChatFullInfo, error) {
		return bot.GetChat(userID, nil)
	}); bioChat != nil && bioChat.Bio != "" {
		checkText += " " + bioChat.Bio
	}

	if matched := b.Blacklist.MatchWithGroup(chatID, checkText); matched != "" {
		// Don't ban group admins
		if b.isGroupAdmin(bot, chatID, user.Id) {
			return nil
		}
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

	// --- Auto-approve group admins ---
	if b.isGroupAdmin(bot, chatID, user.Id) {
		if err := b.approveUser(chatID, user.Id); err != nil {
			log.Printf("[bot] auto-approve group admin error: %v", err)
		}
		return nil
	}

	// Check if rejected by admin
	rejected, err := b.Store.IsRejected(chatID, user.Id)
	if err != nil {
		log.Printf("[bot] store.IsRejected error: %v", err)
		return nil
	}
	if rejected {
		// Silently delete, no reminder, no warning increment
		deleteMessageIfExists(bot, chatID, msg.MessageId, "rejected user message")
		return nil
	}

	// Check if there's an active pending verification window
	hasPending, err := b.Store.HasActivePending(chatID, user.Id)
	if err != nil {
		log.Printf("[bot] store.HasActivePending error: %v", err)
		return nil
	}
	if hasPending {
		// Active window exists, silently delete without new reminder
		deleteMessageIfExists(bot, chatID, msg.MessageId, "extra pending user message")
		if err := b.deletePendingOriginalMessage(bot, chatID, user.Id, false); err != nil {
			log.Printf("[bot] delete pending original message during active window error: %v", err)
		}
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
		deleteMessageIfExists(bot, chatID, msg.MessageId, "unverified message before ban")
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
	maskedName := maskName(user.FirstName, userLanguage)
	reminderText := appendDetectedLanguageLine(
		tr(userLanguage, "reminder_text", user.Id, maskedName, warnCount+1, maxWarnings),
		userLanguage,
		userLanguage,
	)

	// Send reminder in the same comment thread
	sendOpts := &gotgbot.SendMessageOpts{
		ParseMode: "HTML",
		ReplyParameters: &gotgbot.ReplyParameters{
			MessageId:                msg.MessageId,
			AllowSendingWithoutReply: true,
		},
	}
	if msg.MessageThreadId != 0 {
		sendOpts.MessageThreadId = msg.MessageThreadId
	}

	reminderMsg, err := bot.SendMessage(chatID, reminderText, sendOpts)
	if err != nil {
		log.Printf("[bot] send reminder error: %v", err)
		deleteMessageIfExists(bot, chatID, msg.MessageId, "unverified message after reminder failure")
		return nil
	}

	// Record pending verification window
	verifyWindow := b.Config.Moderation.GetVerifyWindow()
	expireAt := time.Unix(timestamp, 0).UTC().Add(verifyWindow)
	if err := b.Store.SetPending(store.PendingVerification{
		ChatID:            chatID,
		UserID:            user.Id,
		UserLanguage:      userLanguage,
		Timestamp:         timestamp,
		RandomToken:       randomToken,
		ExpireAt:          expireAt,
		ReminderMessageID: reminderMsg.MessageId,
		OriginalMessageID: msg.MessageId,
		MessageThreadID:   msg.MessageThreadId,
		ReplyToMessageID:  msg.MessageId,
	}); err != nil {
		log.Printf("[bot] store.SetPending error: %v", err)
		deleteMessageIfExists(bot, chatID, reminderMsg.MessageId, "verification reminder after pending persist failure")
		deleteMessageIfExists(bot, chatID, msg.MessageId, "unverified message after pending persist failure")
		return nil
	}

	verificationStartURL := BuildVerificationStartURL(bot.Username, chatID, user.Id, reminderMsg.MessageId)
	if _, _, err := reminderMsg.EditReplyMarkup(bot, &gotgbot.EditMessageReplyMarkupOpts{
		ReplyMarkup: BuildReminderKeyboard(verificationStartURL, chatID, user.Id, userLanguage),
	}); err != nil {
		log.Printf("[bot] edit reminder message reply markup error: %v", err)
	}

	// Keep the reminder visible for the full verification window unless configured longer.
	reminderTTL := b.Config.Moderation.GetReminderTTL()
	scheduleMessageDeletion(bot, chatID, reminderMsg.MessageId, reminderTTL, "verification reminder")
	b.scheduleOriginalMessageDeletion(bot, chatID, user.Id)

	// Schedule verification window expiry check
	capturedUserID := user.Id
	capturedChatID := chatID
	time.AfterFunc(verifyWindow, func() {
		b.onVerifyWindowExpired(bot, capturedChatID, capturedUserID)
	})

	return nil
}

// onVerifyWindowExpired is called when the verification window expires.
func (b *Bot) onVerifyWindowExpired(bot *gotgbot.Bot, chatID, userID int64) {
	if err := b.deletePendingOriginalMessage(bot, chatID, userID, true); err != nil {
		log.Printf("[bot] delete pending original message on expiry error: %v", err)
	}

	// Clear the pending record
	if err := b.Store.ClearPending(chatID, userID); err != nil {
		log.Printf("[bot] store.ClearPending error in expiry: %v", err)
	}

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

func (b *Bot) scheduleOriginalMessageDeletion(bot *gotgbot.Bot, chatID, userID int64) {
	originalMessageTTL := b.Config.Moderation.GetOriginalMessageTTL()
	if originalMessageTTL <= 0 {
		return
	}

	time.AfterFunc(originalMessageTTL, func() {
		if err := b.deletePendingOriginalMessage(bot, chatID, userID, false); err != nil {
			log.Printf("[bot] delete pending original message after ttl error: %v", err)
		}
	})
}

func (b *Bot) deletePendingOriginalMessage(bot *gotgbot.Bot, chatID, userID int64, force bool) error {
	verified, err := b.Store.IsVerified(chatID, userID)
	if err != nil {
		return fmt.Errorf("check verified status before deleting original message: %w", err)
	}
	if verified {
		return nil
	}

	pending, err := b.Store.GetPending(chatID, userID)
	if err != nil {
		return fmt.Errorf("load pending verification before deleting original message: %w", err)
	}
	if pending == nil || pending.OriginalMessageID == 0 {
		return nil
	}

	if !force {
		deleteAt := time.Unix(pending.Timestamp, 0).UTC().Add(b.Config.Moderation.GetOriginalMessageTTL())
		if time.Now().UTC().Before(deleteAt) {
			return nil
		}
	}

	deleteMessageIfExists(bot, chatID, pending.OriginalMessageID, "pending original")
	pending.OriginalMessageID = 0
	if err := b.Store.SetPending(*pending); err != nil {
		return fmt.Errorf("persist original message deletion state: %w", err)
	}

	return nil
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
func maskName(name, locale string) string {
	if name == "" {
		return tr(locale, "user_name_fallback")
	}
	r, _ := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return tr(locale, "user_name_fallback")
	}
	return string(r) + "**"
}
