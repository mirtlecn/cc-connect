package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
)

func dingTalkTriggerCallback(id, conversation, conversationType, message string) chatbot.BotCallbackDataModel {
	return chatbot.BotCallbackDataModel{
		Msgtype: "text", MsgId: id, ConversationId: conversation, ConversationType: conversationType,
		SenderStaffId: "staff-1", SenderId: "sender-open", SenderNick: "Alice", ChatbotUserId: "bot-open",
		Text: chatbot.BotCallbackDataTextModel{Content: message},
	}
}

func deliverDingTalkTriggerCallback(t *testing.T, p *Platform, data chatbot.BotCallbackDataModel) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	p.onRawMessage(string(raw))
}

func TestDingTalkMessageTriggerCallbackScope(t *testing.T) {
	for _, tt := range []struct {
		name             string
		conversation     string
		conversationType string
		groupMention     bool
		mentionsBotID    bool
		selectPrivate    bool
		allGroups        bool
		wantTrigger      bool
	}{
		{name: "private", conversation: "dm", conversationType: "1", selectPrivate: true, wantTrigger: true},
		{name: "private not selected", conversation: "dm", conversationType: "1"},
		{name: "selected group", conversation: "group-one", conversationType: "2", groupMention: true, wantTrigger: true},
		{name: "group with native bot ID", conversation: "group-one", conversationType: "2", mentionsBotID: true, wantTrigger: true},
		{name: "another group", conversation: "group-two", conversationType: "2", groupMention: true},
		{name: "all selected groups", conversation: "group-two", conversationType: "2", groupMention: true, allGroups: true, wantTrigger: true},
		{name: "unmentioned group", conversation: "group-one", conversationType: "2", allGroups: true},
		{name: "unknown type", conversation: "group-one", conversationType: "3", groupMention: true, allGroups: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var msg *core.Message
			p := &Platform{handler: func(_ core.Platform, received *core.Message) { msg = received }}
			p.SetMessageTriggerScope([]string{"group-one"}, tt.allGroups, tt.selectPrivate)
			data := dingTalkTriggerCallback("msg-one", tt.conversation, tt.conversationType, " 巡检 UI ")
			data.IsInAtList = tt.groupMention
			data.AtUsers = []chatbot.BotCallbackDataAtUserModel{{StaffId: "staff-2", DingtalkId: "member-open"}}
			if tt.mentionsBotID {
				data.AtUsers = append(data.AtUsers, chatbot.BotCallbackDataAtUserModel{DingtalkId: "bot-open"})
			}
			deliverDingTalkTriggerCallback(t, p, data)
			if msg == nil || msg.Content != data.Text.Content || msg.ChannelKey != data.ConversationId ||
				msg.MessageID != data.MsgId || msg.UserID != data.SenderStaffId {
				t.Fatalf("ordinary message routing changed: %+v", msg)
			}
			if (msg.Trigger != nil) != tt.wantTrigger {
				t.Fatalf("trigger = %+v, want present %v", msg.Trigger, tt.wantTrigger)
			}
			if !tt.wantTrigger {
				return
			}
			event := msg.Trigger
			wantMention := tt.conversationType == "2"
			wantMentionedIDs := []string{"staff-2", "member-open"}
			if wantMention {
				wantMentionedIDs = append(wantMentionedIDs, "bot-open")
			}
			if event.Body != "巡检 UI" || event.ChatID != tt.conversation ||
				event.PrivateChat != (tt.conversationType == "1") || !event.BotMentionKnown ||
				event.DirectBotMention != wantMention || !slices.Equal(event.SenderIDs, []string{"staff-1", "sender-open"}) ||
				!slices.Equal(event.MentionedIDs, wantMentionedIDs) {
				t.Fatalf("unexpected trigger metadata: %+v", event)
			}
		})
	}
}

func TestDingTalkMessageTriggerUsesUnquotedBody(t *testing.T) {
	var msg *core.Message
	p := &Platform{handler: func(_ core.Platform, received *core.Message) { msg = received }}
	p.SetMessageTriggerScope(nil, false, true)
	p.onRawMessage(`{
		"msgtype": "text", "msgId": "check-reply", "conversationType": "1",
		"conversationId": "dm", "senderStaffId": "staff-1",
		"text": {"content": "继续", "isReplyMsg": true,
			"repliedMsg": {"msgType": "text", "content": {"text": "trigger phrase"}}}
	}`)
	if msg == nil || msg.Trigger == nil {
		t.Fatal("text reply did not reach the trigger matcher")
	}
	if msg.Trigger.Body != "继续" || !strings.Contains(msg.Content, "trigger phrase") {
		t.Fatalf("trigger body = %q, ordinary content = %q", msg.Trigger.Body, msg.Content)
	}
}

// Drive the installed callback path and the real rule engine. The inbound
// DingTalk callback's webhook is replaced by a loopback capture server.
func TestCUJ_DT1_DingTalkDirectTriggersReplyToSourceConversation(t *testing.T) {
	type delivery struct {
		path string
		text string
	}
	deliveries := make(chan delivery, 8)
	webhooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Markdown struct {
				Text string `json:"text"`
			} `json:"markdown"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture message", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, `{"errcode":0}`)
		deliveries <- delivery{path: r.URL.Path, text: payload.Markdown.Text}
	}))
	t.Cleanup(webhooks.Close)

	p := &Platform{allowFrom: "staff-1"}
	a := &dingTalkTriggerTestAgent{prompts: make(chan string, 4)}
	engine := core.NewEngine("dingtalk-trigger-cuj", a, []core.Platform{p}, "", core.LangEnglish)
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Error(err)
		}
	})
	engine.SetSkipGit(true)
	engine.SetReplyFooterEnabled(false)
	rule := func(name, expression, action, content string) core.MessageTriggerRule {
		return core.MessageTriggerRule{
			Name: name, Enabled: true, Platform: "dingtalk", GroupMatch: "and", Action: action, TriggerMode: "direct",
			Groups: []core.MessageTriggerGroup{{Match: "and", Conditions: []core.MessageTriggerCondition{
				{Type: "chat_scope", Value: "all"},
				{Type: "sender", Values: []string{"staff-1"}},
				{Type: "regex", Value: expression},
			}}},
			Command: content,
		}
	}
	agentRule := rule("agent", "^巡检$", "agent", "")
	agentRule.Prompt = "Rule instruction"
	engine.SetMessageTriggers([]core.MessageTriggerRule{
		rule("echo", "^回声$", "shell", "echo fixture-echo"), agentRule,
	}, core.MessageTriggerModel{})
	p.handler = engine.ReceiveMessage

	send := func(id, conversation, conversationType, text string) {
		t.Helper()
		data := dingTalkTriggerCallback(id, conversation, conversationType, text)
		data.SessionWebhook = webhooks.URL + "/" + conversation
		data.IsInAtList = conversationType == "2"
		deliverDingTalkTriggerCallback(t, p, data)
	}
	assertReply := func(conversation, expected string) {
		t.Helper()
		select {
		case result := <-deliveries:
			if result.path != "/"+conversation || !strings.Contains(result.text, expected) {
				t.Fatalf("reply = %+v, want %s containing %q", result, conversation, expected)
			}
		case <-time.After(4 * time.Second):
			t.Fatalf("no webhook reply for %s", conversation)
		}
	}

	send("msg-private", "private", "1", "回声")
	assertReply("private", "fixture-echo")
	send("msg-group", "group", "2", " 回声 ")
	assertReply("group", "fixture-echo")
	send("msg-group", "group", "2", "回声") // Retry of the same callback must not execute again.
	send("msg-agent", "group", "2", "巡检")
	assertReply("group", "Agent fixture result")
	if prompt := <-a.prompts; !strings.Contains(prompt, "Rule instruction") {
		t.Fatalf("rule-associated Agent prompt = %q", prompt)
	}
	send("msg-ordinary", "private", "1", "ordinary message")
	assertReply("private", "Agent fixture result")
	if prompt := <-a.prompts; strings.Contains(prompt, "Rule instruction") || !strings.Contains(prompt, "ordinary message") {
		t.Fatalf("ordinary Agent prompt = %q", prompt)
	}

	denied := dingTalkTriggerCallback("msg-denied", "group", "2", "回声")
	denied.SenderStaffId, denied.IsInAtList = "staff-other", true
	denied.SessionWebhook = webhooks.URL + "/group"
	deliverDingTalkTriggerCallback(t, p, denied)
	assertReply("group", core.UnauthorizedAccessMessage)
	if len(deliveries) != 0 || len(a.prompts) != 0 {
		t.Fatal("duplicate or unauthorized callback generated additional work")
	}
}

type dingTalkTriggerTestAgent struct {
	prompts chan string
}

func (*dingTalkTriggerTestAgent) Name() string { return "dingtalk-trigger-fixture" }
func (*dingTalkTriggerTestAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (*dingTalkTriggerTestAgent) Stop() error { return nil }

func (a *dingTalkTriggerTestAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return &dingTalkTriggerTestSession{prompts: a.prompts, events: make(chan core.Event, 1)}, nil
}

type dingTalkTriggerTestSession struct {
	prompts chan string
	events  chan core.Event
}

func (s *dingTalkTriggerTestSession) Send(prompt, _ string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.prompts <- prompt
	s.events <- core.Event{Type: core.EventResult, Content: "Agent fixture result", SessionID: s.CurrentSessionID(), Done: true}
	return nil
}
func (*dingTalkTriggerTestSession) RespondPermission(string, core.PermissionResult) error {
	return nil
}
func (s *dingTalkTriggerTestSession) Events() <-chan core.Event { return s.events }
func (*dingTalkTriggerTestSession) CurrentSessionID() string {
	return "dingtalk-trigger-fixture-session"
}
func (*dingTalkTriggerTestSession) Alive() bool  { return true }
func (*dingTalkTriggerTestSession) Close() error { return nil }
