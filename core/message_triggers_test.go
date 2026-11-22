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
	if !rule.watches("test", "ops") || rule.watches("test", "other") || !rule.matchesGroups(operator, at) {
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

func TestMessageTriggerAdminConfirmationAndShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX printf")
	}
	p := newMessageTriggerTestPlatform()
	e := NewEngine("confirm-project", &stubAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetReplyFooterEnabled(true)
	e.SetShowContextIndicator(true)
	e.SetShowWorkdirIndicator(true)
	e.SetAdminFrom("admin, second-admin")
	rule := MessageTriggerRule{
		Name: "shell task", Enabled: true, Platform: p.Name(), TriggerMode: "manual",
		Groups: []MessageTriggerGroup{{Match: "and", Conditions: []MessageTriggerCondition{
			{Type: "group", Values: []string{"group"}}, {Type: "regex", Value: "run task"},
		}}},
		Command: "printf 'configured-command'", Action: "shell",
	}
	e.SetMessageTriggers([]MessageTriggerRule{rule}, MessageTriggerModel{})
	e.ReceiveMessage(p, messageTriggerTestMessage(p, "reject", "run task", false))
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
	e.ReceiveMessage(p, messageTriggerTestMessage(p, "approve", "run task; printf bypass", false))
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
	want := []string{"source:approve cc one two -> configured-command"}
	if got := p.getSent(); !slices.Equal(got, want) {
		t.Fatalf("shell reply with global footer enabled = %q, want %q", got, want)
	}

	e.ReceiveMessage(p, messageTriggerTestMessage(p, "recalled", "run task", false))
	recalled := p.readCard(t)
	e.ReceiveMessage(p, &Message{MessageID: "recalled", Platform: p.Name(), Recalled: true})
	if _, resolved := p.confirm(recalled.id, "group", "admin", true); resolved {
		t.Fatal("admin could approve a recalled source")
	}
}
