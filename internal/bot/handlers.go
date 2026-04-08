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
	if matched := b.matchUserAgainstBlacklist(bot, chatID, user); matched != "" {
		// Don't ban group admins
		if b.isGroupAdmin(bot, chatID, user.Id) {
			return nil
		}
		log.Printf("[bot] blacklist hit: user=%d word=%q in chat=%d", user.Id, matched, chatID)
		deleteMessageIfExists(bot, chatID, msg.MessageId, "blacklist hit")
		banChatMember(bot, chatID, user.Id, "blacklist hit")
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

	verifyWindow := b.Config.Moderation.GetVerifyWindow()
	maxWarnings := b.Config.Moderation.MaxWarnings

	for attempt := 0; attempt < 3; attempt++ {
		warnCount, err := b.Store.GetWarningCount(chatID, user.Id)
		if err != nil {
			log.Printf("[bot] store.GetWarningCount error: %v", err)
			return nil
		}

		if warnCount >= maxWarnings {
			log.Printf("[bot] banning user=%d in chat=%d: exceeded max warnings (%d)", user.Id, chatID, maxWarnings)
			deleteMessageIfExists(bot, chatID, msg.MessageId, "unverified message before ban")
			banChatMember(bot, chatID, user.Id, "warning limit exceeded")
			return nil
		}

		timestamp := time.Now().UTC().Unix()
		randomToken, err := newVerificationRandomToken(7)
		if err != nil {
			log.Printf("[bot] generate verification random token error: %v", err)
			return nil
		}

		pending := store.PendingVerification{
			ChatID:            chatID,
			UserID:            user.Id,
			UserLanguage:      userLanguage,
			Timestamp:         timestamp,
			RandomToken:       randomToken,
			ExpireAt:          time.Unix(timestamp, 0).UTC().Add(verifyWindow),
			OriginalMessageID: msg.MessageId,
			MessageThreadID:   msg.MessageThreadId,
			ReplyToMessageID:  msg.MessageId,
		}

		created, existing, err := b.Store.CreatePendingIfAbsent(pending)
		if err != nil {
			log.Printf("[bot] store.CreatePendingIfAbsent error: %v", err)
			return nil
		}
		if !created {
			if existing == nil {
				verified, err := b.Store.IsVerified(chatID, user.Id)
				if err == nil && verified {
					return nil
				}

				rejected, err := b.Store.IsRejected(chatID, user.Id)
				if err == nil && rejected {
					deleteMessageIfExists(bot, chatID, msg.MessageId, "rejected user message after pending race")
					return nil
				}
				continue
			}

			if existing.ExpireAt.After(time.Now().UTC()) {
				deleteMessageIfExists(bot, chatID, msg.MessageId, "extra pending user message")
				if err := b.deletePendingOriginalMessage(bot, existing, false); err != nil {
					log.Printf("[bot] delete pending original message during active window error: %v", err)
				}
				return nil
			}

			expired, err := b.Store.ResolvePendingByToken(existing.ChatID, existing.UserID, existing.Timestamp, existing.RandomToken, store.PendingActionExpire, maxWarnings)
			if err != nil {
				log.Printf("[bot] resolve expired pending during message handling error: %v", err)
				return nil
			}
			if !expired.Matched {
				continue
			}
			if expired.Pending != nil {
				if err := b.deletePendingOriginalMessage(bot, expired.Pending, true); err != nil {
					log.Printf("[bot] delete pending original message after stale expiry error: %v", err)
				}
			}
			if expired.ShouldBan {
				log.Printf("[bot] auto-banning user=%d in chat=%d: %d warnings", user.Id, chatID, expired.WarningCount)
				deleteMessageIfExists(bot, chatID, msg.MessageId, "unverified message after stale expiry")
				banChatMember(bot, chatID, user.Id, "expired pending")
				return nil
			}
			if expired.Verified {
				return nil
			}
			if expired.Rejected {
				deleteMessageIfExists(bot, chatID, msg.MessageId, "rejected user message after stale expiry")
				return nil
			}
			continue
		}

		maskedName := maskName(user.FirstName, userLanguage)
		reminderText := appendDetectedLanguageLine(
			tr(userLanguage, "reminder_text", user.Id, maskedName, warnCount+1, maxWarnings),
			userLanguage,
			userLanguage,
		)

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

		reminderMsg, err := sendMessageWithLog(bot, chatID, reminderText, sendOpts, "verification reminder")
		if err != nil {
			if _, resolveErr := b.Store.ResolvePendingByToken(chatID, user.Id, pending.Timestamp, pending.RandomToken, store.PendingActionCancel, maxWarnings); resolveErr != nil {
				log.Printf("[bot] cancel pending after reminder send failure error: %v", resolveErr)
			}
			deleteMessageIfExists(bot, chatID, msg.MessageId, "unverified message after reminder failure")
			return nil
		}

		pending.ReminderMessageID = reminderMsg.MessageId
		updated, err := b.Store.UpdatePendingMetadataByToken(pending)
		if err != nil {
			log.Printf("[bot] store.UpdatePendingMetadataByToken error: %v", err)
			deleteMessageIfExists(bot, chatID, reminderMsg.MessageId, "verification reminder after pending update failure")
			if _, resolveErr := b.Store.ResolvePendingByToken(chatID, user.Id, pending.Timestamp, pending.RandomToken, store.PendingActionCancel, maxWarnings); resolveErr != nil {
				log.Printf("[bot] cancel pending after metadata update failure error: %v", resolveErr)
			}
			return nil
		}
		if !updated {
			deleteMessageIfExists(bot, chatID, reminderMsg.MessageId, "verification reminder after pending race")
			return nil
		}

		verificationStartURL := BuildVerificationStartURL(bot.Username, chatID, user.Id, reminderMsg.MessageId)
		editReplyMarkupWithLog(bot, reminderMsg, &gotgbot.EditMessageReplyMarkupOpts{
			ReplyMarkup: BuildReminderKeyboard(verificationStartURL, chatID, user.Id, userLanguage),
		}, "verification reminder")

		reminderTTL := b.Config.Moderation.GetReminderTTL()
		scheduleMessageDeletion(bot, chatID, reminderMsg.MessageId, reminderTTL, "verification reminder")
		b.scheduleOriginalMessageDeletion(bot, pending)
		b.scheduleUserTimer(chatID, user.Id, verifyWindow, func() {
			b.onVerifyWindowExpired(bot, pending)
		})
		return nil
	}

	log.Printf("[bot] failed to reserve verification window after retries: chat=%d user=%d", chatID, user.Id)
	return nil
}

// onVerifyWindowExpired is called when the verification window expires.
func (b *Bot) onVerifyWindowExpired(bot *gotgbot.Bot, pending store.PendingVerification) {
	result, err := b.Store.ResolvePendingByToken(
		pending.ChatID,
		pending.UserID,
		pending.Timestamp,
		pending.RandomToken,
		store.PendingActionExpire,
		b.Config.Moderation.MaxWarnings,
	)
	if err != nil {
		log.Printf("[bot] resolve pending expiry error: %v", err)
		return
	}
	if !result.Matched {
		return
	}

	if result.Pending != nil {
		if err := b.deletePendingOriginalMessage(bot, result.Pending, true); err != nil {
			log.Printf("[bot] delete pending original message on expiry error: %v", err)
		}
	}

	if result.Verified || result.Rejected {
		return
	}

	log.Printf("[bot] verify window expired: user=%d chat=%d warnings=%d/%d",
		pending.UserID, pending.ChatID, result.WarningCount, b.Config.Moderation.MaxWarnings)

	if result.ShouldBan {
		log.Printf("[bot] auto-banning user=%d in chat=%d: %d warnings", pending.UserID, pending.ChatID, result.WarningCount)
		banChatMember(bot, pending.ChatID, pending.UserID, "expired verification window")
	}
}

func (b *Bot) scheduleOriginalMessageDeletion(bot *gotgbot.Bot, pending store.PendingVerification) {
	originalMessageTTL := b.Config.Moderation.GetOriginalMessageTTL()
	if originalMessageTTL <= 0 || pending.OriginalMessageID == 0 {
		return
	}

	deleteAt := time.Unix(pending.Timestamp, 0).UTC().Add(originalMessageTTL)
	delay := deleteAt.Sub(time.Now().UTC())
	if delay <= 0 {
		if err := b.deletePendingOriginalMessage(bot, &pending, false); err != nil {
			log.Printf("[bot] delete pending original message after ttl error: %v", err)
		}
		return
	}

	b.scheduleUserTimer(pending.ChatID, pending.UserID, delay, func() {
		if err := b.deletePendingOriginalMessage(bot, &pending, false); err != nil {
			log.Printf("[bot] delete pending original message after ttl error: %v", err)
		}
	})
}

func (b *Bot) pendingForOriginalMessageDeletion(pending *store.PendingVerification, force bool) (*store.PendingVerification, bool, error) {
	if pending == nil || pending.OriginalMessageID == 0 {
		return nil, false, nil
	}

	verified, err := b.Store.IsVerified(pending.ChatID, pending.UserID)
	if err != nil {
		return nil, false, fmt.Errorf("check verified status before deleting original message: %w", err)
	}
	if verified {
		return nil, false, nil
	}

	if force {
		return pending, false, nil
	}

	currentPending, err := b.Store.GetPending(pending.ChatID, pending.UserID)
	if err != nil {
		return nil, false, fmt.Errorf("read current pending before deleting original message: %w", err)
	}
	if currentPending == nil {
		return nil, false, nil
	}
	if currentPending.Timestamp != pending.Timestamp || currentPending.RandomToken != pending.RandomToken {
		return nil, false, nil
	}
	if currentPending.OriginalMessageID == 0 {
		return nil, false, nil
	}

	deleteAt := time.Unix(currentPending.Timestamp, 0).UTC().Add(b.Config.Moderation.GetOriginalMessageTTL())
	if time.Now().UTC().Before(deleteAt) {
		return nil, false, nil
	}

	return currentPending, true, nil
}

func (b *Bot) deletePendingOriginalMessage(bot *gotgbot.Bot, pending *store.PendingVerification, force bool) error {
	targetPending, shouldPersist, err := b.pendingForOriginalMessageDeletion(pending, force)
	if err != nil {
		return err
	}
	if targetPending == nil {
		return nil
	}

	deleteMessageIfExists(bot, targetPending.ChatID, targetPending.OriginalMessageID, "pending original")
	if !shouldPersist {
		return nil
	}

	targetPending.OriginalMessageID = 0
	updated, err := b.Store.UpdatePendingMetadataByToken(*targetPending)
	if err != nil {
		return fmt.Errorf("persist original message deletion state: %w", err)
	}
	if !updated {
		return nil
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

func (b *Bot) matchUserAgainstBlacklist(bot *gotgbot.Bot, chatID int64, user *gotgbot.User) string {
	checkText := buildCheckText(user)
	if matched := b.Blacklist.MatchWithGroup(chatID, checkText); matched != "" {
		return matched
	}

	if bioChat := b.cachedUserChat(user.Id, func(userID int64) (*gotgbot.ChatFullInfo, error) {
		return bot.GetChat(userID, nil)
	}); bioChat != nil && bioChat.Bio != "" {
		return b.Blacklist.MatchWithGroup(chatID, checkText+" "+bioChat.Bio)
	}

	return ""
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
