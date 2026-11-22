package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
)

type MessageTriggerRule struct {
	Name         string                `json:"name" toml:"name"`
	Enabled      bool                  `json:"enabled" toml:"enabled"`
	Platform     string                `json:"platform" toml:"platform"`
	GroupMatch   string                `json:"group_match" toml:"group_match"`
	Groups       []MessageTriggerGroup `json:"groups" toml:"groups"`
	Action       string                `json:"action" toml:"action"`
	TriggerMode  string                `json:"trigger_mode" toml:"trigger_mode"`
	IntentPrompt string                `json:"intent_prompt,omitempty" toml:"intent_prompt,omitempty"`
	CCEnabled    bool                  `json:"cc_enabled,omitempty" toml:"cc_enabled,omitempty"`
	CCUserIDs    []string              `json:"cc_user_ids,omitempty" toml:"cc_user_ids,omitempty"`
	Prompt       string                `json:"prompt,omitempty" toml:"prompt,omitempty" env:"-"`
	Command      string                `json:"command,omitempty" toml:"command,omitempty" env:"-"`

	// Flat fields are accepted when loading the earlier TOML representation.
	ChatIDs      []string `json:"-" toml:"chat_ids,omitempty"`
	SenderIDs    []string `json:"-" toml:"sender_ids,omitempty"`
	MentionedIDs []string `json:"-" toml:"mentioned_ids,omitempty"`
	Keywords     []string `json:"-" toml:"keywords,omitempty"`
	Regex        string   `json:"-" toml:"regex,omitempty"`
	Intent       string   `json:"-" toml:"intent,omitempty"`
	Match        string   `json:"-" toml:"match,omitempty"`
	Confirm      bool     `json:"-" toml:"confirm,omitempty"`

	groupsInScope []string
}

type MessageTriggerGroup struct {
	Match      string                    `json:"match" toml:"match"`
	Conditions []MessageTriggerCondition `json:"conditions" toml:"conditions"`
}

type MessageTriggerCondition struct {
	Type   string   `json:"type" toml:"type"`
	Values []string `json:"values,omitempty" toml:"values,omitempty"`
	Value  string   `json:"value,omitempty" toml:"value,omitempty"`

	compiled *regexp.Regexp
}

type MessageTriggerModel struct {
	BaseURL        string `toml:"base_url" json:"base_url"`
	Model          string `toml:"model" json:"model"`
	APIKey         string `toml:"api_key,omitempty" json:"-" env:"-"`
	APIKeyEnv      string `toml:"api_key_env,omitempty" json:"-" env:"-"` // legacy configuration
	TimeoutSeconds int    `toml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	APIType        string `toml:"api_type,omitempty" json:"api_type"`
}

var triggerModelEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var ErrMessageTriggerRulesChanged = errors.New("message trigger rules changed since they were loaded")
var ErrInvalidMessageTriggerRules = errors.New("invalid message trigger rules")

func MessageTriggerRulesRevision(rules []MessageTriggerRule) string {
	if rules == nil {
		rules = []MessageTriggerRule{}
	}
	data, _ := json.Marshal(rules) // contains only JSON-compatible field types
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (m MessageTriggerModel) Validate() error {
	if m == (MessageTriggerModel{}) {
		return nil
	}
	base, err := url.Parse(strings.TrimSpace(m.BaseURL))
	if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("base_url must be an HTTP(S) API base URL without credentials, query or fragment")
	}
	if strings.TrimSpace(m.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if protocol := m.protocol(); protocol != "responses" && protocol != "chat_completions" {
		return fmt.Errorf("api_type must be responses or chat_completions")
	}
	if m.APIKeyEnv != "" && !triggerModelEnvName.MatchString(m.APIKeyEnv) {
		return fmt.Errorf("api_key_env must be an environment variable name")
	}
	if strings.ContainsAny(m.APIKey, "\r\n") {
		return fmt.Errorf("api_key must be a single line")
	}
	if m.TimeoutSeconds < 0 || m.TimeoutSeconds > 300 {
		return fmt.Errorf("timeout_seconds must be between 0 and 300")
	}
	return nil
}

func (m MessageTriggerModel) protocol() string {
	protocol := strings.ToLower(strings.TrimSpace(m.APIType))
	if protocol == "" {
		return "chat_completions"
	}
	return protocol
}

func (m MessageTriggerModel) configured() bool {
	return m.BaseURL != "" && m.Model != ""
}

func (m MessageTriggerModel) available() bool {
	return m.configured() && (m.APIKeyEnv == "" || m.key() != "")
}

func (m MessageTriggerModel) key() string {
	if m.APIKey != "" {
		return m.APIKey
	}
	return os.Getenv(m.APIKeyEnv)
}

func NormalizeMessageTriggerRules(rules []MessageTriggerRule, platforms []string, model MessageTriggerModel) ([]MessageTriggerRule, error) {
	normalized := make([]MessageTriggerRule, 0, len(rules))
	for i, input := range rules {
		rule, err := normalizeMessageTriggerRule(input)
		if err != nil {
			return nil, fmt.Errorf("message trigger rule %d: %w", i+1, err)
		}
		if err := rule.validate(platforms, model); err != nil {
			return nil, fmt.Errorf("message trigger rule %d: %w", i+1, err)
		}
		normalized = append(normalized, rule)
	}
	return normalized, nil
}

func normalizeMessageTriggerRule(rule MessageTriggerRule) (MessageTriggerRule, error) {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Platform = strings.ToLower(strings.TrimSpace(rule.Platform))
	rule.CCUserIDs = cleanTriggerList(rule.CCUserIDs)
	rule.Prompt = strings.TrimSpace(rule.Prompt)
	rule.Command = strings.TrimSpace(rule.Command)
	if len(rule.Groups) == 0 && len(rule.ChatIDs) > 0 {
		rule.Groups = flatMessageTriggerGroups(rule)
		rule.GroupMatch = "and"
	}
	if err := normalizeMessageTriggerMode(&rule); err != nil {
		return rule, err
	}
	rule.ChatIDs, rule.SenderIDs, rule.MentionedIDs, rule.Keywords = nil, nil, nil, nil
	rule.Regex, rule.Intent, rule.Match = "", "", ""
	rule.Confirm = false
	rule.GroupMatch = normalizeTriggerOperator(rule.GroupMatch, "or")
	if !validTriggerOperator(rule.GroupMatch) {
		return rule, fmt.Errorf("group_match must be either %q or %q", "and", "or")
	}
	groups := make([]MessageTriggerGroup, 0, len(rule.Groups))
	var groupIDs []string
	for i, input := range rule.Groups {
		group, chats, err := normalizeMessageTriggerGroup(input)
		if err != nil {
			return rule, fmt.Errorf("group %d: %w", i+1, err)
		}
		groupIDs = append(groupIDs, chats...)
		groups = append(groups, group)
	}
	rule.Groups = groups
	rule.groupsInScope = cleanTriggerList(groupIDs)
	return rule, nil
}

func normalizeMessageTriggerMode(rule *MessageTriggerRule) error {
	rule.TriggerMode = strings.ToLower(strings.TrimSpace(rule.TriggerMode))
	rule.IntentPrompt = strings.TrimSpace(rule.IntentPrompt)
	if rule.TriggerMode != "" {
		return nil
	}
	// Earlier drafts placed intent within condition groups. Keep the
	// mechanical filters and migrate intent into the separate review step.
	var legacyPrompts []string
	groups := make([]MessageTriggerGroup, 0, len(rule.Groups))
	for _, original := range rule.Groups {
		group := MessageTriggerGroup{Match: original.Match}
		for _, condition := range original.Conditions {
			if condition.Type == "intent" {
				legacyPrompts = append(legacyPrompts, condition.Value)
			} else {
				group.Conditions = append(group.Conditions, condition)
			}
		}
		if len(group.Conditions) > 0 || len(original.Conditions) == 0 {
			groups = append(groups, group)
		}
	}
	rule.Groups = groups
	switch {
	case rule.Confirm:
		rule.TriggerMode = "manual"
	case len(legacyPrompts) > 0 || rule.IntentPrompt != "":
		rule.TriggerMode = "intent"
		if rule.IntentPrompt == "" {
			rule.IntentPrompt = strings.Join(cleanTriggerList(legacyPrompts), "\n")
		}
	default:
		rule.TriggerMode = "direct"
	}
	return nil
}

func flatMessageTriggerGroups(rule MessageTriggerRule) []MessageTriggerGroup {
	scope := MessageTriggerGroup{Match: "and"}
	scope.Conditions = append(scope.Conditions, MessageTriggerCondition{Type: "group", Values: rule.ChatIDs})
	if len(cleanTriggerList(rule.SenderIDs)) > 0 {
		scope.Conditions = append(scope.Conditions, MessageTriggerCondition{Type: "sender", Values: rule.SenderIDs})
	}
	if len(cleanTriggerList(rule.MentionedIDs)) > 0 {
		scope.Conditions = append(scope.Conditions, MessageTriggerCondition{Type: "mentioned", Values: rule.MentionedIDs})
	}
	groups := []MessageTriggerGroup{scope}
	message := MessageTriggerGroup{Match: normalizeTriggerOperator(rule.Match, "and")}
	pattern := rule.Regex
	if keywords := cleanTriggerList(rule.Keywords); strings.TrimSpace(pattern) == "" && len(keywords) > 0 {
		for i, keyword := range keywords {
			keywords[i] = regexp.QuoteMeta(keyword)
		}
		pattern = "(?i:" + strings.Join(keywords, "|") + ")"
	}
	if strings.TrimSpace(pattern) != "" {
		message.Conditions = append(message.Conditions, MessageTriggerCondition{Type: "regex", Value: pattern})
	}
	if strings.TrimSpace(rule.Intent) != "" {
		message.Conditions = append(message.Conditions, MessageTriggerCondition{Type: "intent", Value: rule.Intent})
	}
	if len(message.Conditions) > 0 {
		groups = append(groups, message)
	}
	return groups
}

func normalizeMessageTriggerGroup(input MessageTriggerGroup) (MessageTriggerGroup, []string, error) {
	group := MessageTriggerGroup{Match: normalizeTriggerOperator(input.Match, "and")}
	if !validTriggerOperator(group.Match) {
		return group, nil, fmt.Errorf("match must be either %q or %q", "and", "or")
	}
	if len(input.Conditions) == 0 {
		return group, nil, fmt.Errorf("at least one condition is required")
	}
	var groupIDs []string
	for i, condition := range input.Conditions {
		condition.Type = strings.ToLower(strings.TrimSpace(condition.Type))
		condition.Values = cleanTriggerList(condition.Values)
		if err := condition.validate(); err != nil {
			return group, nil, fmt.Errorf("condition %d: %w", i+1, err)
		}
		if condition.Type == "group" {
			groupIDs = append(groupIDs, condition.Values...)
		}
		group.Conditions = append(group.Conditions, condition)
	}
	return group, groupIDs, nil
}

func (c *MessageTriggerCondition) validate() error {
	switch c.Type {
	case "group", "sender", "mentioned":
		if len(c.Values) == 0 || c.Value != "" {
			return fmt.Errorf("%s requires a values list of platform IDs", c.Type)
		}
	case "regex":
		if strings.TrimSpace(c.Value) == "" || len(c.Values) != 0 {
			return fmt.Errorf("%s requires a non-empty value", c.Type)
		}
		var err error
		c.compiled, err = regexp.Compile(c.Value)
		if err != nil {
			return fmt.Errorf("invalid regular expression: %w", err)
		}
	default:
		return fmt.Errorf("unknown condition type %q", c.Type)
	}
	return nil
}

func normalizeTriggerOperator(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func validTriggerOperator(value string) bool {
	return value == "and" || value == "or"
}

func cleanTriggerList(values []string) []string {
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func (r MessageTriggerRule) validate(platforms []string, model MessageTriggerModel) error {
	switch {
	case r.Name == "":
		return fmt.Errorf("name is required")
	case !slices.Contains(platforms, r.Platform):
		return fmt.Errorf("select a supported channel configured for this project")
	case len(r.Groups) == 0:
		return fmt.Errorf("at least one condition group is required")
	case len(r.groupsInScope) == 0:
		return fmt.Errorf("at least one group ID condition is required")
	case r.Action != "agent" && r.Action != "shell":
		return fmt.Errorf("action must be agent or shell")
	case r.Action == "shell" && r.Command == "":
		return fmt.Errorf("command is required for shell actions")
	case r.Action == "agent" && r.Command != "":
		return fmt.Errorf("command is only supported for shell actions")
	case r.Action == "shell" && r.Prompt != "":
		return fmt.Errorf("prompt is only supported for agent actions")
	case r.CCEnabled && len(r.CCUserIDs) == 0:
		return fmt.Errorf("cc_user_ids is required when cc_enabled is true")
	case r.TriggerMode != "direct" && r.TriggerMode != "manual" && r.TriggerMode != "intent":
		return fmt.Errorf("trigger_mode must be direct, manual, or intent")
	case r.TriggerMode == "intent" && r.IntentPrompt == "":
		return fmt.Errorf("intent_prompt is required for intelligent judgment")
	case r.TriggerMode != "intent" && r.IntentPrompt != "":
		return fmt.Errorf("intent_prompt requires trigger_mode = intent")
	case r.Enabled && r.TriggerMode == "intent" && !model.configured():
		return fmt.Errorf("configure message_trigger_model before enabling intent rules")
	default:
		return nil
	}
}
