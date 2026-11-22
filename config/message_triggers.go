package config

import (
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

func (c *Config) TriggerModel() core.MessageTriggerModel {
	if c.MessageTriggerModel == nil {
		return core.MessageTriggerModel{}
	}
	return *c.MessageTriggerModel
}

func (p ProjectConfig) normalizeMessageTriggers(rules []core.MessageTriggerRule, model core.MessageTriggerModel) ([]core.MessageTriggerRule, error) {
	var platforms []string
	for _, platform := range p.Platforms {
		switch platform.Type {
		case "feishu", "lark", "telegram":
			platforms = append(platforms, platform.Type)
		}
	}
	normalized, err := core.NormalizeMessageTriggerRules(rules, platforms, model)
	if err != nil {
		return nil, err
	}
	for i, rule := range normalized {
		for _, userID := range rule.CCUserIDs {
			if !validMessageTriggerUserID(rule.Platform, userID) {
				return nil, fmt.Errorf("message trigger rule %d: cc_user_ids must contain %s user IDs", i+1, rule.Platform)
			}
		}
	}
	return normalized, nil
}

func validateMessageTriggerConfirmationAdmins(rules []core.MessageTriggerRule, adminFrom string) error {
	adminIDs := strings.Split(adminFrom, ",")
	for i, rule := range rules {
		if rule.Enabled && rule.TriggerMode == "manual" && !slices.ContainsFunc(adminIDs, func(userID string) bool {
			return validMessageTriggerUserID(rule.Platform, strings.TrimSpace(userID))
		}) {
			return fmt.Errorf("message trigger rule %d: admin_from must contain an explicit %s user ID for confirmation", i+1, rule.Platform)
		}
	}
	return nil
}

var messageTriggerOpenID = regexp.MustCompile(`^ou_[A-Za-z0-9_]+$`)

func validMessageTriggerUserID(platform, userID string) bool {
	switch platform {
	case "feishu", "lark":
		return messageTriggerOpenID.MatchString(userID)
	case "telegram":
		id, err := strconv.ParseInt(userID, 10, 64)
		return err == nil && id > 0 && strconv.FormatInt(id, 10) == userID
	default:
		return false
	}
}

func GetProjectMessageTriggers(name string) ([]core.MessageTriggerRule, core.MessageTriggerModel, error) {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := LoadPermissive(ConfigPath)
	if err != nil {
		return nil, core.MessageTriggerModel{}, err
	}
	for _, project := range cfg.Projects {
		if project.Name == name {
			return project.MessageTriggers, cfg.TriggerModel(), nil
		}
	}
	return nil, core.MessageTriggerModel{}, fmt.Errorf("project %q not found in config", name)
}

// SaveProjectMessageTriggers replaces only the selected project's rules.
func SaveProjectMessageTriggers(name, revision string, rules []core.MessageTriggerRule) ([]core.MessageTriggerRule, core.MessageTriggerModel, error) {
	configMu.Lock()
	defer configMu.Unlock()

	var model core.MessageTriggerModel
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, model, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, model, fmt.Errorf("parse config: %w", err)
	}
	resolveEnvInConfig(&cfg)
	model = cfg.TriggerModel()
	if err := model.Validate(); err != nil {
		return nil, model, fmt.Errorf("message_trigger_model: %w", err)
	}
	for i, project := range cfg.Projects {
		if project.Name != name {
			continue
		}
		current, err := project.normalizeMessageTriggers(project.MessageTriggers, model)
		if err != nil {
			return nil, model, err
		}
		if revision != core.MessageTriggerRulesRevision(current) {
			return nil, model, core.ErrMessageTriggerRulesChanged
		}
		next, err := project.normalizeMessageTriggers(rules, model)
		if err != nil {
			return nil, model, fmt.Errorf("%w: %w", core.ErrInvalidMessageTriggerRules, err)
		}
		if err := validateMessageTriggerConfirmationAdmins(next, project.AdminFrom); err != nil {
			return nil, model, fmt.Errorf("%w: %w", core.ErrInvalidMessageTriggerRules, err)
		}
		if err := writeProjectMessageTriggers(data, i, len(cfg.Projects), next); err != nil {
			return nil, model, err
		}
		return next, model, nil
	}
	return nil, model, fmt.Errorf("project %q not found in config", name)
}

const messageTriggerRulesKey = "message_triggers"

func writeProjectMessageTriggers(data []byte, projectIndex, projectCount int, rules []core.MessageTriggerRule) error {
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse config tables: %w", err)
	}
	project := rawMessageTriggerProject(document, projectIndex)
	if project == nil {
		return fmt.Errorf("could not locate project table in config")
	}
	delete(project, messageTriggerRulesKey)
	unchanged := comparableTOMLValue(document)
	if candidate, ok := patchProjectMessageTriggerBlocks(string(data), projectIndex, projectCount, rules); ok &&
		validMessageTriggerConfigWrite(candidate, projectIndex, rules, unchanged) {
		return writeConfigAtomically(candidate)
	}

	// Inline arrays and quoted keys can prevent a safe text edit. Preserve
	// their values, including unknown fields, via the parsed document.
	if len(rules) > 0 {
		project[messageTriggerRulesKey] = rules
	}
	var contents strings.Builder
	if err := toml.NewEncoder(&contents).Encode(document); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if !validMessageTriggerConfigWrite(contents.String(), projectIndex, rules, unchanged) {
		return fmt.Errorf("encoded message trigger update changed other configuration values")
	}
	return writeConfigAtomically(contents.String())
}

func rawMessageTriggerProject(document map[string]any, index int) map[string]any {
	// Array tables and inline arrays of tables decode to different slice types.
	switch projects := document["projects"].(type) {
	case []map[string]any:
		if index < len(projects) {
			return projects[index]
		}
	case []any:
		if index < len(projects) {
			project, _ := projects[index].(map[string]any)
			return project
		}
	}
	return nil
}

func validMessageTriggerConfigWrite(contents string, projectIndex int, rules []core.MessageTriggerRule, unchanged any) bool {
	var cfg Config
	data := []byte(contents)
	if err := toml.Unmarshal(data, &cfg); err != nil || projectIndex >= len(cfg.Projects) ||
		core.MessageTriggerRulesRevision(cfg.Projects[projectIndex].MessageTriggers) != core.MessageTriggerRulesRevision(rules) {
		return false
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return false
	}
	project := rawMessageTriggerProject(document, projectIndex)
	if project == nil {
		return false
	}
	_, hasRules := project[messageTriggerRulesKey]
	if hasRules != (len(rules) > 0) {
		return false
	}
	delete(project, messageTriggerRulesKey)
	return reflect.DeepEqual(comparableTOMLValue(document), unchanged)
}

func comparableTOMLValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[key] = comparableTOMLValue(child)
		}
		return out
	case []map[string]any:
		out := make([]any, len(value))
		for index, child := range value {
			out[index] = comparableTOMLValue(child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for index, child := range value {
			out[index] = comparableTOMLValue(child)
		}
		return out
	case float64:
		// DeepEqual otherwise rejects unchanged TOML NaN values.
		return math.Float64bits(value)
	default:
		return value
	}
}

func patchProjectMessageTriggerBlocks(data string, projectIndex, projectCount int, rules []core.MessageTriggerRule) (string, bool) {
	lines, trailing := splitConfigLines(data)
	projects := buildRawProjectSpans(lines)
	if len(projects) != projectCount {
		return "", false
	}
	span := projects[projectIndex]
	block, ok := messageTriggerRuleBlock(rules)
	if !ok {
		return "", false
	}
	var found bool
	for i := span.start + 1; i <= span.end; i++ {
		if !isMessageTriggerTableHeader(lines[i]) {
			continue
		}
		end := i + 1
		for end <= span.end && !isAnyTableHeader(lines[end]) {
			end++
		}
		end = beforeTrailingConfigComments(lines, i+1, end)
		replacement := block
		if found {
			replacement = nil
		}
		found = true
		lines = slices.Replace(lines, i, end, replacement...)
		span.end += len(replacement) - (end - i)
		i += len(replacement) - 1
	}
	if !found && len(block) > 0 {
		at := beforeTrailingConfigComments(lines, span.start+1, span.end+1)
		lines = insertLines(lines, at, append([]string{""}, block...))
	}
	contents := strings.Join(lines, "\n")
	if trailing {
		contents += "\n"
	}
	return contents, true
}

func isMessageTriggerTableHeader(line string) bool {
	return matchTableHeader(line, "[[projects.message_triggers]]") ||
		matchTableHeader(line, "[[projects.message_triggers.groups]]") ||
		matchTableHeader(line, "[[projects.message_triggers.groups.conditions]]")
}

func beforeTrailingConfigComments(lines []string, start, end int) int {
	for end > start {
		line := strings.TrimSpace(lines[end-1])
		if line != "" && !strings.HasPrefix(line, "#") {
			break
		}
		end--
	}
	return end
}

func messageTriggerRuleBlock(rules []core.MessageTriggerRule) ([]string, bool) {
	if len(rules) == 0 {
		return nil, true
	}
	var contents strings.Builder
	projects := []map[string]any{{messageTriggerRulesKey: rules}}
	if err := toml.NewEncoder(&contents).Encode(map[string]any{"projects": projects}); err != nil {
		return nil, false
	}
	lines, _ := splitConfigLines(contents.String())
	if len(lines) == 0 || lines[0] != "[[projects]]" {
		return nil, false
	}
	return lines[1:], true
}
