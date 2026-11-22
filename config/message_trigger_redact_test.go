package config

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactMessageTriggerConfigMasksCredentialsAcrossTOMLForms(t *testing.T) {
	for name, source := range map[string]string{
		"table": `
# fixture-secret
[message_trigger_model]
api_key = "fixture-secret"
api_key_env = "LEGACY_KEY_VAR"
model = "intent-test"
`,
		"inline": `message_trigger_model = { api_key = 'fixture-secret', api_key_env = "LEGACY_KEY_VAR", model = "intent-test" }`,
		"quoted header": `["message_trigger_model"]
"api_\u006bey" = "fixture-secret"
'api_key_env' = "LEGACY_KEY_VAR"
model = "intent-test"
`,
		"dotted keys": `"message_trigger_model".'api_key' = "fixture-secret"
message_trigger_model.api_key_env = "LEGACY_KEY_VAR"
message_trigger_model.model = "intent-test"
`,
		"case variants": `["MESSAGE_TRIGGER_MODEL"]
API_Key = "fixture-secret"
API_KEY_ENV = "LEGACY_KEY_VAR"
MODEL = "intent-test"
`,
	} {
		t.Run(name, func(t *testing.T) {
			input := []byte("language = \"zh\"\n" + source)
			out, err := RedactMessageTriggerConfig(input)
			if err != nil {
				t.Fatal(err)
			}
			model, err := decodeMessageTriggerModel(out)
			if err != nil {
				t.Fatal(err)
			}
			if model.APIKey != "[redacted]" || model.APIKeyEnv != "[redacted]" || model.Model != "intent-test" {
				t.Fatal("redaction removed or exposed model settings unexpectedly")
			}
			if bytes.Contains(out, []byte("fixture-secret")) || bytes.Contains(out, []byte("LEGACY_KEY_VAR")) || !bytes.Contains(out, []byte(`language = "zh"`)) {
				t.Fatal("redacted output exposed credentials or removed unrelated settings")
			}
			if !bytes.Contains(input, []byte("fixture-secret")) {
				t.Fatal("redaction modified the stored source bytes")
			}
		})
	}
}

func TestRedactMessageTriggerConfigFailClosed(t *testing.T) {
	for _, source := range []string{
		"[message_trigger_model]\napi_key = \"fixture-secret",
		`message_trigger_model = "fixture-secret"`,
	} {
		out, err := RedactMessageTriggerConfig([]byte(source))
		if err == nil || out != nil || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatal("unsafe source was returned instead of a generic error")
		}
	}
}

func TestRedactMessageTriggerConfigWithoutCredentialsReturnsOriginal(t *testing.T) {
	source := []byte("# Keep this comment.\n[message_trigger_model]\napi_key = \"\"\nmodel = \"intent-test\"\n")
	out, err := RedactMessageTriggerConfig(source)
	if err != nil || !bytes.Equal(out, source) {
		t.Fatalf("non-sensitive configuration was changed: %v", err)
	}
}
