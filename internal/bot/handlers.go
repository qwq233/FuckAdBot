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
	incoming, ok := b.moderatedMessageFromMessage(ctx.EffectiveMessage)
	if !ok {
		return nil
	}

	if b.handleImmediateModeration(bot, incoming) {
		return nil
	}

	b.handleVerificationRequiredMessage(bot, incoming)
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
