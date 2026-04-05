package bot

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

// isAdmin checks if the user is a bot-level admin (from config) or a group admin/creator.
func (b *Bot) isAdmin(bot *gotgbot.Bot, chatID, userID int64) bool {
	for _, id := range b.Config.Bot.Admins {
		if id == userID {
			return true
		}
	}

	member, err := bot.GetChatMember(chatID, userID, nil)
	if err != nil {
		return false
	}
	status := member.MergeChatMember().Status
	return status == "administrator" || status == "creator"
}

// extractTargetUserID gets a user ID from command args or reply_to_message.
func extractTargetUserID(msg *gotgbot.Message, args []string) (int64, error) {
	if len(args) > 0 {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("无效的用户 ID: %s", args[0])
		}
		return id, nil
	}

	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		return msg.ReplyToMessage.From.Id, nil
	}

	return 0, fmt.Errorf("请指定用户 ID 或回复目标用户的消息")
}

func (b *Bot) cmdAddBlocklist(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.From == nil {
		return nil
	}

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		bot.SendMessage(msg.Chat.Id, "用法: /addblocklist <关键词>", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	word := strings.TrimSpace(strings.Join(args[1:], " "))
	if word == "" {
		bot.SendMessage(msg.Chat.Id, "关键词不能为空", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	b.Blacklist.Add(word)
	if err := b.Store.AddBlacklistWord(word, strconv.FormatInt(msg.From.Id, 10)); err != nil {
		log.Printf("[bot] store.AddBlacklistWord error: %v", err)
		bot.SendMessage(msg.Chat.Id, "❌ 持久化黑名单词汇失败", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	reply := fmt.Sprintf("✅ 已添加黑名单词汇: <code>%s</code>", escapeHTML(word))
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

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		bot.SendMessage(msg.Chat.Id, "用法: /delblocklist <关键词>", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	word := strings.TrimSpace(strings.Join(args[1:], " "))
	if word == "" {
		bot.SendMessage(msg.Chat.Id, "关键词不能为空", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	if !b.Blacklist.Remove(word) {
		bot.SendMessage(msg.Chat.Id, "❌ 未找到该黑名单词汇", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	if err := b.Store.RemoveBlacklistWord(word); err != nil {
		log.Printf("[bot] store.RemoveBlacklistWord error: %v", err)
		b.Blacklist.Add(word)
		bot.SendMessage(msg.Chat.Id, "❌ 从数据库删除黑名单词汇失败", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	reply := fmt.Sprintf("✅ 已移除黑名单词汇: <code>%s</code>", escapeHTML(word))
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

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	words := b.Blacklist.List()
	if len(words) == 0 {
		bot.SendMessage(msg.Chat.Id, "黑名单为空", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	var sb strings.Builder
	sb.WriteString("📋 <b>黑名单词汇列表:</b>\n")
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
	if msg.From == nil {
		return nil
	}

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	args := strings.Fields(msg.Text)
	userID, err := extractTargetUserID(msg, args[1:])
	if err != nil {
		bot.SendMessage(msg.Chat.Id, err.Error(), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	chatID := msg.Chat.Id

	if err := b.approveUser(chatID, userID); err != nil {
		log.Printf("[bot] approveUser error: %v", err)
		bot.SendMessage(chatID, "❌ 批准验证失败", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	reply := fmt.Sprintf("✅ 已批准用户 <code>%d</code> 的验证", userID)
	bot.SendMessage(chatID, reply, &gotgbot.SendMessageOpts{
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

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	args := strings.Fields(msg.Text)
	userID, err := extractTargetUserID(msg, args[1:])
	if err != nil {
		bot.SendMessage(msg.Chat.Id, err.Error(), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	chatID := msg.Chat.Id

	if err := b.rejectUser(chatID, userID); err != nil {
		log.Printf("[bot] rejectUser error: %v", err)
		bot.SendMessage(chatID, "❌ 拒绝验证失败", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	reply := fmt.Sprintf("🚫 已拒绝用户 <code>%d</code> 的验证，其消息将被静默删除", userID)
	bot.SendMessage(chatID, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})

	return nil
}

func (b *Bot) cmdUnreject(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.From == nil {
		return nil
	}

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	args := strings.Fields(msg.Text)
	userID, err := extractTargetUserID(msg, args[1:])
	if err != nil {
		bot.SendMessage(msg.Chat.Id, err.Error(), &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	chatID := msg.Chat.Id

	if err := b.unrejectUser(chatID, userID); err != nil {
		log.Printf("[bot] unrejectUser error: %v", err)
		bot.SendMessage(chatID, "❌ 撤销拒绝失败", &gotgbot.SendMessageOpts{
			MessageThreadId: msg.MessageThreadId,
		})
		return nil
	}

	reply := fmt.Sprintf("✅ 已撤销对用户 <code>%d</code> 的拒绝，该用户可重新走验证流程", userID)
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

	if !b.isAdmin(bot, msg.Chat.Id, msg.From.Id) {
		return nil
	}

	words := b.Blacklist.List()
	reply := fmt.Sprintf("📊 <b>统计信息</b>\n黑名单词汇数: %d", len(words))
	bot.SendMessage(msg.Chat.Id, reply, &gotgbot.SendMessageOpts{
		ParseMode:       "HTML",
		MessageThreadId: msg.MessageThreadId,
	})

	return nil
}

func (b *Bot) cmdStart(bot *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg == nil || msg.Chat.Type != "private" {
		return nil
	}

	bot.SendMessage(msg.Chat.Id,
		"👋 欢迎使用 FuckAd 反广告机器人。\n\n"+
			"如果您需要完成人机验证，请点击群组中发送给您的验证链接。\n\n"+
			"<b>管理员命令:</b>\n"+
			"/addblocklist &lt;词汇&gt; - 添加黑名单\n"+
			"/delblocklist &lt;词汇&gt; - 移除黑名单\n"+
			"/listblocklist - 查看黑名单\n"+
			"/approve &lt;uid&gt; - 批准验证\n"+
			"/reject &lt;uid&gt; - 拒绝验证\n"+
			"/unreject &lt;uid&gt; - 撤销拒绝\n"+
			"/stats - 查看统计",
		&gotgbot.SendMessageOpts{ParseMode: "HTML"},
	)

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
