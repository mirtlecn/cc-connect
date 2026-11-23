package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func telegramTriggerTestMessage() *models.Message {
	return &models.Message{
		ID: 10, MessageThreadID: 55, Text: "🚀 @bob deploy", Date: int(time.Now().Unix()),
		From:     &models.User{ID: 7, Username: "alice"},
		Chat:     models.Chat{ID: -100, Type: models.ChatTypeSupergroup, IsForum: true},
		Entities: []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 3, Length: 4}},
	}
}

func TestTelegramMessageTriggers_NativeIdentityAndGroupRouting(t *testing.T) {
	var got *core.Message
	p := &Platform{selfUser: &models.User{ID: 42, Username: "mybot"}}
	p.handler = func(_ core.Platform, msg *core.Message) { got = msg }
	p.SetMessageTriggerGroups([]string{"-100"})
	msg := telegramTriggerTestMessage()
	msg.Entities = append(msg.Entities, models.MessageEntity{Type: models.MessageEntityTypeTextMention, User: &models.User{ID: 99}})
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger == nil || !got.Trigger.RuleOnly || !got.Trigger.BotMentionKnown || got.Trigger.DirectBotMention ||
		got.Trigger.ChatID != "-100" || got.Trigger.Body != msg.Text || !slices.Equal(got.Trigger.SenderIDs, []string{"7", "@alice"}) ||
		!slices.Equal(got.Trigger.MentionedIDs, []string{"@bob", "99"}) {
		t.Fatalf("native mention metadata = %#v", got)
	}
	if got.SessionKey != "telegram:-100:55:7" {
		t.Fatalf("trigger session = %s", got.SessionKey)
	}
	route := got.SessionKey

	got = nil
	msg = telegramTriggerTestMessage()
	msg.Text += " @MyBot"
	msg.Entities = append(msg.Entities, models.MessageEntity{Type: models.MessageEntityTypeMention, Offset: 15, Length: 6})
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger == nil || !got.Trigger.DirectBotMention || got.Trigger.RuleOnly || got.SessionKey != route {
		t.Fatalf("direct bot mention did not preserve the original route: %#v", got)
	}

	got = nil
	msg = telegramTriggerTestMessage()
	msg.ReplyToMessage = &models.Message{From: p.selfUser, Text: "a bot answer"}
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger != nil || got.SessionKey != route || !strings.Contains(got.ExtraContent, "a bot answer") {
		t.Fatalf("quoted bot answer did not preserve the original route: %#v", got)
	}

	msg.Chat = models.Chat{ID: 7, Type: models.ChatTypePrivate}
	msg.MessageThreadID = 0
	got = nil
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger != nil || got.SessionKey != "telegram:7:7" {
		t.Fatalf("private chat without trigger subscription = %#v", got)
	}

	p.SetMessageTriggerPrivateChats(true)
	got = nil
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger == nil || !got.Trigger.PrivateChat || got.Trigger.RuleOnly || got.Trigger.ChatID != "7" ||
		got.Trigger.Body != msg.Text || got.SessionKey != "telegram:7:7" || !strings.Contains(got.ExtraContent, "a bot answer") {
		t.Fatalf("private reply to bot did not preserve the source and route: %#v", got)
	}

	msg.Text = "/status"
	msg.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Length: len(msg.Text)}}
	got = nil
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger != nil || got.Content != "/status" {
		t.Fatalf("private slash command was captured by a trigger: %#v", got)
	}

	p.SetMessageTriggerScope(nil, true, false)
	msg = telegramTriggerTestMessage()
	msg.Chat.ID = -101
	got = nil
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger == nil || !got.Trigger.RuleOnly || got.Trigger.PrivateChat || got.Trigger.ChatID != "-101" ||
		got.SessionKey != "telegram:-101:55:7" {
		t.Fatalf("all-groups scope did not admit group outside the explicit IDs: %#v", got)
	}

	p.SetMessageTriggerGroups([]string{"-100"})
	got = nil
	p.handleMessage(context.Background(), msg)
	if got != nil {
		t.Fatalf("explicit groups still admitted a message through the previous wildcard scope: %#v", got)
	}
}

func TestTelegramMessageTriggers_BodyBeforeLinkAndReplyEnrichment(t *testing.T) {
	var got *core.Message
	p := &Platform{selfUser: &models.User{ID: 42, Username: "mybot"}}
	p.handler = func(_ core.Platform, msg *core.Message) { got = msg }
	p.SetMessageTriggerGroups([]string{"-100"})
	msg := telegramTriggerTestMessage()
	msg.Text = " deploy this "
	msg.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeTextLink, Offset: 1, Length: 6, URL: "https://example.test/hidden-url"}}
	msg.ReplyToMessage = &models.Message{From: &models.User{ID: 8, FirstName: "Other"}, Text: "quoted body"}
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger == nil || got.Trigger.Body != msg.Text ||
		!strings.Contains(got.Content, "[deploy](https://example.test/hidden-url)") || !strings.Contains(got.ExtraContent, "quoted body") {
		t.Fatalf("message body or agent context changed: %#v", got)
	}

	msg.Text, msg.Caption = "", "  source caption  "
	msg.Entities = nil
	msg.Photo = []models.PhotoSize{{}}
	if event := p.triggerEvent(msg); event == nil || event.Body != msg.Caption {
		t.Fatalf("caption body = %+v", event)
	}
}

func TestTelegramMessageTriggers_ScopedAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*Platform, *models.Message)
	}{
		{"unselected group", func(_ *Platform, msg *models.Message) { msg.Chat.ID = -101 }},
		{"bot sender", func(_ *Platform, msg *models.Message) { msg.From.IsBot = true }},
		{"self unknown", func(p *Platform, _ *models.Message) { p.selfUser = nil }},
		{"malformed mention", func(_ *Platform, msg *models.Message) { msg.Entities[0].Offset = 100 }},
		{"all groups with foreign bot command", func(p *Platform, msg *models.Message) {
			p.SetMessageTriggerScope(nil, true, false)
			msg.Chat.ID = -101
			msg.Text = "/help@otherbot"
			msg.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Length: len(msg.Text)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Platform{selfUser: &models.User{ID: 42, Username: "mybot"}}
			p.handler = func(_ core.Platform, msg *core.Message) { t.Errorf("unexpected dispatch: %#v", msg) }
			p.SetMessageTriggerGroups([]string{"-100"})
			msg := telegramTriggerTestMessage()
			tc.alter(p, msg)
			p.handleMessage(context.Background(), msg)
		})
	}
}

func TestTelegramMessageTriggers_PrivateSenderOutsideAllowFromIsRuleOnly(t *testing.T) {
	var got *core.Message
	p := &Platform{allowFrom: "8", selfUser: &models.User{ID: 42, Username: "mybot"}}
	p.handler = func(_ core.Platform, msg *core.Message) { got = msg }
	p.SetMessageTriggerPrivateChats(true)

	msg := telegramTriggerTestMessage()
	msg.Chat = models.Chat{ID: 7, Type: models.ChatTypePrivate}
	p.handleMessage(context.Background(), msg)
	if got == nil || got.Trigger == nil || !got.Trigger.RuleOnly || !got.Trigger.ChatAccessDenied || !got.Trigger.PrivateChat ||
		!slices.Equal(got.Trigger.SenderIDs, []string{"7", "@alice"}) {
		t.Fatalf("private sender outside allow_from was not routed exclusively to rules: %#v", got)
	}
}

func TestTelegramMessageTriggerReply_NativeMentionAndOriginalTopic(t *testing.T) {
	p := newTelegramTestPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("reply endpoint = %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		var reply models.ReplyParameters
		if err := json.Unmarshal([]byte(r.FormValue("reply_parameters")), &reply); err != nil {
			t.Error(err)
		}
		content := r.FormValue("text")
		if r.FormValue("chat_id") != "-100" || r.FormValue("message_thread_id") != "55" || reply.MessageID != 10 ||
			r.FormValue("parse_mode") != string(models.ParseModeHTML) ||
			!strings.HasPrefix(content, "<a href=\"tg://user?id=7\">@Alice &amp; Bob</a>\n") || !strings.Contains(content, "result") ||
			!strings.HasSuffix(content, "\n\ncc <a href=\"tg://user?id=8\">@8</a> <a href=\"tg://user?id=9\">@9</a>") {
			t.Errorf("native reply payload = %v", r.Form)
		}
		if _, err := fmt.Fprint(w, `{"ok":true,"result":{"message_id":15}}`); err != nil {
			t.Error(err)
		}
	})
	msg := &core.Message{
		MessageID: "10", UserID: "7", UserName: "Alice & Bob",
		Trigger:  &core.MessageTriggerEvent{ChatID: "-100"},
		ReplyCtx: replyContext{chatID: -100, threadID: 55, messageID: 10},
	}
	rctx, err := p.TriggerReplyContext(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.WithMessageTriggerCC(rctx, []string{"ou_admin", "*"}); err == nil {
		t.Fatal("reply accepted cc with no numeric administrator IDs")
	}
	rctx, err = p.WithMessageTriggerCC(rctx, []string{"8", "ou_admin", "9", "8"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Reply(context.Background(), rctx, "**result**"); err != nil {
		t.Fatal(err)
	}
	msg.Trigger.ChatID = "-101"
	if _, err := p.TriggerReplyContext(msg); err == nil {
		t.Fatal("accepted reply context from a different group")
	}
}

type triggerReactionBot struct {
	*stubTelegramBot
	reactions chan *tgbot.SetMessageReactionParams
}

func (b *triggerReactionBot) SetMessageReaction(_ context.Context, params *tgbot.SetMessageReactionParams) (bool, error) {
	b.reactions <- params
	return true, nil
}

func TestTelegramMessageTriggers_ReactOnlyAfterAcceptance(t *testing.T) {
	bot := &triggerReactionBot{newStubTelegramBot(), make(chan *tgbot.SetMessageReactionParams, 2)}
	p := &Platform{bot: bot, selfUser: &models.User{ID: 42, Username: "mybot"}, enableReactions: true}
	p.SetMessageTriggerGroups([]string{"-100"})
	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) { got = msg }
	p.handleMessage(context.Background(), telegramTriggerTestMessage())
	if got == nil || got.OnAccepted == nil {
		t.Fatalf("missing deferred reaction: %#v", got)
	}
	select {
	case reaction := <-bot.reactions:
		t.Fatalf("reaction before acceptance: %+v", reaction)
	case <-time.After(15 * time.Millisecond):
	}
	got.OnAccepted()
	got.OnAccepted()
	select {
	case reaction := <-bot.reactions:
		if reaction.ChatID != int64(-100) || reaction.MessageID != 10 || len(reaction.Reaction) != 1 || reaction.Reaction[0].ReactionTypeEmoji.Emoji != "⚡" {
			t.Fatalf("unexpected source-message reaction: %+v", reaction)
		}
	case <-time.After(time.Second):
		t.Fatal("no reaction after acceptance")
	}
	select {
	case reaction := <-bot.reactions:
		t.Fatalf("duplicate reaction: %+v", reaction)
	case <-time.After(15 * time.Millisecond):
	}
}

type triggerConfirmationBot struct {
	*stubTelegramBot
	sends   []*tgbot.SendMessageParams
	answers []*tgbot.AnswerCallbackQueryParams
	edits   []*tgbot.EditMessageTextParams
}

func (b *triggerConfirmationBot) SendMessage(_ context.Context, params *tgbot.SendMessageParams) (*models.Message, error) {
	b.sends = append(b.sends, params)
	return &models.Message{ID: 11}, nil
}

func (b *triggerConfirmationBot) AnswerCallbackQuery(_ context.Context, params *tgbot.AnswerCallbackQueryParams) (bool, error) {
	b.answers = append(b.answers, params)
	return true, nil
}

func (b *triggerConfirmationBot) EditMessageText(_ context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error) {
	b.edits = append(b.edits, params)
	return &models.Message{ID: params.MessageID}, nil
}

func TestTelegramMessageTriggerConfirmation_QuoteAndCallback(t *testing.T) {
	bot := &triggerConfirmationBot{stubTelegramBot: newStubTelegramBot()}
	p := &Platform{bot: bot, allowFrom: "7"}
	ctx := context.Background()
	rctx := replyContext{chatID: -100, threadID: 55, messageID: 10}
	if err := p.SendMessageTriggerConfirmation(ctx, rctx, strings.Repeat("é", 24), "Run?", "Yes", "No", []string{"8"}); err == nil {
		t.Fatal("accepted callback data over Telegram's byte limit")
	}
	if err := p.SendMessageTriggerConfirmation(ctx, rctx, "missing-admin", "Run?", "Yes", "No", []string{"*", "ou_admin"}); err == nil {
		t.Fatal("sent confirmation without a Telegram administrator")
	}
	if err := p.SendMessageTriggerConfirmation(ctx, rctx, "nonce", "Run <task>?", "Yes", "No", []string{"ou_admin", "8", "9", "8"}); err != nil {
		t.Fatal(err)
	}
	if len(bot.sends) != 1 {
		t.Fatalf("confirmation sends = %d", len(bot.sends))
	}
	sent := bot.sends[0]
	markup, ok := sent.ReplyMarkup.(*models.InlineKeyboardMarkup)
	if sent.ChatID != int64(-100) || sent.MessageThreadID != 55 || sent.ReplyParameters == nil || sent.ReplyParameters.MessageID != 10 ||
		!ok || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 ||
		markup.InlineKeyboard[0][0].CallbackData != "trigger-confirm:nonce:y" || markup.InlineKeyboard[0][1].CallbackData != "trigger-confirm:nonce:n" ||
		sent.ParseMode != models.ParseModeHTML || sent.Text != "Run &lt;task&gt;?\n\ncc <a href=\"tg://user?id=8\">@8</a> <a href=\"tg://user?id=9\">@9</a>" {
		t.Fatalf("confirmation send = %+v", sent)
	}

	calls := 0
	p.SetMessageTriggerConfirmationHandler(func(id, chatID, userID string, approved bool) (string, bool) {
		calls++
		if id != "nonce" || chatID != "-100" || !approved {
			t.Errorf("callback metadata = %q %q %q %v", id, chatID, userID, approved)
		}
		if userID == "8" {
			return "Approved", true
		}
		return "Admin required", false
	})
	for _, userID := range []int64{7, 8} {
		p.handleCallbackQuery(ctx, &models.CallbackQuery{
			ID: "cb", From: models.User{ID: userID}, Data: "trigger-confirm:nonce:y",
			Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 11, Text: "Run?\n\ncc @8 @9", Chat: models.Chat{ID: -100}}},
		})
	}
	p.handleCallbackQuery(ctx, &models.CallbackQuery{
		ID: "ordinary-cb", From: models.User{ID: 8}, Data: "cmd:/status",
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 11, Chat: models.Chat{ID: -100}}},
	})
	if calls != 2 || len(bot.answers) != 2 || bot.answers[0].Text != "Admin required" || bot.answers[1].Text != "Approved" || len(bot.edits) != 1 {
		t.Fatalf("callback handler calls=%d, answers=%+v, edits=%+v", calls, bot.answers, bot.edits)
	}
	edit := bot.edits[0]
	markup, ok = edit.ReplyMarkup.(*models.InlineKeyboardMarkup)
	if edit.ChatID != int64(-100) || edit.MessageID != 11 || edit.Text != "Run?\n\nApproved" || !ok || len(markup.InlineKeyboard) != 0 {
		t.Fatalf("resolved confirmation edit = %+v", edit)
	}
}

func TestTelegramMessageTriggerReply_CCMentionsOnlyOnLastChunk(t *testing.T) {
	bot := &triggerConfirmationBot{stubTelegramBot: newStubTelegramBot()}
	p := &Platform{bot: bot}
	base := replyContext{chatID: -100, threadID: 55, messageID: 10, triggerMentionText: "@alice", triggerMentionURL: "tg://user?id=7"}
	rctx, err := p.WithMessageTriggerCC(base, []string{"8", "9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Reply(context.Background(), rctx, strings.Repeat("x", telegramMaxMessageLen)); err != nil {
		t.Fatal(err)
	}
	if len(bot.sends) != 2 {
		t.Fatalf("long telegram reply used %d messages, want 2", len(bot.sends))
	}
	if strings.Contains(bot.sends[0].Text, "\n\ncc ") ||
		!strings.HasSuffix(bot.sends[1].Text, `cc <a href="tg://user?id=8">@8</a> <a href="tg://user?id=9">@9</a>`) {
		t.Fatal("native cc mentions did not appear only on the last chunk")
	}
}
