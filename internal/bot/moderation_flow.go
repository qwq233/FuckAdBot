package bot

import (
	"log"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/qwq233/fuckadbot/internal/store"
)

const verificationReservationAttempts = 3

type moderatedMessage struct {
	message      *gotgbot.Message
	user         *gotgbot.User
	chatID       int64
	userLanguage string
	verifyWindow time.Duration
	maxWarnings  int
}

type pendingReservationOutcome int

const (
	pendingReservationComplete pendingReservationOutcome = iota
	pendingReservationRetry
)

func (b *Bot) moderatedMessageFromMessage(msg *gotgbot.Message) (*moderatedMessage, bool) {
	if msg == nil || msg.From == nil {
		return nil, false
	}

	// Skip auto-forwarded channel posts and anonymous admin messages.
	if msg.IsAutomaticForward || msg.SenderChat != nil {
		return nil, false
	}

	return &moderatedMessage{
		message:      msg,
		user:         msg.From,
		chatID:       msg.Chat.Id,
		userLanguage: b.requestLanguageForUser(msg.From),
		verifyWindow: b.Config.Moderation.GetVerifyWindow(),
		maxWarnings:  b.Config.Moderation.MaxWarnings,
	}, true
}

func (b *Bot) handleImmediateModeration(bot *gotgbot.Bot, incoming *moderatedMessage) bool {
	return b.handleBotAdminBypass(incoming) ||
		b.handleBlacklistMatch(bot, incoming) ||
		b.handleVerifiedUser(incoming) ||
		b.handleGroupAdminAutoApproval(bot, incoming) ||
		b.handleRejectedUser(bot, incoming)
}

func (b *Bot) handleBotAdminBypass(incoming *moderatedMessage) bool {
	return b.isBotAdmin(incoming.user.Id)
}

func (b *Bot) handleBlacklistMatch(bot *gotgbot.Bot, incoming *moderatedMessage) bool {
	matched := b.matchUserAgainstBlacklist(bot, incoming.chatID, incoming.user)
	if matched == "" {
		return false
	}

	// Don't ban group admins.
	if b.isGroupAdmin(bot, incoming.chatID, incoming.user.Id) {
		return true
	}

	log.Printf("[bot] blacklist hit: user=%d word=%q in chat=%d", incoming.user.Id, matched, incoming.chatID)
	deleteMessageIfExists(bot, incoming.chatID, incoming.message.MessageId, "blacklist hit")
	banChatMember(bot, incoming.chatID, incoming.user.Id, "blacklist hit")
	return true
}

func (b *Bot) handleVerifiedUser(incoming *moderatedMessage) bool {
	verified, err := b.Store.IsVerified(incoming.chatID, incoming.user.Id)
	if err != nil {
		log.Printf("[bot] store.IsVerified error: %v", err)
		return true
	}

	return verified
}

func (b *Bot) handleGroupAdminAutoApproval(bot *gotgbot.Bot, incoming *moderatedMessage) bool {
	if !b.isGroupAdmin(bot, incoming.chatID, incoming.user.Id) {
		return false
	}

	if err := b.approveUser(incoming.chatID, incoming.user.Id); err != nil {
		log.Printf("[bot] auto-approve group admin error: %v", err)
	}
	return true
}

func (b *Bot) handleRejectedUser(bot *gotgbot.Bot, incoming *moderatedMessage) bool {
	rejected, err := b.Store.IsRejected(incoming.chatID, incoming.user.Id)
	if err != nil {
		log.Printf("[bot] store.IsRejected error: %v", err)
		return true
	}
	if !rejected {
		return false
	}

	deleteMessageIfExists(bot, incoming.chatID, incoming.message.MessageId, "rejected user message")
	return true
}

func (b *Bot) handleVerificationRequiredMessage(bot *gotgbot.Bot, incoming *moderatedMessage) {
	for attempt := 0; attempt < verificationReservationAttempts; attempt++ {
		pending, err := buildPendingVerification(incoming)
		if err != nil {
			log.Printf("[bot] generate verification random token error: %v", err)
			return
		}

		reservation, err := b.Store.ReserveVerificationWindow(pending, incoming.maxWarnings)
		if err != nil {
			log.Printf("[bot] store.ReserveVerificationWindow error: %v", err)
			return
		}
		if reservation.LimitExceeded {
			log.Printf("[bot] banning user=%d in chat=%d: exceeded max warnings (%d)", incoming.user.Id, incoming.chatID, incoming.maxWarnings)
			deleteMessageIfExists(bot, incoming.chatID, incoming.message.MessageId, "unverified message before ban")
			banChatMember(bot, incoming.chatID, incoming.user.Id, "warning limit exceeded")
			return
		}

		if outcome := b.handlePendingReservation(bot, incoming, pending, reservation.Created, reservation.Existing, reservation.WarningCount); outcome == pendingReservationRetry {
			continue
		}
		return
	}

	log.Printf("[bot] failed to reserve verification window after retries: chat=%d user=%d", incoming.chatID, incoming.user.Id)
}

func buildPendingVerification(incoming *moderatedMessage) (store.PendingVerification, error) {
	timestamp := time.Now().UTC().Unix()
	randomToken, err := newVerificationRandomToken(7)
	if err != nil {
		return store.PendingVerification{}, err
	}

	return store.PendingVerification{
		ChatID:            incoming.chatID,
		UserID:            incoming.user.Id,
		UserLanguage:      incoming.userLanguage,
		Timestamp:         timestamp,
		RandomToken:       randomToken,
		ExpireAt:          time.Unix(timestamp, 0).UTC().Add(incoming.verifyWindow),
		OriginalMessageID: incoming.message.MessageId,
		MessageThreadID:   incoming.message.MessageThreadId,
		ReplyToMessageID:  incoming.message.MessageId,
	}, nil
}

func (b *Bot) handlePendingReservation(bot *gotgbot.Bot, incoming *moderatedMessage, pending store.PendingVerification, created bool, existing *store.PendingVerification, warnCount int) pendingReservationOutcome {
	if !created {
		return b.handleExistingPendingReservation(bot, incoming, existing)
	}

	b.startVerificationReminder(bot, incoming, pending, warnCount)
	return pendingReservationComplete
}

func (b *Bot) handleExistingPendingReservation(bot *gotgbot.Bot, incoming *moderatedMessage, existing *store.PendingVerification) pendingReservationOutcome {
	if existing == nil {
		return b.handlePendingStateAfterCreateRace(bot, incoming)
	}

	if existing.ExpireAt.After(time.Now().UTC()) {
		deleteMessageIfExists(bot, incoming.chatID, incoming.message.MessageId, "extra pending user message")
		if err := b.deletePendingOriginalMessage(bot, existing, false); err != nil {
			log.Printf("[bot] delete pending original message during active window error: %v", err)
		}
		return pendingReservationComplete
	}

	return b.handleExpiredPendingWindow(bot, incoming, existing)
}

func (b *Bot) handlePendingStateAfterCreateRace(bot *gotgbot.Bot, incoming *moderatedMessage) pendingReservationOutcome {
	verified, err := b.Store.IsVerified(incoming.chatID, incoming.user.Id)
	if err == nil && verified {
		return pendingReservationComplete
	}

	rejected, err := b.Store.IsRejected(incoming.chatID, incoming.user.Id)
	if err == nil && rejected {
		deleteMessageIfExists(bot, incoming.chatID, incoming.message.MessageId, "rejected user message after pending race")
		return pendingReservationComplete
	}

	return pendingReservationRetry
}

func (b *Bot) handleExpiredPendingWindow(bot *gotgbot.Bot, incoming *moderatedMessage, existing *store.PendingVerification) pendingReservationOutcome {
	expired, err := b.Store.ResolvePendingByToken(existing.ChatID, existing.UserID, existing.Timestamp, existing.RandomToken, store.PendingActionExpire, incoming.maxWarnings)
	if err != nil {
		log.Printf("[bot] resolve expired pending during message handling error: %v", err)
		return pendingReservationComplete
	}
	if !expired.Matched {
		return pendingReservationRetry
	}

	if expired.Pending != nil {
		if err := b.deletePendingOriginalMessage(bot, expired.Pending, true); err != nil {
			log.Printf("[bot] delete pending original message after stale expiry error: %v", err)
		}
	}

	if expired.ShouldBan {
		log.Printf("[bot] auto-banning user=%d in chat=%d: %d warnings", incoming.user.Id, incoming.chatID, expired.WarningCount)
		deleteMessageIfExists(bot, incoming.chatID, incoming.message.MessageId, "unverified message after stale expiry")
		banChatMember(bot, incoming.chatID, incoming.user.Id, "expired pending")
		return pendingReservationComplete
	}
	if expired.Verified {
		return pendingReservationComplete
	}
	if expired.Rejected {
		deleteMessageIfExists(bot, incoming.chatID, incoming.message.MessageId, "rejected user message after stale expiry")
		return pendingReservationComplete
	}

	return pendingReservationRetry
}

func (b *Bot) startVerificationReminder(bot *gotgbot.Bot, incoming *moderatedMessage, pending store.PendingVerification, warnCount int) {
	reminderText := buildVerificationReminderText(incoming, warnCount)
	reminderMsg, ok := b.sendVerificationReminder(bot, incoming, pending, reminderText)
	if !ok {
		return
	}

	pending.ReminderMessageID = reminderMsg.MessageId
	if !b.persistVerificationReminder(bot, incoming, pending, reminderMsg) {
		return
	}

	b.activateVerificationReminder(bot, incoming, pending, reminderMsg)
}

func buildVerificationReminderText(incoming *moderatedMessage, warnCount int) string {
	maskedName := maskName(incoming.user.FirstName, incoming.userLanguage)
	return appendDetectedLanguageLine(
		tr(incoming.userLanguage, "reminder_text", incoming.user.Id, maskedName, warnCount+1, incoming.maxWarnings),
		incoming.userLanguage,
		incoming.userLanguage,
	)
}

func buildVerificationReminderSendOpts(msg *gotgbot.Message) *gotgbot.SendMessageOpts {
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
	return sendOpts
}

func (b *Bot) sendVerificationReminder(bot *gotgbot.Bot, incoming *moderatedMessage, pending store.PendingVerification, reminderText string) (*gotgbot.Message, bool) {
	reminderMsg, err := sendMessageWithLog(bot, incoming.chatID, reminderText, buildVerificationReminderSendOpts(incoming.message), "verification reminder")
	if err != nil {
		b.cancelPendingVerification(pending, incoming.maxWarnings, "reminder send failure")
		deleteMessageIfExists(bot, incoming.chatID, incoming.message.MessageId, "unverified message after reminder failure")
		return nil, false
	}

	return reminderMsg, true
}

func (b *Bot) persistVerificationReminder(bot *gotgbot.Bot, incoming *moderatedMessage, pending store.PendingVerification, reminderMsg *gotgbot.Message) bool {
	updated, err := b.Store.UpdatePendingMetadataByToken(pending)
	if err != nil {
		log.Printf("[bot] store.UpdatePendingMetadataByToken error: %v", err)
		deleteMessageIfExists(bot, incoming.chatID, reminderMsg.MessageId, "verification reminder after pending update failure")
		b.cancelPendingVerification(pending, incoming.maxWarnings, "metadata update failure")
		return false
	}
	if !updated {
		deleteMessageIfExists(bot, incoming.chatID, reminderMsg.MessageId, "verification reminder after pending race")
		return false
	}

	return true
}

func (b *Bot) activateVerificationReminder(bot *gotgbot.Bot, incoming *moderatedMessage, pending store.PendingVerification, reminderMsg *gotgbot.Message) {
	verificationStartURL := BuildVerificationStartURL(bot.Username, incoming.chatID, incoming.user.Id, reminderMsg.MessageId)
	editReplyMarkupWithLog(bot, reminderMsg, &gotgbot.EditMessageReplyMarkupOpts{
		ReplyMarkup: BuildReminderKeyboard(verificationStartURL, incoming.chatID, incoming.user.Id, incoming.userLanguage),
	}, "verification reminder")

	reminderTTL := b.Config.Moderation.GetReminderTTL()
	b.scheduleMessageDeletion(bot, incoming.chatID, reminderMsg.MessageId, reminderTTL, "verification reminder")
}

func (b *Bot) cancelPendingVerification(pending store.PendingVerification, maxWarnings int, reason string) {
	if _, err := b.Store.ResolvePendingByToken(pending.ChatID, pending.UserID, pending.Timestamp, pending.RandomToken, store.PendingActionCancel, maxWarnings); err != nil {
		log.Printf("[bot] cancel pending after %s error: %v", reason, err)
	}
}
