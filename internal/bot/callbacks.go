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

// BuildReminderKeyboard builds the inline keyboard for a verification reminder.
func BuildReminderKeyboard(verifyURL string, chatID, userID int64) gotgbot.InlineKeyboardMarkup {
	return gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{{Text: "🛡️ 点击验证", Url: verifyURL}},
			{{
				Text:         "✅ 批准",
				CallbackData: BuildModerationCallbackData("a", chatID, userID),
			}, {
				Text:         "🚫 拒绝",
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

func (b *Bot) handleModerationCallback(bot *gotgbot.Bot, ctx *ext.Context) error {
	cq := ctx.CallbackQuery
	if cq == nil {
		return nil
	}

	action, chatID, userID, err := ParseModerationCallbackData(cq.Data)
	if err != nil {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      "无效的审批按钮",
			ShowAlert: true,
		})
		return nil
	}

	if !b.isAdmin(bot, chatID, cq.From.Id) {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      "只有管理员可以操作这个按钮",
			ShowAlert: true,
		})
		return nil
	}

	if cq.Message != nil && cq.Message.GetChat().Id != chatID {
		_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
			Text:      "按钮所属聊天与目标不匹配",
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
				Text:      "批准失败，请稍后重试",
				ShowAlert: true,
			})
			return nil
		}
		messageText = fmt.Sprintf("✅ 已由管理员批准用户 <code>%d</code> 的验证", userID)
		answerText = "已批准"
	case "r":
		if err := b.rejectUser(chatID, userID); err != nil {
			log.Printf("[bot] rejectUser callback error: %v", err)
			_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{
				Text:      "拒绝失败，请稍后重试",
				ShowAlert: true,
			})
			return nil
		}
		messageText = fmt.Sprintf("🚫 已由管理员拒绝用户 <code>%d</code> 的验证，其消息将被静默删除", userID)
		answerText = "已拒绝"
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
		}
	}

	_, _ = cq.Answer(bot, &gotgbot.AnswerCallbackQueryOpts{Text: answerText})
	return nil
}
