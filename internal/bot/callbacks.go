package bot

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

const moderationCallbackPrefix = "review:"
const languagePreferenceCallbackPrefix = "lang:"

// BuildReminderKeyboard builds the inline keyboard for a verification reminder.
func BuildReminderKeyboard(verifyURL string, chatID, userID int64, userLanguage string) gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{{Text: tr(userLanguage, "verify_button_click"), Url: verifyURL}},
			{{
				Text:         tr(userLanguage, "admin_button_approve"),
				CallbackData: BuildModerationCallbackData("a", chatID, userID),
			}, {
				Text:         tr(userLanguage, "admin_button_reject"),
				CallbackData: BuildModerationCallbackData("r", chatID, userID),
			}},
		},
	}
}

// BuildModerationCallbackData encodes moderation action metadata into callback data.
func BuildModerationCallbackData(action string, chatID, userID int64) string {
	return fmt.Sprintf("%s%s:%d:%d", moderationCallbackPrefix, action, chatID, userID)
}

// ParseModerationCallbackData decodes moderation callback data into action and target IDs.
func ParseModerationCallbackData(data string) (string, int64, int64, error) {
	if !strings.HasPrefix(data, moderationCallbackPrefix) {
		return "", 0, 0, fmt.Errorf("invalid moderation callback prefix")
	}

	parts := strings.Split(strings.TrimPrefix(data, moderationCallbackPrefix), ":")
	if len(parts) != 3 {
		return "", 0, 0, fmt.Errorf("invalid moderation callback format")
	}

	action := parts[0]
	if action != "a" && action != "r" {
		return "", 0, 0, fmt.Errorf("invalid moderation action")
	}

	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid chat id: %w", err)
	}

	userID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid user id: %w", err)
	}

	return action, chatID, userID, nil
}

func BuildLanguagePreferenceKeyboard(viewerLanguage string) gotgbot.InlineKeyboardMarkup {
	buttons := make([]gotgbot.InlineKeyboardButton, 0, len(supportedUserLanguages))
	for _, language := range supportedUserLanguages {
		buttons = append(buttons, gotgbot.InlineKeyboardButton{
			Text:         localizedLanguageName(language, viewerLanguage),
			CallbackData: BuildLanguagePreferenceCallbackData(language),
		})
	}

	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{buttons},
	}
}

func BuildLanguagePreferenceCallbackData(language string) string {
	return languagePreferenceCallbackPrefix + normalizeUserLanguage(language)
}

func ParseLanguagePreferenceCallbackData(data string) (string, error) {
	if !strings.HasPrefix(data, languagePreferenceCallbackPrefix) {
		return "", fmt.Errorf("invalid language callback prefix")
	}

	language, ok := resolveSupportedUserLanguage(strings.TrimPrefix(data, languagePreferenceCallbackPrefix))
	if !ok {
		return "", fmt.Errorf("invalid language callback value")
	}

	return language, nil
}

func (b *Bot) handleModerationCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	cq := ctx.CallbackQuery
	if cq == nil {
		return nil
	}
	requestLanguage := b.requestLanguageForUser(&cq.From)

	action, chatID, userID, err := ParseModerationCallbackData(cq.Data)
	if err != nil {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      tr(requestLanguage, "invalid_review_button"),
			ShowAlert: true,
		})
		return nil
	}
	targetLanguage := b.targetUserLanguage(chatID, userID)

	if !b.isAdmin(bot, chatID, cq.From.Id) {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      tr(requestLanguage, "admin_only_button"),
			ShowAlert: true,
		})
		return nil
	}

	if cq.Message != nil && cq.Message.GetChat().Id != chatID {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      tr(requestLanguage, "button_chat_mismatch"),
			ShowAlert: true,
		})
		return nil
	}

	var (
		messageText string
		answerText  string
	)

	switch action {
	case "a":
		if err := b.approveUser(chatID, userID); err != nil {
			log.Printf("[bot] approveUser callback error: %v", err)
			_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
				Text:      tr(requestLanguage, "callback_approve_failed"),
				ShowAlert: true,
			})
			return nil
		}
		log.Printf("[bot] manual approve via callback: admin=%d target=%d chat=%d", cq.From.Id, userID, chatID)
		messageText = appendDetectedLanguageLine(tr(targetLanguage, "callback_approve_result", userID), targetLanguage, targetLanguage)
		answerText = tr(requestLanguage, "callback_answer_approved")
	case "r":
		if err := b.rejectUser(chatID, userID); err != nil {
			log.Printf("[bot] rejectUser callback error: %v", err)
			_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
				Text:      tr(requestLanguage, "callback_reject_failed"),
				ShowAlert: true,
			})
			return nil
		}
		log.Printf("[bot] manual reject via callback: admin=%d target=%d chat=%d", cq.From.Id, userID, chatID)
		messageText = appendDetectedLanguageLine(tr(targetLanguage, "callback_reject_result", userID), targetLanguage, targetLanguage)
		answerText = tr(requestLanguage, "callback_answer_rejected")
	}

	if cq.Message != nil {
		_, _, err = cq.Message.EditText(bot, messageText, &gotgbot.EditMessageTextOpts{
			ParseMode: "HTML",
			ReplyMarkup: gotgbot.InlineKeyboardMarkup{
				InlineKeyboard: [][]gotgbot.InlineKeyboardButton{},
			},
		})
		if err != nil {
			log.Printf("[bot] edit reminder message after callback error: %v", err)
		} else {
			scheduleMessageDeletion(bot, cq.Message.GetChat().Id, cq.Message.GetMessageId(), manualModerationResultTTL, "manual moderation result")
		}
	}

	_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: answerText})
	return nil
}

func (b *Bot) handleLanguagePreferenceCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	cq := ctx.CallbackQuery
	if cq == nil {
		return nil
	}

	requestLanguage := b.requestLanguageForUser(&cq.From)
	if cq.Message == nil || cq.Message.GetChat().Type != "private" || cq.Message.GetChat().Id != cq.From.Id {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      tr(requestLanguage, "lang_private_only"),
			ShowAlert: true,
		})
		return nil
	}

	language, err := ParseLanguagePreferenceCallbackData(cq.Data)
	if err != nil {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      tr(requestLanguage, "lang_invalid"),
			ShowAlert: true,
		})
		return nil
	}

	selectedLanguage, changed, err := b.applyUserLanguagePreference(cq.From.Id, language)
	if err != nil {
		log.Printf("[bot] store.SetUserLanguagePreference error: %v", err)
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      tr(requestLanguage, "lang_update_failed"),
			ShowAlert: true,
		})
		return nil
	}
	if !changed {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      tr(requestLanguage, "lang_invalid"),
			ShowAlert: true,
		})
		return nil
	}

	updatedText := tr(selectedLanguage, "lang_updated", localizedLanguageName(selectedLanguage, selectedLanguage))
	if cq.Message != nil {
		_, _, err = cq.Message.EditText(bot, updatedText, &gotgbot.EditMessageTextOpts{
			ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}},
		})
		if err != nil {
			log.Printf("[bot] edit language preference message error: %v", err)
		}
	}

	_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: tr(selectedLanguage, "lang_callback_updated")})
	return nil
}
