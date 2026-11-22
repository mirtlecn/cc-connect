package feishu

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
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestFeishuMessageTriggers_GroupHistoryAndOriginalSession(t *testing.T) {
	messages := make(chan *core.Message, 3)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.shareSessionInChannel = true
	p.inboundCoalesceWindow = time.Hour
	t.Cleanup(p.flushInboundCoalesced)
	p.SetMessageTriggerGroups([]string{"oc_allowed"})
	sendGroupHistoryText(t, p, "om_ordinary", "ou_alice", "earlier ordinary message", "oc_allowed", "", false)
	first := awaitGroupHistoryMessage(t, messages)
	if first.Trigger == nil || !first.Trigger.RuleOnly || first.Trigger.Body != "earlier ordinary message" || first.ExtraContent != "" {
		t.Fatalf("ordinary rule candidate = %#v", first)
	}

	event := makeGroupHistoryEvent(t, "om_trigger", "ou_alice", "text", `{"text":"@_other deploy"}`, "oc_allowed", "", false)
	event.Event.Sender.SenderId.UserId = stringPtr("alice_account")
	event.Event.Message.Mentions = []*larkim.MentionEvent{{
		Key: stringPtr("@_other"), Name: stringPtr("Other"),
		Id: &larkim.UserId{OpenId: stringPtr("ou_other"), UserId: stringPtr("other_account"), UnionId: stringPtr("on_other")},
	}}
	if err := p.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	msg := awaitGroupHistoryMessage(t, messages)
	if msg.Trigger == nil || !msg.Trigger.RuleOnly || !msg.Trigger.BotMentionKnown || msg.Trigger.DirectBotMention ||
		msg.Trigger.ChatID != "oc_allowed" || !slices.Equal(msg.Trigger.SenderIDs, []string{"ou_alice", "alice_account"}) ||
		!slices.Equal(msg.Trigger.MentionedIDs, []string{"ou_other", "other_account", "on_other"}) {
		t.Fatalf("trigger metadata = %+v", msg.Trigger)
	}
	if msg.Content != "@Other deploy" || msg.Trigger.Body != "@Other deploy" ||
		!strings.Contains(msg.ExtraContent, "earlier ordinary message") || msg.OnAccepted == nil {
		t.Fatalf("trigger lost content/history: %#v", msg)
	}
	msg.OnAccepted()
	sendGroupHistoryText(t, p, "om_direct", "ou_alice", "deploy", "oc_allowed", "", true)
	direct := awaitGroupHistoryMessage(t, messages)
	if direct.SessionKey != msg.SessionKey || direct.Trigger == nil || !direct.Trigger.DirectBotMention || direct.Trigger.RuleOnly || direct.ExtraContent != "" {
		t.Fatalf("direct mention changed route or inherited consumed history: %#v", direct)
	}
}

func TestFeishuMessageTriggers_PrivateChatPreservesReplyAndSession(t *testing.T) {
	messages := make(chan *core.Message, 2)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.SetMessageTriggerGroups([]string{"oc_allowed"})
	send := func(messageID string) *core.Message {
		event := makeGroupHistoryEvent(t, messageID, "ou_alice", "text", `{"text":"deploy"}`, "oc_allowed", "", false)
		event.Event.Message.ChatType = stringPtr("p2p")
		if err := p.onMessage(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		return awaitGroupHistoryMessage(t, messages)
	}
	ordinary := send("om_private_ordinary")
	if ordinary.Trigger != nil {
		t.Fatalf("group scope produced a private trigger: %+v", ordinary.Trigger)
	}

	p.SetMessageTriggerPrivateChats(true)
	triggered := send("om_private_triggered")
	if triggered.Trigger == nil || !triggered.Trigger.PrivateChat || triggered.Trigger.RuleOnly ||
		triggered.Trigger.Body != "deploy" || triggered.SessionKey != ordinary.SessionKey {
		t.Fatalf("private trigger did not preserve text or routing: %#v", triggered)
	}
	if _, err := p.TriggerReplyContext(triggered); err != nil {
		t.Fatal(err)
	}
}

func TestFeishuMessageTriggers_PostBodyExcludesAttachmentNotice(t *testing.T) {
	messages := make(chan *core.Message, 1)
	p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.SetMessageTriggerGroups([]string{"oc_allowed"})
	event := makeGroupHistoryEvent(t, "om_post", "ou_alice", "post", `{"content":[[{"tag":"text","text":"deploy this"}]],"files":[{"file_name":"failed.pdf"}]}`, "oc_allowed", "", false)
	if err := p.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	msg := awaitGroupHistoryMessage(t, messages)
	if msg.Trigger == nil || msg.Trigger.Body != "deploy this" || !strings.Contains(msg.Content, "failed.pdf") {
		t.Fatalf("post body included a generated attachment notice or lost source text: %#v", msg)
	}
}

func TestFeishuMessageTriggers_KeepPlatformAllowListsAndFailClosed(t *testing.T) {
	messages := make(chan *core.Message, 2)
	p := newGroupReplyAllTestPlatform(t, "lark", map[string]any{
		"allow_from": "ou_alice", "allow_chat": "oc_allowed,oc_main",
	}, func(_ core.Platform, msg *core.Message) { messages <- msg })
	p.SetMessageTriggerGroups([]string{"oc_allowed", "oc_forbidden"})

	sendGroupHistoryText(t, p, "om_forbidden_chat", "ou_alice", "deploy", "oc_forbidden", "", false)
	sendGroupHistoryText(t, p, "om_forbidden_sender", "ou_other", "deploy", "oc_allowed", "", false)
	sendGroupHistoryText(t, p, "om_outside_rule", "ou_alice", "deploy", "oc_main", "", false)
	event := makeGroupHistoryEvent(t, "om_unknown_mention", "ou_alice", "text", `{"text":"@_unknown deploy"}`, "oc_allowed", "", false)
	event.Event.Message.Mentions = []*larkim.MentionEvent{{Key: stringPtr("@_unknown"), Id: &larkim.UserId{UserId: stringPtr("unknown")}}}
	if err := p.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	assertNoGroupHistoryMessage(t, messages)

	// A post can carry the bot's OpenID in a native at element without a
	// separate mention placeholder. It still belongs to the direct path.
	event = makeGroupHistoryEvent(t, "om_rich_direct", "ou_alice", "post", `{"content":[[{"tag":"at","user_id":"ou_bot"},{"tag":"text","text":"deploy"}]]}`, "oc_allowed", "", false)
	if err := p.onMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	msg := awaitGroupHistoryMessage(t, messages)
	if msg.Trigger == nil || !msg.Trigger.DirectBotMention || msg.Trigger.RuleOnly || msg.Content != "deploy" || msg.Platform != "lark" {
		t.Fatalf("rich direct mention routed incorrectly: %#v", msg)
	}
}

func TestFeishuMessageTriggerReply_NativeMentionAndOriginalThread(t *testing.T) {
	p := receiptTestPlatform(t, map[string]any{"thread_isolation": true, "no_reply_to_trigger": true}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/open-apis/im/v1/messages/om_source/reply" {
			t.Errorf("reply destination = %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			MessageType string `json:"msg_type"`
			Content     string `json:"content"`
			InThread    bool   `json:"reply_in_thread"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		var body struct{ Text string }
		if err := json.Unmarshal([]byte(request.Content), &body); err != nil {
			t.Error(err)
		}
		want := "<at user_id=\"ou_alice\">Alice &amp; Bob</at>\nresult\n\ncc <at user_id=\"ou_admin\"></at> <at user_id=\"ou_admin2\"></at>"
		if request.MessageType != "text" || !request.InThread || body.Text != want {
			t.Errorf("reply body = %+v, text = %q", request, body.Text)
		}
		_, _ = fmt.Fprint(w, `{"code":0}`)
	})
	msg := &core.Message{
		MessageID: "om_source", UserID: "ou_alice", UserName: "Alice & Bob",
		Trigger:  &core.MessageTriggerEvent{ChatID: "oc_allowed"},
		ReplyCtx: replyContext{chatID: "oc_allowed", messageID: "om_source", sessionKey: "feishu:oc_allowed:root:om_root"},
	}
	rctx, err := p.TriggerReplyContext(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.WithMessageTriggerCC(rctx, []string{"7", "*"}); err == nil {
		t.Fatal("reply accepted cc with no Feishu OpenIDs")
	}
	rctx, err = p.WithMessageTriggerCC(rctx, []string{"7", "ou_admin", "ou_admin2", "ou_admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Reply(context.Background(), rctx, "result"); err != nil {
		t.Fatal(err)
	}
	msg.MessageID = "om_other"
	if _, err := p.TriggerReplyContext(msg); err == nil {
		t.Fatal("accepted reply context from a different message")
	}
}

func TestFeishuMessageTriggerConfirmation_QuoteAndCallback(t *testing.T) {
	p := receiptTestPlatform(t, map[string]any{"thread_isolation": true, "no_reply_to_trigger": true}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/open-apis/im/v1/messages/om_source/reply" {
			t.Errorf("confirmation destination = %s %s", r.Method, r.URL.Path)
		}
		var req struct {
			MessageType string `json:"msg_type"`
			InThread    bool   `json:"reply_in_thread"`
			Content     string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.MessageType != "interactive" || !req.InThread || !strings.Contains(req.Content, "Run this task?") ||
			!strings.Contains(req.Content, `"action":"trigger-confirm:nonce:y"`) || !strings.Contains(req.Content, `"action":"trigger-confirm:nonce:n"`) ||
			!strings.Contains(req.Content, `"trigger_route":"`) {
			t.Errorf("confirmation request = %+v", req)
		}
		var card struct {
			Elements []struct {
				Content string `json:"content"`
			} `json:"elements"`
		}
		if err := json.Unmarshal([]byte(req.Content), &card); err != nil {
			t.Error(err)
		}
		want := "Run this task? &lt;at id=all&gt;&lt;/at&gt;\n\ncc <at id=ou_admin></at> <at id=ou_admin2></at>"
		if len(card.Elements) != 2 || card.Elements[0].Content != want {
			t.Errorf("confirmation did not mention only the Feishu administrators: %+v", card)
		}
		_, _ = fmt.Fprint(w, `{"code":0}`)
	})
	rctx := replyContext{chatID: "oc_test_chat", messageID: "om_source", sessionKey: "feishu:oc_test_chat:root:om_root"}
	if err := p.SendMessageTriggerConfirmation(context.Background(), rctx, "invalid", "Run?", "Yes", "No", []string{"*", "7"}); err == nil {
		t.Fatal("sent confirmation without a Feishu administrator")
	}
	if err := p.SendMessageTriggerConfirmation(context.Background(), rctx, "nonce", "Run this task? <at id=all></at>", "Yes", "No", []string{"7", "ou_admin", "ou_admin2", "ou_admin"}); err != nil {
		t.Fatal(err)
	}

	p.allowFrom = "ou_member,ou_admin"
	p.allowChat = "oc_test_chat"
	calls := 0
	p.SetMessageTriggerConfirmationHandler(func(id, chatID, userID string, approved bool) (string, bool) {
		calls++
		if id != "nonce" || chatID != "oc_test_chat" || approved != (userID != "ou_admin") {
			t.Errorf("callback metadata = %q %q %q %v", id, chatID, userID, approved)
		}
		if userID != "ou_admin" {
			return "Admin required", false
		}
		return "Declined", true
	})
	event := func(userID, action string) *callback.CardActionTriggerEvent {
		event := cardActionEventWithKey(userID, action, "feishu:oc_test_chat:ou_original_sender")
		event.Event.Action.Value[triggerConfirmationRouteKey] = p.triggerConfirmationRoute
		return event
	}
	// Trigger callbacks are independent of the requester's agent session.
	member, err := p.onCardAction(event("ou_member", "trigger-confirm:nonce:y"))
	if err != nil || member == nil || member.Toast == nil || member.Toast.Content != "Admin required" || member.Card != nil {
		t.Fatalf("member callback = %+v, %v", member, err)
	}
	otherProject := &Platform{allowFrom: "*", triggerConfirmationRoute: "other"}
	otherProject.SetMessageTriggerConfirmationHandler(func(_, _, _ string, _ bool) (string, bool) {
		t.Error("a different project claimed the confirmation")
		return "Expired", true
	})
	if response, err := otherProject.onCardAction(event("ou_admin", "trigger-confirm:nonce:n")); err != nil || response != nil {
		t.Fatalf("wrong project's callback = %+v, %v", response, err)
	}
	admin, err := p.onCardAction(event("ou_admin", "trigger-confirm:nonce:n"))
	if err != nil || admin == nil || admin.Toast == nil || admin.Toast.Content != "Declined" || admin.Card == nil {
		t.Fatalf("admin callback = %+v, %v", admin, err)
	}
	body, err := json.Marshal(admin.Card.Data)
	if err != nil || strings.Contains(string(body), `"tag":"button"`) || strings.Contains(string(body), "ou_admin") {
		t.Fatalf("resolved card still has buttons or mentions: %s, %v", body, err)
	}
	wrongChat := event("ou_admin", "trigger-confirm:nonce:y")
	wrongChat.Event.Context.OpenChatID = "oc_other"
	if response, err := p.onCardAction(wrongChat); err != nil || response != nil {
		t.Fatalf("callback from excluded chat = %+v, %v", response, err)
	}
	if response, err := p.onCardAction(event("ou_excluded", "trigger-confirm:nonce:y")); err != nil || response != nil {
		t.Fatalf("callback from excluded user = %+v, %v", response, err)
	}
	if calls != 2 {
		t.Fatalf("confirmation handler got %d calls, want 2", calls)
	}
}
