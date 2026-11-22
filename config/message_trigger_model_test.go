package config

import (
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestSaveMessageTriggerModelPreservesOtherConfig(t *testing.T) {
	source := strings.Replace(messageTriggerConfigFixture,
		`model = "${CC_TRIGGER_TEST_MODEL}"`,
		`model = '${CC_TRIGGER_TEST_MODEL}#build' # Model reference comment.`, 1)
	setupMessageTriggerConfig(t, source)
	t.Setenv("CC_TRIGGER_TEST_KEY", "example-secret-must-not-be-persisted")
	model, err := GetMessageTriggerModel()
	if err != nil {
		t.Fatal(err)
	}
	if model.BaseURL != "https://example.invalid/v1" || model.Model != "intent-test#build" || model.APIKeyEnv != "CC_TRIGGER_TEST_KEY" {
		t.Fatalf("model getter did not resolve the URL and model or preserve the key's variable name: %+v", model)
	}
	model.APIType = "responses"
	model.TimeoutSeconds = 25
	if err := SaveMessageTriggerModel(model); err != nil {
		t.Fatal(err)
	}
	got, err := GetMessageTriggerModel()
	if err != nil || got != model {
		t.Fatalf("saved model = %+v, %v; want %+v", got, err, model)
	}
	loaded, err := Load(ConfigPath)
	if err != nil || loaded.TriggerModel() != model {
		t.Fatalf("config reload did not load the saved model: %v", err)
	}
	prefix, _, _ := strings.Cut(source, "[message_trigger_model]")
	projectStart := strings.Index(source, "[[projects]]")
	contents := readMessageTriggerConfig(t)
	if !bytes.HasPrefix(contents, []byte(prefix)) || !bytes.HasSuffix(contents, []byte(source[projectStart:])) {
		t.Fatal("saving the model changed configuration outside its table")
	}
	for _, keyLine := range []string{
		`base_url = "${CC_TRIGGER_TEST_BASE_URL}"`,
		`model = "${CC_TRIGGER_TEST_MODEL}#build" # Model reference comment.`,
		`api_key_env = "CC_TRIGGER_TEST_KEY"`,
		`future_model_option = "keep this too"`,
		"# Keep the key as an environment variable reference.",
	} {
		if !bytes.Contains(contents, []byte(keyLine)) {
			t.Fatalf("model table lost %q", keyLine)
		}
	}
	if bytes.Contains(contents, []byte("example-secret-must-not-be-persisted")) {
		t.Fatal("the API key was persisted instead of its environment variable name")
	}
}

func TestSaveMessageTriggerModelCreatesTableWithEnvironmentReferences(t *testing.T) {
	const source = `# Project settings.
[[projects]]
name = "example"
agent = { type = "codex" }
platforms = [{ type = "feishu" }]
# Footer comment.
`
	writeTestConfig(t, source)
	t.Setenv("CC_MODEL_URL", "https://example.invalid/v1")
	t.Setenv("CC_MODEL_NAME", "intent-example")
	t.Setenv("CC_MODEL_API", "responses")
	model, err := GetMessageTriggerModel()
	if err != nil || model != (core.MessageTriggerModel{}) {
		t.Fatalf("unconfigured model = %+v, %v", model, err)
	}
	input := core.MessageTriggerModel{
		BaseURL: "${CC_MODEL_URL}", Model: "${CC_MODEL_NAME}", APIType: "${CC_MODEL_API}", APIKeyEnv: "CC_MODEL_KEY",
	}
	if err := SaveMessageTriggerModel(input); err != nil {
		t.Fatal(err)
	}
	want := core.MessageTriggerModel{BaseURL: "https://example.invalid/v1", Model: "intent-example", APIType: "responses", APIKeyEnv: "CC_MODEL_KEY"}
	got, err := GetMessageTriggerModel()
	if err != nil || got != want {
		t.Fatalf("saved model = %+v, %v; want %+v", got, err, want)
	}
	contents := string(readMessageTriggerConfig(t))
	if !strings.HasPrefix(contents, source) || !strings.Contains(contents, `api_type = "${CC_MODEL_API}"`) {
		t.Fatal("creating a model table changed the project or expanded an environment reference on disk")
	}
}

func TestSaveMessageTriggerModelRejectsInvalidFields(t *testing.T) {
	setupMessageTriggerConfig(t, messageTriggerConfigFixture)
	before := readMessageTriggerConfig(t)
	for _, tc := range []struct {
		field  string
		change func(*core.MessageTriggerModel)
	}{
		{"base_url", func(m *core.MessageTriggerModel) { m.BaseURL = "file:///tmp/model" }},
		{"api_type", func(m *core.MessageTriggerModel) { m.APIType = "unknown" }},
		{"api_key_env", func(m *core.MessageTriggerModel) { m.APIKeyEnv = "${CC_TRIGGER_TEST_KEY}" }},
		{"model", func(m *core.MessageTriggerModel) { m.Model = "" }},
		{"timeout_seconds", func(m *core.MessageTriggerModel) { m.TimeoutSeconds = -1 }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			model := core.MessageTriggerModel{BaseURL: "https://example.invalid/v1", Model: "intent-example"}
			tc.change(&model)
			if err := SaveMessageTriggerModel(model); err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("invalid %s error = %v", tc.field, err)
			}
			if !bytes.Equal(before, readMessageTriggerConfig(t)) {
				t.Fatal("invalid save modified the configuration")
			}
		})
	}
}

func TestGetMessageTriggerModelDoesNotReturnInvalidCredentials(t *testing.T) {
	writeTestConfig(t, `
[message_trigger_model]
base_url = "https://invalid:private@example.invalid/v1"
model = "intent-example"
`)
	model, err := GetMessageTriggerModel()
	if err == nil || model != (core.MessageTriggerModel{}) || strings.Contains(err.Error(), "private") {
		t.Fatalf("invalid credential-bearing URL was returned")
	}
}

func TestSaveAndClearMessageTriggerModelAPIKey(t *testing.T) {
	setupMessageTriggerConfig(t, messageTriggerConfigFixture)
	t.Setenv("CC_TRIGGER_LITERAL", "expanded-value")
	model, err := GetMessageTriggerModel()
	if err != nil {
		t.Fatal(err)
	}
	model.APIKey = "direct-test-key-${CC_TRIGGER_LITERAL}"
	if err := SaveMessageTriggerModel(model); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(ConfigPath)
	if err != nil || loaded.TriggerModel() != model {
		t.Fatalf("saved credentials did not round-trip literally: %v", err)
	}
	info, err := os.Stat(ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions are %04o, want 0600", info.Mode().Perm())
	}
	publicJSON, err := json.Marshal(loaded.TriggerModel())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(publicJSON, []byte(model.APIKey)) || bytes.Contains(publicJSON, []byte(model.APIKeyEnv)) {
		t.Fatal("serializing model settings exposed a credential field")
	}
	beforeClear := readMessageTriggerConfig(t)
	if !bytes.Contains(beforeClear, []byte(model.APIKey)) {
		t.Fatal("direct API key was not saved to TOML")
	}
	previous := model
	model.APIKey, model.APIKeyEnv = "", ""
	if err := SaveMessageTriggerModel(model); err != nil {
		t.Fatal(err)
	}
	got, err := GetMessageTriggerModel()
	if err != nil || got != model {
		t.Fatalf("cleared model did not load without credentials: %v", err)
	}
	afterClear := readMessageTriggerConfig(t)
	_, oldProjects, _ := bytes.Cut(beforeClear, []byte("[[projects]]"))
	_, newProjects, _ := bytes.Cut(afterClear, []byte("[[projects]]"))
	if !bytes.Equal(oldProjects, newProjects) || bytes.Contains(afterClear, []byte(previous.APIKey)) || bytes.Contains(afterClear, []byte(previous.APIKeyEnv)) {
		t.Fatal("clearing the API key modified project configuration or left a previous credential")
	}
}

func TestClearMessageTriggerModelAPIKeyFromAlternateTOML(t *testing.T) {
	const surroundings = "\n# Keep the project table.\n[[projects]]\nname = \"example\"\nagent = { type = \"codex\" }\nplatforms = [{ type = \"feishu\" }]\n"
	for name, body := range map[string]string{
		"inline": "message_trigger_model = { base_url = \"https://example.invalid/v1\", model = \"intent-test\", api_key = \"old-fixture-key\", future = 42 } # Keep this note.",
		"dotted": "message_trigger_model.base_url = \"https://example.invalid/v1\"\nmessage_trigger_model.model = \"intent-test\"\nmessage_trigger_model.api_key = \"old-fixture-key\" # Keep this note.\nmessage_trigger_model.future = 42",
		"quoted": "[ \"MESSAGE_TRIGGER_MODEL\" ]\nBASE_URL = \"https://example.invalid/v1\"\nmodel = \"intent-test\"\n\"api_\\u006bey\" = \"old-fixture-key\" # Keep this note.\nfuture = 42",
	} {
		t.Run(name, func(t *testing.T) {
			writeTestConfig(t, "# Keep the header.\n"+body+surroundings)
			model, err := GetMessageTriggerModel()
			if err != nil || model.APIKey != "old-fixture-key" {
				t.Fatalf("load existing model key: %v", err)
			}
			model.APIKey = ""
			if err := SaveMessageTriggerModel(model); err != nil {
				t.Fatal(err)
			}
			loaded, err := GetMessageTriggerModel()
			if err != nil || loaded != model {
				t.Fatalf("load updated model: %v", err)
			}
			contents := string(readMessageTriggerConfig(t))
			if strings.Contains(contents, "old-fixture-key") || !strings.HasPrefix(contents, "# Keep the header.\n") ||
				!strings.Contains(contents, "# Keep this note.") || !strings.Contains(contents, surroundings) ||
				!strings.Contains(contents, "future = 42") {
				t.Fatal("clearing a key left the credential or changed surrounding configuration")
			}
		})
	}
}

func TestSaveMessageTriggerModelRejectsAmbiguousCredentialFields(t *testing.T) {
	const source = "[message_trigger_model]\napi_key = \"first-fixture-key\"\nAPI_KEY = \"second-fixture-key\"\nbase_url = \"https://example.invalid/v1\"\nmodel = \"intent-test\"\n"
	writeTestConfig(t, source)
	model := core.MessageTriggerModel{BaseURL: "https://example.invalid/v1", Model: "intent-test"}
	if err := SaveMessageTriggerModel(model); err == nil || string(readMessageTriggerConfig(t)) != source {
		t.Fatal("ambiguous model key appeared to clear successfully")
	}
}
