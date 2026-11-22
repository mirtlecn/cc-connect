package telegram

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const triggerConfirmationPrefix = "trigger-confirm:"
const triggerCCLabel = "\n\ncc "
const triggerHTMLMention = `<a href="tg://user?id=%[1]s">@%[1]s</a>`
const triggerMarkdownMention = `[@%[1]s](tg://user?id=%[1]s)`

var escapeTriggerMentionMarkdown = strings.NewReplacer(
	"\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`",
	"~", "\\~", "|", "\\|", "=", "\\=", "<", "&lt;", ">", "&gt;", "&", "&amp;",
)

func (p *Platform) SetMessageTriggerGroups(groups []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messageTriggerGroups = slices.Clone(groups)
}

func (p *Platform) triggerEvent(msg *models.Message) *core.MessageTriggerEvent {
	if msg.From.ID <= 0 || msg.From.IsBot || isCommand(msg) ||
		(msg.Text == "" && (msg.Caption == "" || (len(msg.Photo) == 0 && msg.Document == nil))) {
		return nil
	}
	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	p.mu.RLock()
	selected := slices.Contains(p.messageTriggerGroups, chatID)
	self := p.selfUser
	p.mu.RUnlock()
	if !selected || telegramReplyToBot(msg, self) {
		return nil
	}
	body := msg.Text
	if body == "" {
		body = msg.Caption
	}
	ids, complete := telegramMessageMentions(msg)
	event := &core.MessageTriggerEvent{
		ChatID: chatID, SenderIDs: telegramUserIDs(msg.From), MentionedIDs: ids, Body: body,
		BotMentionKnown: complete && self != nil && self.ID > 0 && self.Username != "",
	}
	event.DirectBotMention = telegramMentionsBot(ids, self)
	return event
}

func telegramUserIDs(user *models.User) []string {
	ids := []string{strconv.FormatInt(user.ID, 10)}
	if user.Username != "" {
		ids = append(ids, "@"+user.Username)
	}
	return ids
}

func telegramReplyToBot(msg *models.Message, bot *models.User) bool {
	return bot != nil && msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil &&
		msg.ReplyToMessage.From.ID == bot.ID
}

func telegramMessageMentions(msg *models.Message) ([]string, bool) {
	var ids []string
	complete := true
	for _, part := range []struct {
		text     string
		entities []models.MessageEntity
	}{{msg.Text, msg.Entities}, {msg.Caption, msg.CaptionEntities}} {
		for _, entity := range part.entities {
			switch entity.Type {
			case models.MessageEntityTypeMention:
				mention := extractEntityText(part.text, entity.Offset, entity.Length)
				if strings.HasPrefix(mention, "@") && len(mention) > 1 {
					ids = append(ids, mention)
				} else {
					complete = false
				}
			case models.MessageEntityTypeTextMention:
				if entity.User != nil && entity.User.ID > 0 {
					ids = append(ids, telegramUserIDs(entity.User)...)
				} else {
					complete = false
				}
			}
		}
	}
	return ids, complete
}

func telegramMentionsBot(ids []string, bot *models.User) bool {
	if bot == nil {
		return false
	}
	for _, botID := range telegramUserIDs(bot) {
		if slices.ContainsFunc(ids, func(id string) bool { return strings.EqualFold(id, botID) }) {
			return true
		}
	}
	return false
}

func (p *Platform) TriggerReplyContext(msg *core.Message) (any, error) {
	rc, ok := msg.ReplyCtx.(replyContext)
	sender, err := strconv.ParseInt(msg.UserID, 10, 64)
	if !ok || msg.Trigger == nil || rc.chatID == 0 || strconv.FormatInt(rc.chatID, 10) != msg.Trigger.ChatID ||
		rc.messageID <= 0 || strconv.Itoa(rc.messageID) != msg.MessageID || err != nil || sender <= 0 {
		return nil, fmt.Errorf("telegram: trigger reply requires the source message and sender ID")
	}
	name := strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(msg.UserName))
	if name == "" {
		name = msg.UserID
	}
	rc.triggerMentionText = "@" + name
	rc.triggerMentionURL = fmt.Sprintf("tg://user?id=%d", sender)
	return rc, nil
}

func (p *Platform) WithMessageTriggerCC(rctx any, userIDs []string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok || rc.triggerMentionURL == "" {
		return nil, fmt.Errorf("telegram: cc requires the trigger reply context")
	}
	ccIDs, length := telegramTriggerUsers(userIDs)
	if len(ccIDs) == 0 {
		return nil, fmt.Errorf("telegram: cc requires a numeric user ID")
	}
	rc.triggerCCUserIDs = ccIDs
	rc.triggerCCLength = len(triggerCCLabel) + length
	if rc.triggerCCLength+utf8.RuneCountInString(rc.triggerMentionText)+1 >= telegramMaxMessageLen {
		return nil, fmt.Errorf("telegram: trigger cc exceeds the message length limit")
	}
	return rc, nil
}

func telegramTriggerUsers(userIDs []string) ([]string, int) {
	seen := make(map[string]struct{}, len(userIDs))
	ids := make([]string, 0, len(userIDs))
	length := 0
	for _, id := range userIDs {
		id = strings.TrimSpace(id)
		value, err := strconv.ParseInt(id, 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != id {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
			length += len(id) + 1 // The rendered mention includes its leading @.
		}
	}
	return ids, length + max(0, len(ids)-1)
}

func formatTelegramTriggerCC(userIDs []string, format string) string {
	if len(userIDs) == 0 {
		return ""
	}
	mentions := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		mentions = append(mentions, fmt.Sprintf(format, id))
	}
	return triggerCCLabel + strings.Join(mentions, " ")
}

func telegramTriggerRichText(rc replyContext, content string) string {
	// Apply mentions after formatting so they stay outside the answer's collapsible details.
	sender := fmt.Sprintf("[%s](%s)", escapeTriggerMentionMarkdown.Replace(rc.triggerMentionText), rc.triggerMentionURL)
	return sender + "\n\n" + content + formatTelegramTriggerCC(rc.triggerCCUserIDs, triggerMarkdownMention)
}

func telegramTriggerHTML(rc replyContext) (sender, cc string) {
	sender = fmt.Sprintf(`<a href="%s">%s</a>`, rc.triggerMentionURL, html.EscapeString(rc.triggerMentionText)) + "\n"
	return sender, formatTelegramTriggerCC(rc.triggerCCUserIDs, triggerHTMLMention)
}

func (p *Platform) replyToTrigger(ctx context.Context, rc replyContext, content string) error {
	bot, err := p.connectedBot("reply to trigger")
	if err != nil {
		return err
	}
	mention, cc := telegramTriggerHTML(rc)
	// Leave room for the sender's mention and the final cc after Telegram
	// parses the HTML. If this reply is split, attach the cc only to its end.
	maxLen := telegramMaxMessageLen - utf8.RuneCountInString(rc.triggerMentionText) - 1 - rc.triggerCCLength
	chunks := core.SplitMessageCodeFenceAware(core.StripANSI(content), maxLen)
	for i, chunk := range chunks {
		prefix, footer := "", ""
		if i == 0 {
			prefix = mention
		}
		if i == len(chunks)-1 {
			footer = cc
		}
		params := &tgbot.SendMessageParams{
			ChatID: rc.chatID, MessageThreadID: rc.threadID,
			Text: prefix + core.FormatSmartTelegramHTML(chunk) + footer, ParseMode: models.ParseModeHTML,
			ReplyParameters: &models.ReplyParameters{MessageID: rc.messageID},
		}
		if _, err = bot.SendMessage(ctx, params); err != nil && strings.Contains(err.Error(), "can't parse") {
			params.Text = prefix + html.EscapeString(chunk) + footer
			_, err = bot.SendMessage(ctx, params)
		}
		if err != nil {
			return fmt.Errorf("telegram: reply to trigger message: %w", err)
		}
	}
	return nil
}

func (p *Platform) SetMessageTriggerConfirmationHandler(handler func(id, chatID, userID string, approved bool) (feedback string, resolved bool)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.triggerConfirmationHandler = handler
}

func (p *Platform) SendMessageTriggerConfirmation(ctx context.Context, rctx any, id, prompt, yesLabel, noLabel string, adminIDs []string) error {
	rc, ok := rctx.(replyContext)
	if !ok || rc.chatID == 0 || rc.messageID <= 0 {
		return fmt.Errorf("telegram: trigger confirmation requires the source message")
	}
	data := triggerConfirmationPrefix + id + ":"
	if id == "" || len(data)+1 > 64 {
		return fmt.Errorf("telegram: invalid trigger confirmation button data length")
	}
	admins, length := telegramTriggerUsers(adminIDs)
	if len(admins) == 0 {
		return fmt.Errorf("telegram: trigger confirmation requires a numeric administrator ID")
	}
	if length+len(triggerCCLabel)+utf8.RuneCountInString(prompt) > telegramMaxMessageLen {
		return fmt.Errorf("telegram: trigger confirmation exceeds the message length limit")
	}
	bot, err := p.connectedBot("send trigger confirmation")
	if err != nil {
		return err
	}
	params := &tgbot.SendMessageParams{
		ChatID: rc.chatID, MessageThreadID: rc.threadID, Text: html.EscapeString(prompt) + formatTelegramTriggerCC(admins, triggerHTMLMention), ParseMode: models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: rc.messageID},
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: yesLabel, CallbackData: data + "y"},
			{Text: noLabel, CallbackData: data + "n"},
		}}},
	}
	if _, err := bot.SendMessage(ctx, params); err != nil {
		return fmt.Errorf("telegram: send trigger confirmation: %w", err)
	}
	return nil
}

func (p *Platform) handleTriggerConfirmation(ctx context.Context, bot telegramBot, callbackID string, msg *models.Message, userID, data string) {
	id, choice, ok := strings.Cut(data, ":")
	p.mu.RLock()
	handler := p.triggerConfirmationHandler
	p.mu.RUnlock()
	var feedback string
	var resolved bool
	if ok && id != "" && (choice == "y" || choice == "n") && handler != nil {
		feedback, resolved = handler(id, strconv.FormatInt(msg.Chat.ID, 10), userID, choice == "y")
	}
	if _, err := bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: feedback}); err != nil {
		slog.Debug("telegram: answer trigger confirmation failed", "error", err)
	}
	if !resolved {
		return
	}
	prompt := msg.Text
	if i := strings.LastIndex(prompt, triggerCCLabel); i >= 0 {
		prompt = prompt[:i]
	}
	text := prompt + "\n\n" + feedback
	if utf8.RuneCountInString(text) > telegramMaxMessageLen {
		text = feedback
	}
	if _, err := bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID: msg.Chat.ID, MessageID: msg.ID, Text: text,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}},
	}); err != nil {
		slog.Debug("telegram: update trigger confirmation message failed", "error", err)
	}
}
