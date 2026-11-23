package core

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMessageTriggerConditions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		condition MessageTriggerCondition
		event     MessageTriggerEvent
		want      bool
	}{
		{"regex honors ignore case flag", MessageTriggerCondition{Type: "regex", Value: "(?i:release|报警)"}, MessageTriggerEvent{ChatID: "group", Body: "RELEASE ready"}, true},
		{"regex is case sensitive by default", MessageTriggerCondition{Type: "regex", Value: "release"}, MessageTriggerEvent{ChatID: "group", Body: "RELEASE ready"}, false},
		{"regex matches exact whitespace", MessageTriggerCondition{Type: "regex", Value: "^  deploy $"}, MessageTriggerEvent{ChatID: "group", Body: "  deploy "}, true},
		{"sender can match one ID", MessageTriggerCondition{Type: "sender", Values: []string{"one", "@operator"}}, MessageTriggerEvent{ChatID: "group", SenderIDs: []string{"@Operator"}}, true},
		{"native mention matches", MessageTriggerCondition{Type: "mentioned", Values: []string{"me"}}, MessageTriggerEvent{ChatID: "group", MentionedIDs: []string{"me"}}, true},
		{"mention requires native metadata", MessageTriggerCondition{Type: "mentioned", Values: []string{"me"}}, MessageTriggerEvent{ChatID: "group", Body: "@me please review"}, false},
		{"another group fails the scope condition", MessageTriggerCondition{Type: "regex", Value: "release"}, MessageTriggerEvent{ChatID: "other", Body: "release"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule, err := normalizeMessageTriggerRule(MessageTriggerRule{
				Groups: []MessageTriggerGroup{{Match: "and", Conditions: []MessageTriggerCondition{
					{Type: "group", Values: []string{"group"}}, tc.condition,
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := rule.matchesGroups(&tc.event, time.Now()); got != tc.want {
				t.Fatalf("match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMessageTriggerGroupsCombineIndependently(t *testing.T) {
	rule, err := normalizeMessageTriggerRule(MessageTriggerRule{
		Enabled: true, Platform: "test",
		Groups: []MessageTriggerGroup{
			{Match: "and", Conditions: []MessageTriggerCondition{
				{Type: "group", Values: []string{"release"}},
				{Type: "regex", Value: "^confirmed$"},
			}},
			{Match: "and", Conditions: []MessageTriggerCondition{
				{Type: "group", Values: []string{"ops"}},
				{Type: "mentioned", Values: []string{"on-call"}},
				{Type: "sender", Values: []string{"operator"}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	operator := &MessageTriggerEvent{ChatID: "ops", Body: "contact", SenderIDs: []string{"operator"}, MentionedIDs: []string{"on-call"}}
	at := time.Now()
	if !rule.watches("test", operator) || rule.watches("test", &MessageTriggerEvent{ChatID: "other"}) || !rule.matchesGroups(operator, at) {
		t.Fatalf("OR failed to match the second group within scope")
	}
	rule.GroupMatch = "and"
	if rule.matchesGroups(operator, at) {
		t.Fatal("AND must require both groups")
	}
	rule.GroupMatch = "or"
	rule.Groups[1].Match = "or"
	operator.Body, operator.SenderIDs, operator.MentionedIDs = "not a match", nil, nil
	if !rule.matchesGroups(operator, at) {
		t.Fatal("a group-ID condition participates in OR")
	}
}

type messageTriggerTestCard struct {
	id     string
	reply  string
	prompt string
	admins []string
}

type messageTriggerTestPlatform struct {
	*stubPlatformEngine
	confirm func(id, chatID, userID string, approved bool) (string, bool)
	cards   chan messageTriggerTestCard
}

func newMessageTriggerTestPlatform() *messageTriggerTestPlatform {
	return &messageTriggerTestPlatform{
		stubPlatformEngine: &stubPlatformEngine{n: "trigger-test"},
		cards:              make(chan messageTriggerTestCard, 8),
	}
}

func (p *messageTriggerTestPlatform) Reply(ctx context.Context, reply any, content string) error {
	return p.stubPlatformEngine.Reply(ctx, reply, fmt.Sprintf("%v -> %s", reply, content))
}

func (p *messageTriggerTestPlatform) Send(ctx context.Context, reply any, content string) error {
	if result := MessageTriggerResult(ctx); result != nil {
		return p.Reply(ctx, result, content)
	}
	return p.stubPlatformEngine.Send(ctx, reply, content)
}

func (*messageTriggerTestPlatform) SetMessageTriggerGroups([]string) {}
func (*messageTriggerTestPlatform) TriggerReplyContext(m *Message) (any, error) {
	return "source:" + m.MessageID, nil
}

func (*messageTriggerTestPlatform) WithMessageTriggerCC(reply any, userIDs []string) (any, error) {
	return fmt.Sprintf("%v cc %s", reply, strings.Join(userIDs, " ")), nil
}

func (p *messageTriggerTestPlatform) SetMessageTriggerConfirmationHandler(fn func(string, string, string, bool) (string, bool)) {
	p.confirm = fn
}

func (p *messageTriggerTestPlatform) SendMessageTriggerConfirmation(
	_ context.Context, reply any, id, prompt, _, _ string, admins []string,
) error {
	p.cards <- messageTriggerTestCard{id: id, reply: reply.(string), prompt: prompt, admins: admins}
	return nil
}

func (p *messageTriggerTestPlatform) readCard(t *testing.T) messageTriggerTestCard {
	t.Helper()
	select {
	case card := <-p.cards:
		return card
	case <-time.After(3 * time.Second):
		t.Fatal("no confirmation card was sent")
		return messageTriggerTestCard{}
	}
}

func messageTriggerTestMessage(p Platform, id, content string, direct bool) *Message {
	return &Message{
		Platform: p.Name(), SessionKey: "same-group-session", MessageID: id,
		UserID: "sender", UserName: "sender", ReplyCtx: id, Content: content,
		Trigger: &MessageTriggerEvent{
			ChatID: "group", Body: content, SenderIDs: []string{"sender"}, MentionedIDs: []string{"me"},
			BotMentionKnown: true, DirectBotMention: direct, RuleOnly: !direct,
		},
	}
}

func TestMessageTriggerIsolatesPrivateChatsAndGroups(t *testing.T) {
	p := newMessageTriggerTestPlatform()
	agent := &cujAgent{}
	e := NewEngine("trigger-chat-scope-project", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetReplyFooterEnabled(false)
	rules := []MessageTriggerRule{{
		Name: "private task", Enabled: true, Platform: p.Name(), TriggerMode: "direct", Action: "agent",
		Groups: []MessageTriggerGroup{{Match: "and", Conditions: []MessageTriggerCondition{
			{Type: "chat_scope", Value: "private"},
			{Type: "regex", Value: "^run task$"},
		}}},
		Prompt: "private rule",
	}, {
		Name: "group task", Enabled: true, Platform: p.Name(), TriggerMode: "direct", Action: "agent",
		Groups: []MessageTriggerGroup{{Match: "and", Conditions: []MessageTriggerCondition{
			{Type: "group", Values: []string{"group"}}, {Type: "regex", Value: "^run task$"},
		}}},
		Prompt: "group rule",
	}}
	e.SetMessageTriggers(rules, MessageTriggerModel{})

	otherGroup := messageTriggerTestMessage(p, "other-group", "run task", false)
	otherGroup.SessionKey, otherGroup.Trigger.ChatID = "unwatched-group-session", "other"
	e.ReceiveMessage(p, otherGroup)
	if id := e.sessions.ActiveSessionID(otherGroup.SessionKey); id != "" {
		t.Fatalf("unwatched group started agent session %q", id)
	}

	env := &cujEnv{t: t, plat: p.stubPlatformEngine}
	for _, tc := range []struct {
		id          string
		privateChat bool
		wantPrompt  string
	}{
		{id: "private-chat", privateChat: true, wantPrompt: "private rule\n\nrun task"},
		{id: "watched-group", wantPrompt: "group rule\n\nrun task"},
	} {
		msg := messageTriggerTestMessage(p, tc.id, "run task", false)
		msg.SessionKey, msg.Trigger.PrivateChat = tc.id, tc.privateChat
		e.ReceiveMessage(p, msg)
		env.waitFor("agent result for "+tc.id, 3*time.Second, func() bool {
			return slices.Contains(p.getSent(), "source:"+tc.id+" -> ok")
		})
		agent.mu.Lock()
		prompts := agent.sessions[len(agent.sessions)-1].getSentPrompts()
		agent.mu.Unlock()
		if !slices.Equal(prompts, []string{tc.wantPrompt}) {
			t.Fatalf("%s prompt = %q, want %q", tc.id, prompts, tc.wantPrompt)
		}
	}

	rules[0].TriggerMode = "manual"
	e.SetAdminFrom("admin")
	e.SetMessageTriggers(rules, MessageTriggerModel{})
	denied := messageTriggerTestMessage(p, "private-denied", "run task", true)
	denied.Trigger.PrivateChat = true
	e.ReceiveMessage(p, denied)
	if len(p.cards) != 0 || !slices.ContainsFunc(p.getSent(), func(reply string) bool {
		return strings.Contains(reply, e.i18n.T(MsgMessageTriggerAdminOnly))
	}) {
		t.Fatalf("non-admin private chat should receive an error instead of a confirmation: %q", p.getSent())
	}

	admin := messageTriggerTestMessage(p, "private-admin", "run task", true)
	admin.Trigger.PrivateChat = true
	admin.UserID = "admin"
	e.ReceiveMessage(p, admin)
	if card := p.readCard(t); card.reply != "source:private-admin" {
		t.Fatalf("private confirmation reply = %q", card.reply)
	}
}

func TestMessageTriggerChatScopes(t *testing.T) {
	at := time.Now()
	for _, tc := range []struct {
		name         string
		scope        *MessageTriggerCondition
		private      bool
		listedGroup  bool
		anotherGroup bool
	}{
		{name: "implicit all", private: true, listedGroup: true, anotherGroup: true},
		{name: "all chats", scope: &MessageTriggerCondition{Type: "chat_scope", Value: "all"}, private: true, listedGroup: true, anotherGroup: true},
		{name: "all with group IDs", scope: &MessageTriggerCondition{Type: "chat_scope", Value: "all", Values: []string{"listed"}}, private: true, listedGroup: true},
		{name: "private chats", scope: &MessageTriggerCondition{Type: "chat_scope", Value: "private"}, private: true},
		{name: "all groups", scope: &MessageTriggerCondition{Type: "chat_scope", Value: "group"}, listedGroup: true, anotherGroup: true},
		{name: "listed groups", scope: &MessageTriggerCondition{Type: "chat_scope", Value: "group", Values: []string{"listed"}}, listedGroup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conditions := []MessageTriggerCondition{{Type: "regex", Value: "^record:"}}
			if tc.scope != nil {
				conditions = append(conditions, *tc.scope)
			}
			rule, err := normalizeMessageTriggerRule(MessageTriggerRule{Enabled: true, Platform: "test",
				Groups: []MessageTriggerGroup{{Match: "or", Conditions: conditions}},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, chat := range []struct {
				event MessageTriggerEvent
				want  bool
			}{
				{MessageTriggerEvent{ChatID: "direct", PrivateChat: true, Body: "record: test"}, tc.private},
				{MessageTriggerEvent{ChatID: "listed", Body: "record: test"}, tc.listedGroup},
				{MessageTriggerEvent{ChatID: "another", Body: "record: test"}, tc.anotherGroup},
				{MessageTriggerEvent{ChatID: "listed", Body: "ordinary message"}, false},
			} {
				if got := rule.watches("test", &chat.event) && rule.matchesGroups(&chat.event, at); got != chat.want {
					t.Errorf("route for %+v = %v, want %v", chat.event, got, chat.want)
				}
			}
		})
	}
}

const messageTriggerCapturePattern = `^run task ([^|]+)\|([^|]+)\|(.)(.)(.)(.)(.)(.)(.)(.+)$`

func TestMessageTriggerAgentExpandsCapturesAndSourceBody(t *testing.T) {
	p := newMessageTriggerTestPlatform()
	agent := &cujAgent{}
	e := NewEngine("agent-template-project", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetReplyFooterEnabled(false)
	e.SetMessageTriggers([]MessageTriggerRule{{
		Name: "agent task", Enabled: true, Platform: p.Name(), TriggerMode: "direct", Action: "agent",
		Groups: []MessageTriggerGroup{{Match: "and", Conditions: []MessageTriggerCondition{
			{Type: "group", Values: []string{"group"}}, {Type: "regex", Value: messageTriggerCapturePattern},
		}}},
		Prompt: "First: <$1>\nSecond: <$2>\nTenth: <$10>\nSource: <${message}>",
	}}, MessageTriggerModel{})
	first := `two words with ' and " and $HOME`
	second := "first line\nsecond line ${message}"
	tenth := "literal $2"
	body := "run task " + first + "|" + second + "|1234567" + tenth
	msg := messageTriggerTestMessage(p, "agent", body, false)
	msg.Content = "platform-enriched text"
	msg.ExtraContent = "quoted context"
	e.ReceiveMessage(p, msg)
	env := &cujEnv{t: t, plat: p.stubPlatformEngine}
	env.waitFor("agent result quoting the source", 3*time.Second, func() bool {
		return slices.Contains(p.getSent(), "source:agent -> ok")
	})
	agent.mu.Lock()
	prompts := agent.sessions[0].getSentPrompts()
	agent.mu.Unlock()
	want := fmt.Sprintf("First: <%s>\nSecond: <%s>\nTenth: <%s>\nSource: <%s>\n\nquoted context\nplatform-enriched text", first, second, tenth, body)
	if !slices.Equal(prompts, []string{want}) {
		t.Fatalf("agent prompt = %q, want %q", prompts, want)
	}
}

func TestMessageTriggerAdminConfirmationAndShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX printf")
	}
	p := newMessageTriggerTestPlatform()
	e := NewEngine("confirm-project", &stubAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	e.SetShell("sh", "-c", "")
	e.SetReplyFooterEnabled(true)
	e.SetShowContextIndicator(true)
	e.SetShowWorkdirIndicator(true)
	e.SetAdminFrom("admin, second-admin")
	rule := MessageTriggerRule{
		Name: "shell task", Enabled: true, Platform: p.Name(), TriggerMode: "manual",
		Groups: []MessageTriggerGroup{{Match: "and", Conditions: []MessageTriggerCondition{
			{Type: "group", Values: []string{"group"}}, {Type: "regex", Value: messageTriggerCapturePattern},
		}}},
		Command: `printf '<%s>\n' $1 $2 $10 ${message}`, Action: "shell",
	}
	e.SetMessageTriggers([]MessageTriggerRule{rule}, MessageTriggerModel{})
	e.ReceiveMessage(p, messageTriggerTestMessage(p, "reject", "run task denied|two|1234567ten", false))
	rejected := p.readCard(t)
	if rejected.reply != "source:reject" || !slices.Equal(rejected.admins, []string{"admin", "second-admin"}) || len(p.getSent()) != 0 {
		t.Fatalf("wrong card routing or premature run: %+v messages=%v", rejected, p.getSent())
	}
	if _, resolved := p.confirm(rejected.id, "group", "sender", true); resolved {
		t.Fatal("non-admin resolved the confirmation")
	}
	if _, resolved := p.confirm(rejected.id, "other-group", "admin", true); resolved {
		t.Fatal("another group resolved the confirmation")
	}
	if _, resolved := p.confirm(rejected.id, "group", "admin", false); !resolved {
		t.Fatal("admin could not decline")
	}
	if _, resolved := p.confirm(rejected.id, "group", "admin", true); resolved {
		t.Fatal("declined request was reusable")
	}

	rule.CCEnabled = true
	rule.CCUserIDs = []string{"one", "two"}
	e.SetMessageTriggers([]MessageTriggerRule{rule}, MessageTriggerModel{})
	first := `two words with ' and " and $HOME; printf bypass`
	second := "first line\nsecond line ${message}"
	tenth := "literal $2"
	body := "run task " + first + "|" + second + "|1234567" + tenth
	msg := messageTriggerTestMessage(p, "approve", body, true)
	msg.Content = "platform-enriched text"
	msg.ExtraContent = "quoted context"
	e.ReceiveMessage(p, msg)
	approved := p.readCard(t)
	if _, resolved := p.confirm(approved.id, "group", "admin", true); !resolved {
		t.Fatal("admin could not approve")
	}
	if _, resolved := p.confirm(approved.id, "group", "admin", true); resolved {
		t.Fatal("confirmation ran twice")
	}
	env := &cujEnv{t: t, plat: p.stubPlatformEngine}
	env.waitFor("configured shell result quoting the source", 3*time.Second, func() bool {
		return len(p.getSent()) != 0
	})
	want := []string{fmt.Sprintf("source:approve cc one two -> <%s>\n<%s>\n<%s>\n<%s>", first, second, tenth, body)}
	if got := p.getSent(); !slices.Equal(got, want) {
		t.Fatalf("shell reply with global footer enabled = %q, want %q", got, want)
	}

	e.ReceiveMessage(p, messageTriggerTestMessage(p, "recalled", "run task recalled|two|1234567ten", false))
	recalled := p.readCard(t)
	e.ReceiveMessage(p, &Message{MessageID: "recalled", Platform: p.Name(), Recalled: true})
	if _, resolved := p.confirm(recalled.id, "group", "admin", true); resolved {
		t.Fatal("admin could approve a recalled source")
	}

	msg = messageTriggerTestMessage(p, "missing-workspace", body, false)
	msg.ChannelKey = "stale-binding"
	e.workspaceBindings.Bind("project:"+e.name, e.effectiveWorkspaceChannelKey(msg), "Missing workspace", filepath.Join(t.TempDir(), "missing"))
	e.ReceiveMessage(p, msg)
	got := p.getSent()
	if len(got) != 2 || !strings.Contains(got[1], e.i18n.T(MsgWsNotFoundHintGitOnly)) {
		t.Fatalf("an unavailable binding should prompt for workspace setup, got %q", got)
	}
}

func TestMessageTriggerSenderWithoutChatAccessDoesNotEnterWorkspaceSetup(t *testing.T) {
	p := newMessageTriggerTestPlatform()
	e := NewEngine("trigger-workspace-project", &stubAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	e.SetMessageTriggers([]MessageTriggerRule{{
		Name: "agent task", Enabled: true, Platform: p.Name(), Action: "agent",
		Groups: []MessageTriggerGroup{{Conditions: []MessageTriggerCondition{{Type: "regex", Value: "^run task$"}}}},
	}}, MessageTriggerModel{})

	msg := messageTriggerTestMessage(p, "rule-only", "run task", false)
	msg.Trigger.ChatAccessDenied = true
	e.ReceiveMessage(p, msg)
	if got := p.getSent(); !slices.Equal(got, []string{"rule-only -> " + e.i18n.T(MsgWsNoBinding)}) {
		t.Fatalf("expected only a missing workspace reply, got %q", got)
	}
	if len(e.initFlows) != 0 {
		t.Fatal("a rule-only message opened the ordinary workspace setup flow")
	}
}

func TestMessageTriggerShellDoesNotRunWithoutMatchedCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX printf")
	}
	p := newMessageTriggerTestPlatform()
	e := NewEngine("missing-capture-project", &stubAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetShell("sh", "-c", "")
	e.SetMessageTriggers([]MessageTriggerRule{{
		Name: "shell task with alternatives", Enabled: true, Platform: p.Name(), TriggerMode: "direct", GroupMatch: "and",
		Groups: []MessageTriggerGroup{
			{Match: "and", Conditions: []MessageTriggerCondition{{Type: "group", Values: []string{"group"}}}},
			{Match: "or", Conditions: []MessageTriggerCondition{
				{Type: "regex", Value: `^run task (.+)$`}, {Type: "sender", Values: []string{"sender"}},
			}},
		},
		Command: `printf 'command executed <%s>' $1`, Action: "shell",
	}}, MessageTriggerModel{})
	e.ReceiveMessage(p, messageTriggerTestMessage(p, "missing", "only the sender condition matches", false))
	env := &cujEnv{t: t, plat: p.stubPlatformEngine}
	env.waitFor("missing capture error", 3*time.Second, func() bool {
		return len(p.getSent()) != 0
	})
	got := p.getSent()
	if len(got) != 1 || !strings.Contains(got[0], "capture $1 is unavailable") || strings.Contains(got[0], "command executed") {
		t.Fatalf("missing capture should prevent shell execution, got %q", got)
	}
}

func TestMessageTriggerShellBindingsRequireArgumentPosition(t *testing.T) {
	for _, command := range []string{`$1`, `printf fixed | "${message}"`, `printf fixed > $1`} {
		_, _, err := bindMessageTriggerShell(command, "sh", "-c", []string{"words with spaces"}, "current message")
		if err == nil || !strings.Contains(err.Error(), "argument to a fixed command") {
			t.Errorf("binding %q = %v, want argument position error", command, err)
		}
	}
}
