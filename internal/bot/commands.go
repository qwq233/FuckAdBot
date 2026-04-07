package bot

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

// isBotAdmin checks if the user is a bot-level admin (from config).
func (b *Bot) isBotAdmin(userID int64) bool {
	for _, id := range b.Config.Bot.Admins {
		if id == userID {
			return true
		}
	}
	return false
}

// isGroupAdmin checks if the user is a group admin/creator via Telegram API.
func (b *Bot) isGroupAdmin(bot *gotgbot.Bot, chatID, userID int64) bool {
	member, err := bot.GetChatMember(chatID, userID, nil)
	if err != nil {
		return false
	}
	status := member.MergeChatMember().Status
	return status == "administrator" || status == "creator"
}

// isAdmin checks if the user is a bot-level admin or a group admin/creator.
func (b *Bot) isAdmin(bot *gotgbot.Bot, chatID, userID int64) bool {
	return b.isBotAdmin(userID) || b.isGroupAdmin(bot, chatID, userID)
}

func isAnonymousGroupAdminMessage(msg *gotgbot.Message) bool {
	if msg == nil || msg.SenderChat == nil {
		return false
	}

	if msg.Chat.Type != "group" && msg.Chat.Type != "supergroup" {
		return false
	}

	return msg.SenderChat.Id == msg.Chat.Id
}

func (b *Bot) canApproveFromMessage(bot *gotgbot.Bot, msg *gotgbot.Message) bool {
	if msg == nil {
		return false
	}

	if msg.From != nil && b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return true
	}

	return isAnonymousGroupAdminMessage(msg)
}

func commandActorLabel(msg *gotgbot.Message) string {
	if msg == nil {
		return "unknown"
	}

	if msg.From != nil {
		return strconv.FormatInt(msg.From.Id, 10)
	}

	if isAnonymousGroupAdminMessage(msg) {
		return "anonymous-admin"
	}

	return "unknown"
}

// extractTargetUserID gets a user ID from command args or reply_to_message.
func extractTargetUserID(locale string, msg *gotgbot.Message, args []string) (int64, error) {
	if len(args) > 0 {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return 0, errors.New(tr(locale, "invalid_user_id", args[0]))
		}
		return id, nil
	}

	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		return msg.ReplyToMessage.From.Id, nil
	}

	return 0, errors.New(tr(locale, "target_user_required"))
}

func (b *Bot) cmdAddBlocklist(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.From == nil {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(msg.From)

	isPrivate := msg.Chat.Type == "private"
	if isPrivate {
		if !b.isBotAdmin(msg.From.Id) {
			return nil
		}
	} else {
		if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
			return nil
		}
	}

	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "usage_addblocklist"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	word := strings.TrimSpace(strings.Join(args[1:], " "))
	if word == "" {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "keyword_empty"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	var scopeChatID int64
	var scopeLabel string
	if isPrivate {
		scopeChatID = 0
		scopeLabel = tr(requestLanguage, "scope_global")
		b.Blacklist.Add(word)
	} else {
		scopeChatID = msg.Chat.Id
		scopeLabel = tr(requestLanguage, "scope_group")
		b.Blacklist.AddGroup(scopeChatID, word)
	}

	if err := b.Store.AddBlacklistWord(scopeChatID, word, strconv.FormatInt(msg.From.Id, 10)); err != nil {
		log.Printf("[bot] store.AddBlacklistWord error: %v", err)
		if isPrivate {
			b.Blacklist.Remove(word)
		} else {
			b.Blacklist.RemoveGroup(scopeChatID, word)
		}
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "blacklist_persist_failed"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	reply := tr(requestLanguage, "blacklist_added", scopeLabel, escapeHTML(word))
	bot.SendMessage(msg.Chat.Id, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})

	return nil
}

func (b *Bot) cmdDelBlocklist(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.From == nil {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(msg.From)

	isPrivate := msg.Chat.Type == "private"
	if isPrivate {
		if !b.isBotAdmin(msg.From.Id) {
			return nil
		}
	} else {
		if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
			return nil
		}
	}

	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "usage_delblocklist"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	word := strings.TrimSpace(strings.Join(args[1:], " "))
	if word == "" {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "keyword_empty"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	var scopeChatID int64
	var scopeLabel string
	if isPrivate {
		scopeChatID = 0
		scopeLabel = tr(requestLanguage, "scope_global")
		if !b.Blacklist.Remove(word) {
			bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "blacklist_not_found_global"), &gotgbot.SendMessageOpts{
				MessageThreadId: msg.MessageThreadId,
			})
			return nil
		}
	} else {
		scopeChatID = msg.Chat.Id
		scopeLabel = tr(requestLanguage, "scope_group")
		if !b.Blacklist.RemoveGroup(scopeChatID, word) {
			bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "blacklist_not_found_group"), &gotgbot.SendMessageOpts{
				MessageThreadId: msg.MessageThreadId,
			})
			return nil
		}
	}

	if err := b.Store.RemoveBlacklistWord(scopeChatID, word); err != nil {
		log.Printf("[bot] store.RemoveBlacklistWord error: %v", err)
		if isPrivate {
			b.Blacklist.Add(word)
		} else {
			b.Blacklist.AddGroup(scopeChatID, word)
		}
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "blacklist_delete_failed"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	reply := tr(requestLanguage, "blacklist_removed", scopeLabel, escapeHTML(word))
	bot.SendMessage(msg.Chat.Id, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})

	return nil
}

func (b *Bot) cmdListBlocklist(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.From == nil {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(msg.From)

	isPrivate := msg.Chat.Type == "private"
	if isPrivate {
		if !b.isBotAdmin(msg.From.Id) {
			return nil
		}
	} else {
		if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
			return nil
		}
	}

	var words []string
	var title string
	if isPrivate {
		words = b.Blacklist.List()
		title = tr(requestLanguage, "blacklist_list_title_global")
	} else {
		words = b.Blacklist.ListGroup(msg.Chat.Id)
		title = tr(requestLanguage, "blacklist_list_title_group")
	}

	if len(words) == 0 {
		if isPrivate {
			bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "blacklist_empty_global"), &gotgbot.SendMessageOpts{
				MessageThreadId: msg.MessageThreadId,
			})
		} else {
			bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "blacklist_empty_group"), &gotgbot.SendMessageOpts{
				MessageThreadId: msg.MessageThreadId,
			})
		}
		return nil
	}

	var sb strings.Builder
	sb.WriteString(title)
	for i, w := range words {
		sb.WriteString(fmt.Sprintf("%d. <code>%s</code>\n", i+1, escapeHTML(w)))
	}

	bot.SendMessage(msg.Chat.Id, sb.String(), &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})

	return nil
}

func (b *Bot) cmdApprove(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(msg.From)

	if !b.canApproveFromMessage(bot, msg) {
		return nil
	}

	args := strings.Fields(msg.Text)
	userID, err := extractTargetUserID(requestLanguage, msg, args[1:])
	if err != nil {
		bot.SendMessage(msg.Chat.Id, err.Error(), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	chatID := msg.Chat.Id
	targetLanguage := b.targetUserLanguage(chatID, userID)

	if err := b.approveUser(chatID, userID); err != nil {
		log.Printf("[bot] approveUser error: %v", err)
		bot.SendMessage(chatID, tr(requestLanguage, "approve_failed"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}
	log.Printf("[bot] manual approve via command: admin=%s target=%d chat=%d", commandActorLabel(msg), userID, chatID)

	reply := appendDetectedLanguageLine(tr(requestLanguage, "approve_result", userID), targetLanguage, requestLanguage)
	resp, err := bot.SendMessage(chatID, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})
	if err != nil {
		log.Printf("[bot] send approve confirmation error: %v", err)
		return nil
	}
	scheduleMessageDeletion(bot, chatID, resp.MessageId, manualModerationResultTTL, "approve confirmation")

	return nil
}

func (b *Bot) cmdResetAllVerify(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || msg.From == nil || !b.isBotAdmin(msg.From.Id) {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(msg.From)

	args := strings.Fields(msg.Text)
	userID, err := extractTargetUserID(requestLanguage, msg, args[1:])
	if err != nil {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "resetverify_usage"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	if err := b.Store.ClearUserVerificationStateEverywhere(userID); err != nil {
		log.Printf("[bot] ClearUserVerificationStateEverywhere error: %v", err)
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "resetverify_failed"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	log.Printf("[bot] reset all verification state via command: admin=%d target=%d", msg.From.Id, userID)
	bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "resetverify_success", userID), &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})

	return nil
}

func (b *Bot) cmdReject(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.From == nil {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(msg.From)

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	args := strings.Fields(msg.Text)
	userID, err := extractTargetUserID(requestLanguage, msg, args[1:])
	if err != nil {
		bot.SendMessage(msg.Chat.Id, err.Error(), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	chatID := msg.Chat.Id
	targetLanguage := b.targetUserLanguage(chatID, userID)

	if err := b.rejectUser(chatID, userID); err != nil {
		log.Printf("[bot] rejectUser error: %v", err)
		bot.SendMessage(chatID, tr(requestLanguage, "reject_failed"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}
	log.Printf("[bot] manual reject via command: admin=%d target=%d chat=%d", msg.From.Id, userID, chatID)

	reply := appendDetectedLanguageLine(tr(requestLanguage, "reject_result", userID), targetLanguage, requestLanguage)
	resp, err := bot.SendMessage(chatID, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})
	if err != nil {
		log.Printf("[bot] send reject confirmation error: %v", err)
		return nil
	}
	scheduleMessageDeletion(bot, chatID, resp.MessageId, manualModerationResultTTL, "reject confirmation")

	return nil
}

func (b *Bot) cmdUnreject(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.From == nil {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(msg.From)

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	args := strings.Fields(msg.Text)
	userID, err := extractTargetUserID(requestLanguage, msg, args[1:])
	if err != nil {
		bot.SendMessage(msg.Chat.Id, err.Error(), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	chatID := msg.Chat.Id
	targetLanguage := b.targetUserLanguage(chatID, userID)

	if err := b.unrejectUser(chatID, userID); err != nil {
		log.Printf("[bot] unrejectUser error: %v", err)
		bot.SendMessage(chatID, tr(requestLanguage, "unreject_failed"), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	reply := appendDetectedLanguageLine(tr(requestLanguage, "unreject_result", userID), targetLanguage, requestLanguage)
	bot.SendMessage(chatID, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})

	return nil
}

func (b *Bot) cmdStats(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.From == nil {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(msg.From)

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	words := b.Blacklist.List()
	var reply string
	if msg.Chat.Type == "private" {
		reply = tr(requestLanguage, "stats_private", len(words))
	} else {
		groupWords := b.Blacklist.ListGroup(msg.Chat.Id)
		reply = tr(requestLanguage, "stats_group", len(words), len(groupWords))
	}
	bot.SendMessage(msg.Chat.Id, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})

	return nil
}

func (b *Bot) sendLanguagePreferencePrompt(bot *gotgbot.Bot, chatID int64, viewerLanguage string, notice string) {
	text := tr(viewerLanguage, "lang_prompt")
	if notice != "" {
		text = notice + "\n\n" + text
	}

	bot.SendMessage(chatID, text, &gotgbot.SendMessageOpts{
		ReplyMarkup: BuildLanguagePreferenceKeyboard(viewerLanguage),
	})
}

func (b *Bot) cmdLang(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || msg.Chat.Type != "private" || msg.From == nil {
		return nil
	}

	requestLanguage := b.requestLanguageForUser(msg.From)
	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		b.sendLanguagePreferencePrompt(bot, msg.Chat.Id, requestLanguage, "")
		return nil
	}

	selectedLanguage, changed, err := b.applyUserLanguagePreference(msg.From.Id, args[1])
	if err != nil {
		log.Printf("[bot] store.SetUserLanguagePreference error: %v", err)
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "lang_update_failed"), nil)
		return nil
	}
	if !changed {
		b.sendLanguagePreferencePrompt(bot, msg.Chat.Id, requestLanguage, tr(requestLanguage, "lang_invalid"))
		return nil
	}

	bot.SendMessage(msg.Chat.Id, tr(selectedLanguage, "lang_updated", localizedLanguageName(selectedLanguage, selectedLanguage)), nil)
	return nil
}

func (b *Bot) cmdStart(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || msg.Chat.Type != "private" {
		return nil
	}
	requestLanguage := defaultUserLanguage
	if msg.From != nil {
		requestLanguage = b.requestLanguageForUser(msg.From)
	}

	args := ctx.Args()
	if len(args) > 1 && strings.HasPrefix(args[1], verificationStartPayloadPrefix+"_") {
		return b.handleVerificationStart(bot, msg, args[1])
	}

	bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "start_help"), &gotgbot.SendMessageOpts{ParseMode: "HTML"})

	return nil
}

func (b *Bot) handleVerificationStart(bot *gotgbot.Bot, msg *gotgbot.Message, payload string) error {
	requestLanguage := defaultUserLanguage
	if msg.From != nil {
		requestLanguage = b.requestLanguageForUser(msg.From)
	}

	chatID, userID, verificationInfoID, err := ParseVerificationStartPayload(payload)
	if err != nil {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "verify_params_invalid"), nil)
		return nil
	}

	if msg.From == nil || msg.From.Id != userID {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "verify_link_not_yours"), nil)
		return nil
	}

	pending, err := b.Store.GetPending(chatID, userID)
	if err != nil {
		log.Printf("[bot] store.GetPending error in /start: %v", err)
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "verify_state_read_failed"), nil)
		return nil
	}
	if pending == nil || pending.ReminderMessageID != verificationInfoID || !pending.ExpireAt.After(time.Now().UTC()) {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "verify_link_expired"), nil)
		return nil
	}
	requestLanguage = b.requestLanguageForUser(msg.From)

	checkText := buildCheckText(msg.From)
	if chat := b.cachedUserChat(userID, func(userID int64) (*gotgbot.ChatFullInfo, error) {
		return bot.GetChat(userID, nil)
	}); chat != nil && chat.Bio != "" {
		checkText += " " + chat.Bio
	}

	if matched := b.Blacklist.MatchWithGroup(chatID, checkText); matched != "" {
		log.Printf("[bot] blacklist hit during /start verification: user=%d word=%q in chat=%d", userID, matched, chatID)
		if err := b.Store.ClearPending(chatID, userID); err != nil {
			log.Printf("[bot] store.ClearPending error after blacklist hit in /start: %v", err)
		}
		if pending.OriginalMessageID != 0 {
			deleteMessageIfExists(bot, chatID, pending.OriginalMessageID, "pending original after /start blacklist hit")
		}
		if pending.ReminderMessageID != 0 {
			deleteMessageIfExists(bot, chatID, pending.ReminderMessageID, "verification reminder after /start blacklist hit")
		}
		if _, err := bot.BanChatMember(chatID, userID, &gotgbot.BanChatMemberOpts{}); err != nil {
			log.Printf("[bot] ban user after /start blacklist hit error: %v", err)
		}
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "verify_blacklist_hit", escapeHTML(matched)), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return nil
	}

	if b.Captcha == nil {
		bot.SendMessage(msg.Chat.Id, tr(requestLanguage, "verify_service_disabled"), nil)
		return nil
	}

	verifyURL := b.Captcha.GenerateVerifyURL(chatID, userID, pending.Timestamp, pending.RandomToken)
	if pending.PrivateMessageID != 0 {
		if _, err := bot.DeleteMessage(msg.Chat.Id, pending.PrivateMessageID, nil); err != nil {
			log.Printf("[bot] delete previous private verification message error: %v", err)
		}
	}

	privateVerificationMsg, err := bot.SendMessage(msg.Chat.Id,
		tr(requestLanguage, "private_verification_prompt"),
		&gotgbot.SendMessageOpts{
			ReplyMarkup: gotgbot.InlineKeyboardMarkup{
				InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
					{Text: tr(requestLanguage, "verify_button_open"), Url: verifyURL},
				}},
			},
		},
	)
	if err != nil {
		log.Printf("[bot] send private verification link error: %v", err)
		return nil
	}

	pending.PrivateMessageID = privateVerificationMsg.MessageId
	pending.UserLanguage = requestLanguage
	if err := b.Store.SetPending(*pending); err != nil {
		log.Printf("[bot] store.SetPending error after private verification message: %v", err)
	}

	return nil
}

// escapeHTML escapes HTML special characters for Telegram HTML mode.
func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func (b *Bot) approveUser(chatID, userID int64) error {
	return errors.Join(
		b.Store.SetVerified(chatID, userID),
		b.Store.ClearPending(chatID, userID),
		b.Store.ResetWarningCount(chatID, userID),
		b.Store.RemoveRejected(chatID, userID),
	)
}

func (b *Bot) rejectUser(chatID, userID int64) error {
	pending, err := b.Store.GetPending(chatID, userID)
	if err != nil {
		return err
	}

	if pending != nil {
		if pending.OriginalMessageID != 0 {
			deleteMessageIfExists(b.Bot, chatID, pending.OriginalMessageID, "pending original after reject")
		}
		if pending.PrivateMessageID != 0 {
			deleteMessageIfExists(b.Bot, userID, pending.PrivateMessageID, "private verification message after reject")
		}
	}

	return errors.Join(
		b.Store.SetRejected(chatID, userID),
		b.Store.ClearPending(chatID, userID),
		b.Store.ResetWarningCount(chatID, userID),
		b.Store.RemoveVerified(chatID, userID),
	)
}

func (b *Bot) unrejectUser(chatID, userID int64) error {
	return errors.Join(
		b.Store.RemoveRejected(chatID, userID),
		b.Store.ResetWarningCount(chatID, userID),
	)
}
