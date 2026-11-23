package config

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

const messageTriggerConfigFixture = `# Global comments must survive a rule edit.
future_top_level = "keep this"   # Unknown global setting.

[message_trigger_model]
# Keep the key as an environment variable reference.
base_url = "${CC_TRIGGER_TEST_BASE_URL}"
model = "${CC_TRIGGER_TEST_MODEL}"
api_key_env = "CC_TRIGGER_TEST_KEY"
timeout_seconds = 12
future_model_option = "keep this too"

[[projects]]
name = "alpha"
admin_from = "${CC_TRIGGER_TEST_ADMINS}"
future_project_field = ["active", "archived"]

[projects.agent]
type = "codex"

[[projects.platforms]]
type = "lark"

[projects.platforms.options]
app_secret = "${CC_TRIGGER_TEST_APP_SECRET}"
future_option = 4

[[projects.platforms]]
type = "telegram"

[[projects.message_triggers]]
name = "Old alpha rule"
enabled = true
platform = "lark"
chat_ids = ["oc_alpha"]
sender_ids = ["ou_legacy_sender"]
mentioned_ids = ["ou_legacy_target"]
keywords = ["old alpha", "a+b?"]
intent = "The sender reports an old incident"
match = "or"
action = "agent"

# The next project has its own rule.
[[projects]]
name = "beta"

[projects.agent]
type = "codex"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "${CC_TRIGGER_TEST_BETA_TOKEN}"

[[projects.platforms]]
type = "slack"

[[projects.message_triggers]]
name = "Beta rule"
enabled = true
platform = "telegram"
action = "agent"
future_rule_field = "untouched" # Beta rule note.

[[projects.message_triggers.groups]]
match = "and"

[[projects.message_triggers.groups.conditions]]
type = "group"
values = ["-1009876"]

[[projects.message_triggers.groups.conditions]]
type = "regex"
value = "ping beta"

[future_notes]
description = '''
This is a multiline value.
[looks_like_a_table]
'''
# Keep the document footer.
`

func setupMessageTriggerConfig(t *testing.T, contents string) {
	t.Helper()
	t.Setenv("CC_TRIGGER_TEST_BASE_URL", "https://example.invalid/v1")
	t.Setenv("CC_TRIGGER_TEST_MODEL", "intent-test")
	t.Setenv("CC_TRIGGER_TEST_APP_SECRET", "example-secret")
	t.Setenv("CC_TRIGGER_TEST_BETA_TOKEN", "example-token")
	t.Setenv("CC_TRIGGER_TEST_ADMINS", "12345, ou_approval_admin")
	writeTestConfig(t, contents)
}

func readMessageTriggerConfig(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return data
}

func messageTriggerConfigDocument(t *testing.T, data []byte) (map[string]any, []map[string]any) {
	t.Helper()
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse raw TOML: %v", err)
	}
	projects, ok := document["projects"].([]map[string]any)
	if !ok || len(projects) != 2 {
		t.Fatalf("expected two project tables, got %#v", document["projects"])
	}
	return document, projects
}

func getMessageTriggerRules(t *testing.T, project string) ([]core.MessageTriggerRule, core.MessageTriggerModel) {
	t.Helper()
	rules, model, err := GetProjectMessageTriggers(project)
	if err != nil {
		t.Fatalf("GetProjectMessageTriggers(%q): %v", project, err)
	}
	return rules, model
}

func triggerTestGroup(conditions ...core.MessageTriggerCondition) []core.MessageTriggerGroup {
	return []core.MessageTriggerGroup{{Match: "and", Conditions: conditions}}
}

func findTriggerTestCondition(rule core.MessageTriggerRule, conditionType string) core.MessageTriggerCondition {
	for _, group := range rule.Groups {
		for _, condition := range group.Conditions {
			if condition.Type == conditionType {
				return condition
			}
		}
	}
	return core.MessageTriggerCondition{}
}

func TestMessageTriggerConfigRoundTrip(t *testing.T) {
	setupMessageTriggerConfig(t, messageTriggerConfigFixture)
	before, beforeProjects := messageTriggerConfigDocument(t, readMessageTriggerConfig(t))
	alphaBefore, model := getMessageTriggerRules(t, "alpha")
	betaBefore, _ := getMessageTriggerRules(t, "beta")
	wantModel := core.MessageTriggerModel{BaseURL: "https://example.invalid/v1", Model: "intent-test", APIKeyEnv: "CC_TRIGGER_TEST_KEY", TimeoutSeconds: 12}
	if model != wantModel {
		t.Fatalf("model = %+v, want %+v", model, wantModel)
	}
	if len(alphaBefore) != 1 || alphaBefore[0].Name != "Old alpha rule" || len(alphaBefore[0].Groups) == 0 {
		t.Fatalf("existing rules were not read and normalized: %#v", alphaBefore)
	}
	pattern := findTriggerTestCondition(alphaBefore[0], "regex").Value
	legacyRegex, err := regexp.Compile(pattern)
	if err != nil || pattern == "" {
		t.Fatalf("legacy keywords were not converted to a valid expression: %q, %v", pattern, err)
	}
	if !legacyRegex.MatchString("an OLD AlPhA report") || !legacyRegex.MatchString("literal A+B? example") || legacyRegex.MatchString("expanded aaab example") {
		t.Fatalf("legacy keywords must remain case-insensitive literal alternatives: %q", pattern)
	}
	if values := findTriggerTestCondition(alphaBefore[0], "group").Values; !reflect.DeepEqual(values, []string{"oc_alpha"}) {
		t.Fatalf("legacy group scope changed: %#v", values)
	}
	legacyGroups := alphaBefore[0].Groups
	if alphaBefore[0].GroupMatch != "and" || len(legacyGroups) != 2 ||
		legacyGroups[0].Match != "and" || len(legacyGroups[0].Conditions) != 3 ||
		len(legacyGroups[1].Conditions) != 1 {
		t.Fatalf("legacy identity and content matches are no longer separated: %#v", legacyGroups)
	}
	if alphaBefore[0].TriggerMode != "intent" || alphaBefore[0].IntentPrompt != "The sender reports an old incident" ||
		findTriggerTestCondition(alphaBefore[0], "intent").Type != "" {
		t.Fatalf("legacy intent was not migrated to the trigger mode: %#v", alphaBefore[0])
	}

	input := []core.MessageTriggerRule{
		{
			Name: " Review ", Enabled: true, Platform: " LARK ", GroupMatch: " OR ",
			Groups: []core.MessageTriggerGroup{
				{Match: " AND ", Conditions: []core.MessageTriggerCondition{
					{Type: "group", Values: []string{" oc_alpha ", "oc_second", "oc_alpha"}},
					{Type: "sender", Values: []string{"ou_sender"}},
					{Type: "regex", Value: "(?i:assess|review)"},
				}},
				{Match: " OR ", Conditions: []core.MessageTriggerCondition{
					{Type: "mentioned", Values: []string{"ou_target"}},
					{Type: "regex", Value: "review"},
				}},
			},
			CCEnabled: true, CCUserIDs: []string{" ou_copy_user ", "ou_copy_user"},
			Action: "agent", TriggerMode: " MANUAL ", Prompt: "Remember ${CC_TRIGGER_TEST_MODEL} as text.",
		},
		{
			Name: "Export", Platform: "telegram",
			Groups: triggerTestGroup(
				core.MessageTriggerCondition{Type: "group", Values: []string{"-1001234"}},
				core.MessageTriggerCondition{Type: "regex", Value: "^export$"},
			),
			CCUserIDs: []string{"24680"}, Action: "shell", Command: `printf '%s\n' "${CC_TRIGGER_TEST_MODEL}"`,
		},
	}
	want := []core.MessageTriggerRule{
		{
			Name: "Review", Enabled: true, Platform: "lark", GroupMatch: "or",
			Groups: []core.MessageTriggerGroup{
				{Match: "and", Conditions: []core.MessageTriggerCondition{
					{Type: "group", Values: []string{"oc_alpha", "oc_second"}},
					{Type: "sender", Values: []string{"ou_sender"}},
					{Type: "regex", Value: "(?i:assess|review)"},
				}},
				{Match: "or", Conditions: input[0].Groups[1].Conditions},
			},
			CCEnabled: true, CCUserIDs: []string{"ou_copy_user"}, Action: "agent", TriggerMode: "manual", Prompt: input[0].Prompt,
		},
		{
			Name: "Export", Platform: "telegram", GroupMatch: "or", Groups: input[1].Groups,
			CCUserIDs: []string{"24680"}, Action: "shell", TriggerMode: "direct", Command: input[1].Command,
		},
	}
	wantRevision := core.MessageTriggerRulesRevision(want)
	got, savedModel, err := SaveProjectMessageTriggers("alpha", core.MessageTriggerRulesRevision(alphaBefore), input)
	if err != nil || core.MessageTriggerRulesRevision(got) != wantRevision || savedModel != model {
		t.Fatalf("save = %#v, %+v, %v; want %#v and %+v", got, savedModel, err, want, model)
	}
	got, model = getMessageTriggerRules(t, "alpha")
	if core.MessageTriggerRulesRevision(got) != wantRevision || model != savedModel {
		t.Fatalf("read after save = %#v and %+v; want %#v and %+v", got, model, want, savedModel)
	}
	if got, _ := getMessageTriggerRules(t, "beta"); !reflect.DeepEqual(got, betaBefore) {
		t.Fatalf("beta rules changed: got %#v, want %#v", got, betaBefore)
	}
	contents := readMessageTriggerConfig(t)
	after, afterProjects := messageTriggerConfigDocument(t, contents)
	delete(beforeProjects[0], "message_triggers")
	delete(afterProjects[0], "message_triggers")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("unrelated TOML changed, including unknown fields or environment references:\nbefore = %#v\nafter = %#v", before, after)
	}
	prefix, _, _ := strings.Cut(messageTriggerConfigFixture, "[[projects.message_triggers]]")
	suffix := messageTriggerConfigFixture[strings.Index(messageTriggerConfigFixture, "# The next project"):]
	if !bytes.HasPrefix(contents, []byte(prefix)) || !bytes.HasSuffix(contents, []byte(suffix)) {
		t.Fatal("saving a standard rule block changed the global configuration, comments, or another project")
	}
	next := []core.MessageTriggerRule{want[1]}
	next[0].Name = "Updated export"
	replaced, _, err := SaveProjectMessageTriggers("alpha", core.MessageTriggerRulesRevision(got), next)
	if err != nil || core.MessageTriggerRulesRevision(replaced) != core.MessageTriggerRulesRevision(next) {
		t.Fatalf("replace nested rules = %#v, %v", replaced, err)
	}
	contents = readMessageTriggerConfig(t)
	if !bytes.HasPrefix(contents, []byte(prefix)) || !bytes.HasSuffix(contents, []byte(suffix)) || bytes.Contains(contents, []byte("ou_target")) {
		t.Fatal("replacing nested tables left old conditions or changed the configuration around them")
	}
}

func TestMessageTriggerConfigRuleValuesStayLiteral(t *testing.T) {
	t.Setenv("CC_TRIGGER_TEST_PROJECT", "demo")
	t.Setenv("CC_TRIGGER_TEST_PLATFORM", "feishu")
	t.Setenv("CC_TRIGGER_TEST_CHAT", "oc_resolved")
	writeTestConfig(t, `
[[projects]]
name = "${CC_TRIGGER_TEST_PROJECT}"
agent = { type = "codex" }
platforms = [{ type = "${CC_TRIGGER_TEST_PLATFORM}" }]

[[projects.message_triggers]]
name = "Rule name"
enabled = true
platform = "feishu"
action = "agent"

[[projects.message_triggers.groups]]

[[projects.message_triggers.groups.conditions]]
type = "group"
values = ["${CC_TRIGGER_TEST_CHAT}"]

[[projects.message_triggers.groups.conditions]]
type = "regex"
value = ' \$\{CC_TRIGGER_TEST_PROJECT\} '
`)
	rules, _ := getMessageTriggerRules(t, "demo")
	if len(rules) != 1 ||
		!reflect.DeepEqual(findTriggerTestCondition(rules[0], "group").Values, []string{"${CC_TRIGGER_TEST_CHAT}"}) ||
		findTriggerTestCondition(rules[0], "regex").Value != ` \$\{CC_TRIGGER_TEST_PROJECT\} ` {
		t.Fatalf("rule selectors unexpectedly changed: %#v", rules)
	}
	updated := rules[0]
	updated.Name = "Updated name"
	if _, _, err := SaveProjectMessageTriggers("demo", core.MessageTriggerRulesRevision(rules), []core.MessageTriggerRule{updated}); err != nil {
		t.Fatalf("save after reading rule with literal selectors: %v", err)
	}
	cfg, err := Load(ConfigPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := cfg.Projects[0].MessageTriggers; !reflect.DeepEqual(got, []core.MessageTriggerRule{updated}) {
		t.Fatalf("rule after reload = %#v, want %#v", got, updated)
	}
}

func TestMessageTriggerConfigWriteSupportsAlternateTOMLForms(t *testing.T) {
	const otherSection = `
[custom]
message = '''
Unrelated multiline content.
[[projects]]
'''
settings = { enabled = false, labels = ["x", "y"] }
`
	for name, source := range map[string]string{
		"quoted project and rule keys": `
[["projects"]]
name = "demo"
agent = { type = "codex" }
platforms = [{ type = "feishu" }]
"message_triggers" = [{ name = "Before", platform = "feishu", chat_ids = ["oc_example"], regex = "ping", action = "agent" }]
`,
		"inline projects array": `
projects = [{ name = "demo", agent = { type = "codex" }, platforms = [{ type = "feishu" }] }]
`,
		"standard rules with misleading multiline header": `
[[projects]]
name = "demo"
agent = { type = "codex" }
platforms = [{ type = "feishu" }]

[[projects.message_triggers]]
name = "Before"
platform = "feishu"
chat_ids = ["oc_example"]
regex = "ping"
action = "agent"
`,
	} {
		t.Run(name, func(t *testing.T) {
			writeTestConfig(t, source+otherSection)
			current, _ := getMessageTriggerRules(t, "demo")
			updated := core.MessageTriggerRule{
				Name: "After", Enabled: true, Platform: "feishu", GroupMatch: "or", Action: "agent", TriggerMode: "direct",
				Groups: triggerTestGroup(
					core.MessageTriggerCondition{Type: "group", Values: []string{"oc_example"}},
					core.MessageTriggerCondition{Type: "regex", Value: "ping"},
				),
			}
			if _, _, err := SaveProjectMessageTriggers("demo", core.MessageTriggerRulesRevision(current), []core.MessageTriggerRule{updated}); err != nil {
				t.Fatalf("save rule: %v", err)
			}
			if got, _ := getMessageTriggerRules(t, "demo"); core.MessageTriggerRulesRevision(got) != core.MessageTriggerRulesRevision([]core.MessageTriggerRule{updated}) {
				t.Fatalf("loaded rules = %#v, want %#v", got, updated)
			}
			var before, after map[string]any
			if err := toml.Unmarshal([]byte(otherSection), &before); err != nil {
				t.Fatalf("parse original custom section: %v", err)
			}
			if err := toml.Unmarshal(readMessageTriggerConfig(t), &after); err != nil {
				t.Fatalf("parse updated config: %v", err)
			}
			if !reflect.DeepEqual(before["custom"], after["custom"]) {
				t.Fatalf("unrelated multiline or inline values changed: before = %#v, after = %#v", before["custom"], after["custom"])
			}
		})
	}
}

func TestMessageTriggerConfigConcurrentRevision(t *testing.T) {
	setupMessageTriggerConfig(t, messageTriggerConfigFixture)
	current, _ := getMessageTriggerRules(t, "alpha")
	if len(current) != 1 {
		t.Fatalf("expected one starting rule, got %#v", current)
	}
	revision := core.MessageTriggerRulesRevision(current)
	start := make(chan struct{})
	type result struct {
		name string
		err  error
	}
	results := make(chan result, 2)
	for _, name := range []string{"Update A", "Update B"} {
		next := current[0]
		next.Name = name
		go func() {
			<-start
			_, _, err := SaveProjectMessageTriggers("alpha", revision, []core.MessageTriggerRule{next})
			results <- result{name: name, err: err}
		}()
	}
	close(start)
	var winner string
	conflicts := 0
	for _, got := range []result{<-results, <-results} {
		switch {
		case got.err == nil:
			if winner != "" {
				t.Fatalf("both saves with revision %s succeeded", revision)
			}
			winner = got.name
		case errors.Is(got.err, core.ErrMessageTriggerRulesChanged):
			conflicts++
		default:
			t.Fatalf("unexpected error from %s: %v", got.name, got.err)
		}
	}
	got, _ := getMessageTriggerRules(t, "alpha")
	if conflicts != 1 || winner == "" || len(got) != 1 || got[0].Name != winner {
		t.Fatalf("winner = %q, conflicts = %d, stored rules = %#v", winner, conflicts, got)
	}
}

func TestMessageTriggerConfigDeleteAndRecreate(t *testing.T) {
	setupMessageTriggerConfig(t, messageTriggerConfigFixture)
	alpha, _ := getMessageTriggerRules(t, "alpha")
	beta, _ := getMessageTriggerRules(t, "beta")
	result, _, err := SaveProjectMessageTriggers("alpha", core.MessageTriggerRulesRevision(alpha), []core.MessageTriggerRule{})
	if err != nil || len(result) != 0 {
		t.Fatalf("delete rules = %#v, %v", result, err)
	}
	if got, _ := getMessageTriggerRules(t, "alpha"); len(got) != 0 {
		t.Fatalf("deleted rules reappeared: %#v", got)
	}
	if got, _ := getMessageTriggerRules(t, "beta"); !reflect.DeepEqual(got, beta) {
		t.Fatalf("deleting alpha rules changed beta: %#v", got)
	}
	_, projects := messageTriggerConfigDocument(t, readMessageTriggerConfig(t))
	if _, keyExists := projects[0]["message_triggers"]; keyExists {
		t.Fatal("deleted rule table still present in TOML")
	}
	if _, _, err := SaveProjectMessageTriggers("alpha", core.MessageTriggerRulesRevision(result), alpha); err != nil {
		t.Fatalf("create rule in project with no rules: %v", err)
	}
	if got, _ := getMessageTriggerRules(t, "alpha"); !reflect.DeepEqual(got, alpha) {
		t.Fatalf("recreated rules = %#v, want %#v", got, alpha)
	}
	_, projects = messageTriggerConfigDocument(t, readMessageTriggerConfig(t))
	tables, ok := projects[0]["message_triggers"].([]map[string]any)
	if !ok || len(tables) != 1 || tables[0]["groups"] == nil {
		t.Fatalf("recreating a legacy rule did not write the grouped conditions: %#v", projects[0]["message_triggers"])
	}
	if _, exists := tables[0]["keywords"]; exists {
		t.Fatal("saving a legacy rule left the keywords field on disk")
	}
	if _, _, err := SaveProjectMessageTriggers("alpha", core.MessageTriggerRulesRevision(alpha), nil); err != nil {
		t.Fatalf("delete the rule after migration to nested tables: %v", err)
	}
	contents := readMessageTriggerConfig(t)
	prefix, _, _ := strings.Cut(messageTriggerConfigFixture, "[[projects.message_triggers]]")
	suffix := messageTriggerConfigFixture[strings.Index(messageTriggerConfigFixture, "# The next project"):]
	if !bytes.HasPrefix(contents, []byte(prefix)) || !bytes.HasSuffix(contents, []byte(suffix)) {
		t.Fatal("deleting nested rules changed the surrounding configuration")
	}
	_, projects = messageTriggerConfigDocument(t, contents)
	if _, exists := projects[0]["message_triggers"]; exists {
		t.Fatal("deleting migrated rules left their nested tables on disk")
	}
}

func TestMessageTriggerConfigRejectsInvalidRules(t *testing.T) {
	setupMessageTriggerConfig(t, messageTriggerConfigFixture)
	original := readMessageTriggerConfig(t)
	tests := []struct {
		name     string
		project  string
		platform string
		groups   []core.MessageTriggerGroup
		wantErr  string
	}{
		{"requires a condition group", "alpha", "lark", nil, "condition group"},
		{"channel enabled in another project", "beta", "lark", triggerTestGroup(core.MessageTriggerCondition{Type: "group", Values: []string{"oc_alpha"}}), "channel configured for this project"},
		{"channel does not support triggers", "beta", "slack", triggerTestGroup(core.MessageTriggerCondition{Type: "group", Values: []string{"C123"}}), "channel configured for this project"},
		{"DingTalk channel not configured", "beta", "dingtalk", triggerTestGroup(core.MessageTriggerCondition{Type: "regex", Value: "^ping$"}), "channel configured for this project"},
		{"requires a condition in the group", "alpha", "lark", triggerTestGroup(), "condition"},
		{"rejects unsupported expressions", "alpha", "lark", triggerTestGroup(
			core.MessageTriggerCondition{Type: "group", Values: []string{"oc_alpha"}},
			core.MessageTriggerCondition{Type: "regex", Value: "(?=ping)"},
		), "regex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, _ := getMessageTriggerRules(t, tt.project)
			invalid := core.MessageTriggerRule{
				Name: "Invalid", Platform: tt.platform, Groups: tt.groups,
				Action: "agent",
			}
			_, _, err := SaveProjectMessageTriggers(tt.project, core.MessageTriggerRulesRevision(rules), []core.MessageTriggerRule{invalid})
			if !errors.Is(err, core.ErrInvalidMessageTriggerRules) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("SaveProjectMessageTriggers() = %v, want invalid rules with %q", err, tt.wantErr)
			}
			if got := readMessageTriggerConfig(t); !bytes.Equal(got, original) {
				t.Fatal("invalid save changed the configuration file")
			}
		})
	}
}

func TestMessageTriggerConfigChecksConfirmationAndCCIDs(t *testing.T) {
	const fixture = `
[[projects]]
name = "demo"
admin_from = "${CC_TRIGGER_TEST_ADMINS}"
agent = { type = "codex" }
platforms = [{ type = "feishu" }, { type = "lark" }, { type = "telegram" }]
`
	for _, tc := range []struct {
		name     string
		admins   string
		platform string
		rule     core.MessageTriggerRule
		wantErr  string
	}{
		{name: "confirmation rejects wildcard alone", admins: "*", platform: "feishu", rule: core.MessageTriggerRule{Enabled: true, TriggerMode: "manual"}, wantErr: "admin_from"},
		{name: "confirmation rejects wrong platform", admins: "ou_wrong_platform", platform: "telegram", rule: core.MessageTriggerRule{Enabled: true, TriggerMode: "manual"}, wantErr: "admin_from"},
		{name: "confirmation accepts explicit platform ID", admins: "ou_other, 12345", platform: "telegram", rule: core.MessageTriggerRule{Enabled: true, TriggerMode: "manual"}},
		{name: "disabled confirmation allows editing without an admin", platform: "feishu", rule: core.MessageTriggerRule{TriggerMode: "manual"}},
		{name: "CC enabled requires a recipient", platform: "lark", rule: core.MessageTriggerRule{CCEnabled: true}, wantErr: "cc_user_ids"},
		{name: "CC rejects invalid OpenID", platform: "lark", rule: core.MessageTriggerRule{CCEnabled: true, CCUserIDs: []string{"ou_<invalid>"}}, wantErr: "cc_user_ids"},
		{name: "CC rejects non-canonical Telegram ID", platform: "telegram", rule: core.MessageTriggerRule{CCEnabled: true, CCUserIDs: []string{"012345"}}, wantErr: "cc_user_ids"},
		{name: "CC rejects an invalid saved ID when disabled", platform: "telegram", rule: core.MessageTriggerRule{CCUserIDs: []string{"-100123"}}, wantErr: "cc_user_ids"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CC_TRIGGER_TEST_ADMINS", tc.admins)
			writeTestConfig(t, fixture)
			rule := tc.rule
			rule.Name, rule.Platform, rule.Action = "Check", tc.platform, "agent"
			groupID := "oc_test_group"
			if tc.platform == "telegram" {
				groupID = "-1001234"
			}
			rule.Groups = triggerTestGroup(core.MessageTriggerCondition{Type: "group", Values: []string{groupID}})
			next := []core.MessageTriggerRule{rule}
			saved, _, err := SaveProjectMessageTriggers("demo", core.MessageTriggerRulesRevision(nil), next)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if loaded, _ := getMessageTriggerRules(t, "demo"); !reflect.DeepEqual(saved, loaded) {
					t.Fatalf("loaded rules = %#v, want %#v", loaded, saved)
				}
				return
			}
			if !errors.Is(err, core.ErrInvalidMessageTriggerRules) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("saving invalid rule = %v, want %q", err, tc.wantErr)
			}
			if actual := string(readMessageTriggerConfig(t)); actual != fixture {
				t.Fatal("invalid save changed the configuration file")
			}
			lines, ok := messageTriggerRuleBlock(next)
			if !ok {
				t.Fatal("encode rule")
			}
			writeTestConfig(t, fixture+strings.Join(lines, "\n")+"\n")
			if tc.wantErr == "admin_from" {
				if _, err := Load(ConfigPath); err != nil {
					t.Fatalf("load existing confirmation after removing admins: %v", err)
				}
				current, _ := getMessageTriggerRules(t, "demo")
				if len(current) != 1 || !current[0].Enabled || current[0].TriggerMode != "manual" {
					t.Fatalf("existing confirmation was not preserved: %#v", current)
				}
				disabled := current[0]
				disabled.Enabled = false
				if _, _, err := SaveProjectMessageTriggers("demo", core.MessageTriggerRulesRevision(current), []core.MessageTriggerRule{disabled}); err != nil {
					t.Fatalf("disable existing confirmation after removing admins: %v", err)
				}
				return
			}
			if _, err := Load(ConfigPath); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("loading invalid rule = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

const dingTalkMessageTriggerConfig = `
[[projects]]
name = "demo"
agent = { type = "codex" }
platforms = [{ type = "dingtalk" }]
`

func TestMessageTriggerConfigSavesDingTalkDirectRules(t *testing.T) {
	writeTestConfig(t, dingTalkMessageTriggerConfig)
	rules := []core.MessageTriggerRule{
		{
			Name: "Private note", Enabled: true, Platform: " DingTalk ", Action: "agent",
			Groups: triggerTestGroup(
				core.MessageTriggerCondition{Type: "chat_scope", Value: "private"},
				core.MessageTriggerCondition{Type: "sender", Values: []string{"staff-private"}},
				core.MessageTriggerCondition{Type: "regex", Value: "^note:"},
			),
		},
		{
			Name: "Group ping", Enabled: true, Platform: "dingtalk", Action: "shell",
			TriggerMode: "direct", Command: "echo ping",
			Groups: triggerTestGroup(
				core.MessageTriggerCondition{Type: "chat_scope", Value: "group", Values: []string{"cidExample="}},
				core.MessageTriggerCondition{Type: "mentioned", Values: []string{"ding-user"}},
				core.MessageTriggerCondition{Type: "time_range", Start: "09:00", End: "17:30"},
			),
		},
	}
	saved, _, err := SaveProjectMessageTriggers("demo", core.MessageTriggerRulesRevision(nil), rules)
	if err != nil {
		t.Fatalf("save DingTalk rules: %v", err)
	}
	loaded, _ := getMessageTriggerRules(t, "demo")
	if !reflect.DeepEqual(saved, loaded) || len(loaded) != len(rules) {
		t.Fatalf("loaded rules = %#v, saved = %#v", loaded, saved)
	}
	for i, rule := range loaded {
		if rule.Platform != "dingtalk" || rule.TriggerMode != "direct" || rule.Action != rules[i].Action {
			t.Fatalf("unexpected DingTalk rule after reloading: %#v", rule)
		}
	}
}

func TestMessageTriggerConfigRejectsUnsupportedDingTalkRuleOptions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rule    core.MessageTriggerRule
		wantErr string
	}{
		{"manual confirmation", core.MessageTriggerRule{TriggerMode: "manual"}, "DingTalk only supports direct"},
		{"legacy confirmation", core.MessageTriggerRule{Confirm: true}, "DingTalk only supports direct"},
		{"legacy intent", core.MessageTriggerRule{IntentPrompt: "Check the incident"}, "DingTalk only supports direct"},
		{"intent without a model", core.MessageTriggerRule{Enabled: true, TriggerMode: "intent", IntentPrompt: "Check the incident"}, "DingTalk only supports direct"},
		{"CC enabled", core.MessageTriggerRule{CCEnabled: true}, "DingTalk does not support completion CC"},
		{"CC recipients saved while disabled", core.MessageTriggerRule{CCUserIDs: []string{"staff-copy"}}, "DingTalk does not support completion CC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeTestConfig(t, dingTalkMessageTriggerConfig)
			rule := tc.rule
			rule.Name, rule.Platform, rule.Action = "Unsupported", "dingtalk", "agent"
			rule.Groups = triggerTestGroup(core.MessageTriggerCondition{Type: "regex", Value: "^test$"})
			rules := []core.MessageTriggerRule{rule}
			_, _, err := SaveProjectMessageTriggers("demo", core.MessageTriggerRulesRevision(nil), rules)
			if !errors.Is(err, core.ErrInvalidMessageTriggerRules) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("save unsupported DingTalk rule = %v, want %q", err, tc.wantErr)
			}
			if string(readMessageTriggerConfig(t)) != dingTalkMessageTriggerConfig {
				t.Fatal("unsupported rule changed the config file")
			}
			lines, ok := messageTriggerRuleBlock(rules)
			if !ok {
				t.Fatal("encode rule")
			}
			writeTestConfig(t, dingTalkMessageTriggerConfig+strings.Join(lines, "\n")+"\n")
			if _, err := Load(ConfigPath); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("load unsupported DingTalk rule = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestMessageTriggerConfigSavesIntentModeWithoutManualAdministrators(t *testing.T) {
	setupMessageTriggerConfig(t, messageTriggerConfigFixture)
	current, _ := getMessageTriggerRules(t, "beta")
	rule := current[0]
	rule.TriggerMode = "intent"
	rule.IntentPrompt = "Reply only to maintenance requests."
	rule.Groups = triggerTestGroup(core.MessageTriggerCondition{Type: "regex", Value: "maintenance"})
	saved, _, err := SaveProjectMessageTriggers("beta", core.MessageTriggerRulesRevision(current), []core.MessageTriggerRule{rule})
	if err != nil {
		t.Fatalf("save intent rule: %v", err)
	}
	rules, _ := getMessageTriggerRules(t, "beta")
	if len(rules) != 1 || rules[0].TriggerMode != "intent" || rules[0].IntentPrompt != rule.IntentPrompt ||
		!reflect.DeepEqual(rules[0].Groups, saved[0].Groups) ||
		core.MessageTriggerRulesRevision(rules) != core.MessageTriggerRulesRevision(saved) {
		t.Fatalf("loaded intent rule = %#v, saved = %#v", rules, saved)
	}
}

func TestMessageTriggerConfigLegacyManualModeTakesPrecedence(t *testing.T) {
	source := strings.Replace(messageTriggerConfigFixture, `intent = "The sender reports an old incident"`, `intent = "The sender reports an old incident"
confirm = true`, 1)
	setupMessageTriggerConfig(t, source)
	rules, _ := getMessageTriggerRules(t, "alpha")
	if len(rules) != 1 || rules[0].TriggerMode != "manual" {
		t.Fatalf("legacy confirmation was not preserved: %#v", rules)
	}
}
