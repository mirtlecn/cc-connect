package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

const messageTriggerModelKey = "message_trigger_model"

// GetMessageTriggerModel reads the global settings and resolves configuration
// placeholders. Credential fields stay literal and are only for server use.
func GetMessageTriggerModel() (core.MessageTriggerModel, error) {
	configMu.Lock()
	defer configMu.Unlock()
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return core.MessageTriggerModel{}, fmt.Errorf("read config: %w", err)
	}
	raw, err := decodeMessageTriggerModel(data)
	if err != nil {
		return core.MessageTriggerModel{}, err
	}
	model := resolveMessageTriggerModel(raw)
	if err := model.Validate(); err != nil {
		return core.MessageTriggerModel{}, fmt.Errorf("message_trigger_model: %w", err)
	}
	return model, nil
}

// SaveMessageTriggerModel updates the global model table without re-encoding
// project settings, unrelated fields, or comments.
func SaveMessageTriggerModel(model core.MessageTriggerModel) error {
	configMu.Lock()
	defer configMu.Unlock()

	resolved := resolveMessageTriggerModel(model)
	if err := resolved.Validate(); err != nil {
		return fmt.Errorf("message_trigger_model: %w", err)
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	prior, err := decodeMessageTriggerModel(data)
	if err != nil {
		return err
	}
	model.BaseURL = preserveTriggerModelPlaceholder(prior.BaseURL, model.BaseURL)
	model.Model = preserveTriggerModelPlaceholder(prior.Model, model.Model)
	model.APIType = preserveTriggerModelPlaceholder(prior.APIType, model.APIType)
	return writeMessageTriggerModel(data, model)
}

func decodeMessageTriggerModel(data []byte) (core.MessageTriggerModel, error) {
	var settings struct {
		Model core.MessageTriggerModel `toml:"message_trigger_model"`
	}
	if err := toml.Unmarshal(data, &settings); err != nil {
		return core.MessageTriggerModel{}, errors.New("could not parse the message trigger model configuration")
	}
	return settings.Model, nil
}

func resolveMessageTriggerModel(model core.MessageTriggerModel) core.MessageTriggerModel {
	resolveEnvValue(reflect.ValueOf(&model))
	return model
}

func preserveTriggerModelPlaceholder(prior, next string) string {
	if next != "" && envPlaceholderPattern.MatchString(prior) && resolveEnvPlaceholders(prior) == next {
		return prior
	}
	return next
}

func writeMessageTriggerModel(data []byte, model core.MessageTriggerModel) error {
	fields := map[string]any{
		"api_type": model.APIType, "base_url": model.BaseURL, "model": model.Model,
		"api_key": model.APIKey, "api_key_env": model.APIKeyEnv, "timeout_seconds": model.TimeoutSeconds,
	}
	var encoded strings.Builder
	if err := toml.NewEncoder(&encoded).Encode(fields); err != nil {
		return fmt.Errorf("encode message trigger model: %w", err)
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return errors.New("could not parse config tables")
	}
	var tableName string
	var previousTable map[string]any
	for name, value := range document {
		if !strings.EqualFold(name, messageTriggerModelKey) {
			continue
		}
		if tableName != "" {
			return errUnsafeMessageTriggerModelPatch
		}
		tableName = name
		previousTable, _ = value.(map[string]any)
	}
	if !unambiguousMessageTriggerModelFields(previousTable, fields) {
		return errUnsafeMessageTriggerModelPatch
	}
	unchanged := messageTriggerModelConfigRemainder(document, fields)
	lines, hadTrailing := splitConfigLines(string(data))
	block, _ := splitConfigLines(encoded.String())
	header := "[" + messageTriggerModelKey + "]"
	if tableName == "" {
		lines = append(lines, "", header)
		lines = append(lines, block...)
		return saveMessageTriggerModelPatch(lines, hadTrailing, model, fields, unchanged)
	}
	for i, line := range lines {
		if !messageTriggerModelTable(line, tableName) {
			continue
		}
		end := i + 1
		for end < len(lines) && !isAnyTableHeader(lines[end]) {
			end++
		}
		candidate := append([]string(nil), lines...)
		end = beforeTrailingConfigComments(candidate, i+1, end)
		for _, assignment := range block {
			key, value, _ := strings.Cut(assignment, "=")
			lineCount := len(candidate)
			key = messageTriggerModelFieldToken(candidate[i+1:end], strings.TrimSpace(key))
			candidate = upsertTomlRawKey(candidate, i+1, end-1, key, strings.TrimSpace(value))
			end += len(candidate) - lineCount
		}
		if err := saveMessageTriggerModelPatch(candidate, hadTrailing, model, fields, unchanged); err == nil {
			return nil
		} else if !errors.Is(err, errUnsafeMessageTriggerModelPatch) {
			return err
		}
	}
	return replaceInlineMessageTriggerModel(lines, hadTrailing, tableName, previousTable, model, fields, unchanged)
}

var errUnsafeMessageTriggerModelPatch = errors.New("could not update [message_trigger_model] without altering other configuration")

func messageTriggerModelTable(line, name string) bool {
	if !isAnyTableHeader(line) {
		return false
	}
	table := parseStandaloneMessageTriggerTable(line, name)
	return table != nil && len(table) == 0
}

func parseStandaloneMessageTriggerTable(line, name string) map[string]any {
	var document map[string]any
	if err := toml.Unmarshal([]byte(line), &document); err != nil || len(document) != 1 {
		return nil
	}
	table, _ := document[name].(map[string]any)
	return table
}

func messageTriggerModelFieldToken(lines []string, key string) string {
	for _, line := range lines {
		token, _, found := strings.Cut(line, "=")
		token = strings.TrimSpace(token)
		if !found || token == "" {
			continue
		}
		if strings.EqualFold(token, key) {
			return token
		}
		if token[0] == '"' || token[0] == '\'' {
			var field map[string]bool
			if err := toml.Unmarshal([]byte(token+" = true"), &field); err != nil {
				continue
			}
			for name := range field {
				if strings.EqualFold(name, key) {
					return token
				}
			}
		}
	}
	return key
}

func unambiguousMessageTriggerModelFields(table, fields map[string]any) bool {
	for field := range fields {
		matched := false
		for key := range table {
			if strings.EqualFold(key, field) {
				if matched {
					return false
				}
				matched = true
			}
		}
	}
	return true
}

// Inline and dotted model settings precede the first table. Put their
// replacement at the end to keep subsequent top-level settings in scope.
func replaceInlineMessageTriggerModel(lines []string, trailing bool, name string, remainder map[string]any, model core.MessageTriggerModel, fields map[string]any, unchanged any) error {
	var candidate []string
	var changed, inTable bool
	for _, line := range lines {
		inTable = inTable || isAnyTableHeader(line)
		if !inTable && strings.ContainsRune(line, '=') && parseStandaloneMessageTriggerTable(line, name) != nil {
			changed = true
			if comment := extractLineComment(line); comment != "" {
				candidate = append(candidate, leadingWhitespace(line)+comment)
			}
			continue
		}
		candidate = append(candidate, line)
	}
	if !changed {
		return errUnsafeMessageTriggerModelPatch
	}
	merged := make(map[string]any, len(remainder)+len(fields))
	for key, value := range remainder {
		merged[key] = value
	}
	for key, value := range fields {
		merged[key] = value
	}
	var encoded strings.Builder
	if err := toml.NewEncoder(&encoded).Encode(map[string]any{name: merged}); err != nil {
		return errUnsafeMessageTriggerModelPatch
	}
	block, _ := splitConfigLines(encoded.String())
	candidate = append(candidate, "")
	candidate = append(candidate, block...)
	return saveMessageTriggerModelPatch(candidate, trailing, model, fields, unchanged)
}

func messageTriggerModelConfigRemainder(document, fields map[string]any) any {
	for name, value := range document {
		table, ok := value.(map[string]any)
		if !ok || !strings.EqualFold(name, messageTriggerModelKey) {
			continue
		}
		for key := range table {
			for field := range fields {
				if strings.EqualFold(key, field) {
					delete(table, key)
					break
				}
			}
		}
		if len(table) == 0 {
			delete(document, name)
		}
	}
	return comparableTOMLValue(document)
}

func saveMessageTriggerModelPatch(lines []string, trailing bool, model core.MessageTriggerModel, fields map[string]any, unchanged any) error {
	data := strings.Join(lines, "\n")
	if trailing {
		data += "\n"
	}
	got, err := decodeMessageTriggerModel([]byte(data))
	if err != nil || got != model {
		return errUnsafeMessageTriggerModelPatch
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(data), &document); err != nil || !reflect.DeepEqual(messageTriggerModelConfigRemainder(document, fields), unchanged) {
		return errUnsafeMessageTriggerModelPatch
	}
	return writeConfigAtomically(data)
}
