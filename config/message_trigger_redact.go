package config

import (
	"bytes"
	"errors"
	"strings"

	"github.com/BurntSushi/toml"
)

var errMessageTriggerConfigRedaction = errors.New("could not safely redact the message trigger model credentials")

// RedactMessageTriggerConfig returns a copy for Web display. Decode TOML
// first so key spelling, quoting and inline table syntax cannot bypass it.
func RedactMessageTriggerConfig(contents []byte) ([]byte, error) {
	var document map[string]any
	if err := toml.Unmarshal(contents, &document); err != nil {
		return nil, errMessageTriggerConfigRedaction
	}
	redacted := false
	for name, value := range document {
		if !strings.EqualFold(name, messageTriggerModelKey) {
			continue
		}
		table, ok := value.(map[string]any)
		if !ok {
			return nil, errMessageTriggerConfigRedaction
		}
		for key, raw := range table {
			if !strings.EqualFold(key, "api_key") && !strings.EqualFold(key, "api_key_env") {
				continue
			}
			if text, ok := raw.(string); ok && text == "" {
				continue
			}
			table[key] = "[redacted]"
			redacted = true
		}
	}
	if !redacted {
		return contents, nil
	}
	var result bytes.Buffer
	if err := toml.NewEncoder(&result).Encode(document); err != nil {
		return nil, errMessageTriggerConfigRedaction
	}
	return result.Bytes(), nil
}
