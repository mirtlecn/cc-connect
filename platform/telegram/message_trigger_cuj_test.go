package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/go-telegram/bot/models"
)

// Exercise the production Telegram update and callback handlers with the
// real engine and SDK. Only Telegram's service and the agent process are fake.
func TestCUJ_TG1_MessageTriggerApprovalAndOriginalReply(t *testing.T) {
	ctx := context.Background()
	requests := map[string]chan url.Values{
		"sendMessage": make(chan url.Values, 10), "answerCallbackQuery": make(chan url.Values, 10),
		"editMessageText": make(chan url.Values, 10), "setMessageReaction": make(chan url.Values, 10),
	}
	p := newTelegramTestPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		method := path.Base(r.URL.Path)
		if method != "sendChatAction" {
			channel, ok := requests[method]
			if !ok {
				t.Errorf("unexpected Bot API method %q", method)
				http.NotFound(w, r)
				return
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			channel <- r.Form
		}
		if method == "sendMessage" || method == "editMessageText" {
			_, _ = fmt.Fprint(w, `{"ok":true,"result":{"message_id":200}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	// The adapter's optional rich-message transport is separate from the SDK.
	// Reject it locally so even the regular @bot path cannot contact Telegram.
	p.httpClient = &http.Client{Transport: rejectTelegramRichTransport{}}
	p.newTypingTicker = func(d time.Duration) typingTicker {
		return &stdlibTypingTicker{Ticker: time.NewTicker(d)}
	}
	p.enableReactions = true
	p.allowFrom = "6"
	agent := &telegramTriggerCUJAgent{
		prompts: make(chan string, 4), events: make(chan core.Event, 4),
	}
	engine := core.NewEngine("telegram-trigger-cuj", agent, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Error(err)
		}
	})
	engine.SetSkipGit(true)
	engine.SetDisplayConfig(core.DisplayCfg{Mode: "quiet"})
	engine.SetStreamPreviewCfg(core.StreamPreviewCfg{})
	engine.SetReplyFooterEnabled(false)
	engine.SetAdminFrom("8,ou_feishu_admin,9,8")
	rule := core.MessageTriggerRule{
		Name: "Deploy", Enabled: true, Platform: "telegram", GroupMatch: "and",
		Groups: []core.MessageTriggerGroup{{
			Match: "and", Conditions: []core.MessageTriggerCondition{
				{Type: "group", Values: []string{"-100"}},
				{Type: "regex", Value: `^deploy\b`},
				{Type: "sender", Values: []string{"7"}},
			},
		}},
		Action: "agent", Confirm: true, Prompt: "Configured instruction",
		CCUserIDs: []string{"10", "11"},
	}
	engine.SetMessageTriggers([]core.MessageTriggerRule{rule}, core.MessageTriggerModel{})
	p.handler = engine.ReceiveMessage

	chat := models.Chat{ID: -100, Type: models.ChatTypeSupergroup, IsForum: true}
	send := func(id int, sender int64, text string, entities ...models.MessageEntity) {
		p.processUpdate(ctx, &models.Update{Message: &models.Message{
			ID: id, Date: int(time.Now().Unix()), Chat: chat, MessageThreadID: 55, Text: text, Entities: entities,
			From: &models.User{ID: sender, Username: "alice"},
		}})
	}
	await := func(method string) url.Values {
		t.Helper()
		select {
		case fields := <-requests[method]:
			return fields
		case <-time.After(3 * time.Second):
			t.Fatalf("Bot API did not receive %s", method)
			return nil
		}
	}
	awaitIdle := func() {
		t.Helper()
		// The mock records a request before writing its HTTP response.
		// Wait until the turn has finished before sending the next input.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			busy := false
			for _, session := range engine.GetSessions().AllSessions() {
				busy = busy || session.Busy()
			}
			if !busy {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("Telegram agent turn did not finish")
	}
	assertSourceReaction := func(messageID, emoji string) {
		t.Helper()
		fields := await("setMessageReaction")
		var reactions []models.ReactionType
		if err := json.Unmarshal([]byte(fields.Get("reaction")), &reactions); err != nil ||
			fields.Get("chat_id") != "-100" || fields.Get("message_id") != messageID ||
			len(reactions) != 1 || reactions[0].ReactionTypeEmoji == nil || reactions[0].ReactionTypeEmoji.Emoji != emoji {
			t.Fatalf("unexpected source reaction for message %s: %v, %v", messageID, fields, err)
		}
	}
	assertReplyDestination := func(fields url.Values, originalID int) {
		t.Helper()
		var reply models.ReplyParameters
		if err := json.Unmarshal([]byte(fields.Get("reply_parameters")), &reply); err != nil {
			t.Fatalf("missing original reply: %v", fields)
		}
		if fields.Get("chat_id") != "-100" || fields.Get("message_thread_id") != "55" || reply.MessageID != originalID {
			t.Fatalf("wrong original reply location: %v", fields)
		}
	}
	assertTriggeredResult := func(fields url.Values, originalID int) {
		t.Helper()
		assertReplyDestination(fields, originalID)
		text := fields.Get("text")
		if fields.Get("parse_mode") != string(models.ParseModeHTML) ||
			strings.Count(text, "tg://user?id=") != 3 ||
			!strings.Contains(text, `<a href="tg://user?id=7">@alice</a>`) ||
			!strings.Contains(text, "Fixture result") ||
			!strings.HasSuffix(text, `cc <a href="tg://user?id=10">@10</a> <a href="tg://user?id=11">@11</a>`) {
			t.Fatalf("trigger reply lost the original sender or configured cc mentions: %v", fields)
		}
	}
	assertOrdinaryTurn := func(input string) {
		t.Helper()
		fields := await("sendMessage")
		text := fields.Get("text")
		if fields.Get("reply_markup") != "" || !strings.Contains(text, "Fixture result") || strings.Contains(text, "tg://user?id=") {
			t.Fatalf("ordinary agent reply was captured by the rule: %v", fields)
		}
		if prompt := <-agent.prompts; strings.Contains(prompt, "Configured instruction") || !strings.Contains(prompt, input) {
			t.Fatalf("ordinary agent prompt for %q = %q", input, prompt)
		}
		awaitIdle()
	}
	readConfirmButtons := func(fields url.Values, originalID int) []models.InlineKeyboardButton {
		t.Helper()
		assertReplyDestination(fields, originalID)
		var keyboard models.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(fields.Get("reply_markup")), &keyboard); err != nil ||
			len(keyboard.InlineKeyboard) != 1 || len(keyboard.InlineKeyboard[0]) != 2 {
			t.Fatalf("invalid confirmation: %v, %v", fields, err)
		}
		if fields.Get("parse_mode") != string(models.ParseModeHTML) ||
			!strings.HasSuffix(fields.Get("text"), "\n\ncc <a href=\"tg://user?id=8\">@8</a> <a href=\"tg://user?id=9\">@9</a>") ||
			strings.Count(fields.Get("text"), "tg://user?id=") != 2 {
			t.Fatalf("confirmation should mention all and only the channel administrators: %v", fields)
		}
		return keyboard.InlineKeyboard[0]
	}
	click := func(clicker int64, button models.InlineKeyboardButton, confirmation url.Values) {
		prompt, _, _ := strings.Cut(confirmation.Get("text"), "\n\ncc ")
		p.processUpdate(ctx, &models.Update{CallbackQuery: &models.CallbackQuery{
			ID: "approval", From: models.User{ID: clicker}, Data: button.CallbackData,
			Message: models.MaybeInaccessibleMessage{Message: &models.Message{
				ID: 200, Chat: chat, MessageThreadID: 55, Text: html.UnescapeString(prompt) + "\n\ncc @8 @9",
			}},
		}})
	}

	p.processUpdate(ctx, &models.Update{Message: &models.Message{
		ID: 100, Date: int(time.Now().Unix()), Chat: chat, MessageThreadID: 55, Text: "ordinary conversation",
		From:           &models.User{ID: 7, Username: "alice"},
		ReplyToMessage: &models.Message{From: &models.User{ID: 8, FirstName: "Bob"}, Text: "deploy quoted text"},
	}})
	send(101, 7, "deploy first")
	first := await("sendMessage")
	assertSourceReaction("101", "⚡")
	firstButtons := readConfirmButtons(first, 101)
	click(7, firstButtons[0], first)
	if text := await("answerCallbackQuery").Get("text"); !strings.Contains(text, "administrators") {
		t.Fatalf("non-admin click feedback = %q", text)
	}
	click(8, firstButtons[1], first)
	if text := await("answerCallbackQuery").Get("text"); !strings.Contains(text, "declined") {
		t.Fatalf("admin decline feedback = %q", text)
	}
	assertSourceReaction("101", "👎")
	if edit := await("editMessageText"); edit.Get("reply_markup") != `{"inline_keyboard":[]}` || strings.Contains(edit.Get("text"), "@8") {
		t.Fatalf("decline left active buttons or repeated mentions: %v", edit)
	}
	select {
	case prompt := <-agent.prompts:
		t.Fatalf("agent ran before approval: %q", prompt)
	default:
	}

	rule.CCEnabled = true
	engine.SetMessageTriggers([]core.MessageTriggerRule{rule}, core.MessageTriggerModel{})
	link := models.MessageEntity{Type: models.MessageEntityTypeTextLink, Length: len("deploy"), URL: "https://example.test/runbook"}
	send(102, 7, "deploy approved", link)
	second := await("sendMessage")
	assertSourceReaction("102", "⚡")
	secondButtons := readConfirmButtons(second, 102)
	send(102, 7, "deploy approved", link) // Telegram can deliver the same source update again.
	click(8, secondButtons[0], second)
	if text := await("answerCallbackQuery").Get("text"); !strings.Contains(text, "Task submitted") {
		t.Fatalf("admin approval feedback = %q", text)
	}
	if edit := await("editMessageText"); edit.Get("reply_markup") != `{"inline_keyboard":[]}` || strings.Contains(edit.Get("text"), "@8") {
		t.Fatalf("approval left active buttons or repeated mentions: %v", edit)
	}
	assertTriggeredResult(await("sendMessage"), 102)
	if prompt := <-agent.prompts; !strings.Contains(prompt, "Configured instruction") ||
		!strings.Contains(prompt, "[deploy](https://example.test/runbook) approved") {
		t.Fatalf("agent prompt = %q", prompt)
	}
	awaitIdle()
	click(8, secondButtons[0], second)
	if text := await("answerCallbackQuery").Get("text"); !strings.Contains(text, "no longer pending") || len(requests["editMessageText"]) != 0 {
		t.Fatalf("duplicate approval changed the resolved card: %q", text)
	}

	rule.Confirm = false
	rule.Groups[0].Conditions[1].Value = `^(deploy\b|/status\b)`
	engine.SetMessageTriggers([]core.MessageTriggerRule{rule}, core.MessageTriggerModel{})
	mentionBot := func(id int, sender int64, text string) {
		const mention = "@TESTBOT"
		send(id, sender, mention+" "+text, models.MessageEntity{Type: models.MessageEntityTypeMention, Length: len(mention)})
	}
	mentionBot(103, 7, "deploy via direct mention")
	assertTriggeredResult(await("sendMessage"), 103)
	if prompt := <-agent.prompts; !strings.HasPrefix(prompt, "Configured instruction\n\ndeploy via direct mention") {
		t.Fatalf("matched direct @bot agent prompt = %q", prompt)
	}
	awaitIdle()

	mentionBot(107, 8, "deploy from a sender outside the rule")
	mentionBot(108, 7, "ordinary mention from outside allow_from")
	mentionBot(109, 7, "/status")
	if len(requests["sendMessage"]) != 0 || len(agent.prompts) != 0 {
		t.Fatal("a non-matching message or command from outside allow_from entered normal chat")
	}

	p.allowFrom = "7"
	mentionBot(104, 7, "ordinary direct mention")
	assertOrdinaryTurn("ordinary direct mention")

	p.processUpdate(ctx, &models.Update{Message: &models.Message{
		ID: 105, Date: int(time.Now().Unix()), Chat: chat, MessageThreadID: 55, Text: "deploy in reply to bot",
		From:           &models.User{ID: 7, Username: "alice"},
		ReplyToMessage: &models.Message{From: p.selfUser, Text: "Previous bot answer"},
	}})
	assertOrdinaryTurn("deploy in reply to bot")

	mentionBot(106, 7, "/status")
	if status := await("sendMessage"); !strings.Contains(strings.ToLower(status.Get("text")), "cc-connect status") {
		t.Fatalf("/status bypassed the command handler: %v", status)
	}

	if starts := agent.starts.Load(); starts != 1 {
		t.Fatalf("trigger and ordinary chat should reuse one agent session, started %d", starts)
	}
	awaitIdle()
	if len(requests["sendMessage"]) != 0 || len(agent.prompts) != 0 {
		t.Fatal("duplicate source, approval, or command triggered another agent turn")
	}
}

type rejectTelegramRichTransport struct{}

func (rejectTelegramRichTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("rich-message transport isolated by the Telegram integration fixture")
}

type telegramTriggerCUJAgent struct {
	starts  atomic.Int32
	prompts chan string
	events  chan core.Event
}

func (*telegramTriggerCUJAgent) Name() string { return "telegram-cuj-agent" }

func (a *telegramTriggerCUJAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	a.starts.Add(1)
	return a, nil
}

func (*telegramTriggerCUJAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (*telegramTriggerCUJAgent) Stop() error  { return nil }
func (*telegramTriggerCUJAgent) Close() error { return nil }
func (*telegramTriggerCUJAgent) Alive() bool  { return true }
func (*telegramTriggerCUJAgent) CurrentSessionID() string {
	return "telegram-cuj-agent-session"
}

func (a *telegramTriggerCUJAgent) Send(prompt, _ string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	a.prompts <- prompt
	a.events <- core.Event{Type: core.EventResult, Content: "Fixture result", SessionID: a.CurrentSessionID(), Done: true}
	return nil
}

func (*telegramTriggerCUJAgent) RespondPermission(string, core.PermissionResult) error { return nil }
func (a *telegramTriggerCUJAgent) Events() <-chan core.Event                           { return a.events }
