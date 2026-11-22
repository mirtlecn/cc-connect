package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type triggerModelTestFormat struct {
	Type       string                  `json:"type"`
	Name       string                  `json:"name"`
	Strict     bool                    `json:"strict"`
	JSONSchema *triggerModelTestFormat `json:"json_schema"`
	Schema     struct {
		Type                 string `json:"type"`
		AdditionalProperties *bool  `json:"additionalProperties"`
		Required             []string
		Properties           map[string]struct {
			Type string
			Enum []string
		}
	} `json:"schema"`
}

type triggerModelTestRequest struct {
	Model          string                 `json:"model"`
	Instructions   string                 `json:"instructions"`
	Input          string                 `json:"input"`
	Messages       []triggerIntentMessage `json:"messages"`
	Store          *bool                  `json:"store"`
	Thinking       map[string]string      `json:"thinking"`
	Reasoning      map[string]string      `json:"reasoning"`
	Tools          json.RawMessage        `json:"tools"`
	ToolChoice     json.RawMessage        `json:"tool_choice"`
	ResponseFormat triggerModelTestFormat `json:"response_format"`
	Text           struct {
		Format triggerModelTestFormat `json:"format"`
	} `json:"text"`
}

func decodeTriggerModelTestInput(t *testing.T, body io.Reader, protocol string) (triggerIntentInput, error) {
	t.Helper()
	var request triggerModelTestRequest
	if err := json.NewDecoder(body).Decode(&request); err != nil {
		return triggerIntentInput{}, err
	}
	if request.Model != "classifier-fixture" || request.Tools != nil || request.ToolChoice != nil {
		return triggerIntentInput{}, fmt.Errorf("unexpected model or tool parameters")
	}

	instructions, input := request.Instructions, request.Input
	format := request.Text.Format
	schema := &format
	if protocol == "responses" {
		if request.Store == nil || *request.Store || len(request.Messages) != 0 || request.ResponseFormat.Type != "" {
			t.Error("Responses request must use input, instructions, store=false and text.format")
		}
	} else {
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" || input != "" || instructions != "" {
			return triggerIntentInput{}, fmt.Errorf("Chat Completions request must separate system instructions and user data")
		}
		instructions, input = request.Messages[0].Content, request.Messages[1].Content
		format = request.ResponseFormat
		schema = format.JSONSchema
	}
	if format.Type != "json_schema" || schema == nil || schema.Name != triggerDecisionSchemaName || !schema.Strict {
		return triggerIntentInput{}, fmt.Errorf("expected strict decision JSON Schema")
	}
	fields := schema.Schema
	if fields.Type != "object" || fields.AdditionalProperties == nil || *fields.AdditionalProperties ||
		len(fields.Required) != 2 || !slices.Contains(fields.Required, "decision") || !slices.Contains(fields.Required, "reason") ||
		fields.Properties["decision"].Type != "string" || !slices.Equal(fields.Properties["decision"].Enum, []string{"yes", "no", "confirm"}) ||
		fields.Properties["reason"].Type != "string" {
		t.Errorf("unexpected decision schema: %+v", fields)
	}
	if !strings.Contains(instructions, "untrusted") || !strings.Contains(instructions, `"decision"`) {
		t.Errorf("model instructions lack JSON output or untrusted message guidance")
	}
	var payload triggerIntentInput
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		return triggerIntentInput{}, err
	}
	return payload, nil
}

func triggerModelJSONResponse(protocol, decisionJSON string) map[string]any {
	if protocol == "responses" {
		return map[string]any{"status": "completed", "output": []map[string]any{
			{"type": "reasoning"},
			{"type": "message", "content": []map[string]any{{"type": "output_text", "text": decisionJSON}}},
		}}
	}
	return map[string]any{"choices": []map[string]any{{
		"finish_reason": "stop", "message": map[string]any{"content": decisionJSON},
	}}}
}

func TestMessageTriggerModelStructuredOutputProtocols(t *testing.T) {
	t.Setenv("CC_TRIGGER_MODEL_TEST_KEY", "test-credential")
	for _, wire := range []struct{ protocol, path string }{
		{"chat_completions", "/v1/chat/completions"}, {"responses", "/v1/responses"},
	} {
		t.Run(wire.protocol, func(t *testing.T) {
			received := make(chan triggerIntentInput, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != wire.path || r.Header.Get("Authorization") != "Bearer test-credential" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected method, endpoint, or headers for %s", r.URL.Path)
				}
				input, err := decodeTriggerModelTestInput(t, r.Body, wire.protocol)
				if err != nil {
					t.Error(err)
					http.Error(w, "bad test request", http.StatusBadRequest)
					return
				}
				received <- input
				arguments := `{"decision":"confirm","reason":"  missing release approval  "}`
				if input.Session.Trigger.Body == "test" {
					arguments = `{"decision":"yes","reason":"ok"}`
				}
				if err := json.NewEncoder(w).Encode(triggerModelJSONResponse(wire.protocol, arguments)); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()

			model := MessageTriggerModel{
				BaseURL: server.URL + "/v1", Model: "classifier-fixture", APIType: wire.protocol, APIKeyEnv: "CC_TRIGGER_MODEL_TEST_KEY",
			}
			if err := model.Probe(context.Background()); err != nil {
				t.Fatalf("probe: %v", err)
			}
			probe := <-received
			if !strings.Contains(probe.Prompt, "verify") || probe.Session.Trigger.Body != "test" ||
				len(probe.Session.RecentMessages) != 0 || probe.Session.Trigger.Context != "" || probe.Session.Trigger.SenderID != "" {
				t.Fatalf("probe should contain synthetic data only, got %+v", probe)
			}
			input := triggerIntentInput{
				Prompt: "Report whether the release needs review",
				Session: triggerIntentSession{
					RecentMessages: []triggerIntentMessage{{Role: "user", Content: "Release discussion"}, {Role: "assistant", Content: "Awaiting a plan"}},
					Trigger:        triggerIntentSource{Body: "Deploy build 12", Context: "Reply context", SenderID: "sender", Mentions: []string{"reviewer"}},
				},
			}
			decision, err := model.classify(context.Background(), input)
			if want := (triggerIntentDecision{Decision: "confirm", Reason: "missing release approval"}); err != nil || decision != want {
				t.Fatalf("classification: %+v, %v; want %+v", decision, err, want)
			}
			if got := <-received; !reflect.DeepEqual(got, input) {
				t.Fatalf("wire input = %+v, want %+v", got, input)
			}
		})
	}
}

func TestMessageTriggerModelDeepSeekStructuredOutput(t *testing.T) {
	for _, test := range []struct {
		name, baseURL, protocol, format, thinking, reasoning string
	}{
		{"official chat", "https://api.deepseek.com/", "chat_completions", "json_object", "disabled", ""},
		{"official responses", "https://api.deepseek.com/", "responses", "json_schema", "", "none"},
		{"other provider", "https://api.deepseek.com.example.com/v1", "chat_completions", "json_schema", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := MessageTriggerModel{BaseURL: test.baseURL, Model: "deepseek-flash", APIType: test.protocol}
			payload, err := model.payload(triggerIntentInput{})
			if err != nil {
				t.Fatal(err)
			}
			var request triggerModelTestRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				t.Fatal(err)
			}
			format := request.ResponseFormat.Type
			if test.protocol == "responses" {
				format = request.Text.Format.Type
			}
			if format != test.format || request.Thinking["type"] != test.thinking || request.Reasoning["effort"] != test.reasoning || request.Tools != nil || request.ToolChoice != nil {
				t.Fatalf("unexpected structured response format or model options for %s", test.name)
			}
		})
	}
}

func TestMessageTriggerModelFailsClosed(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			model := MessageTriggerModel{APIType: protocol}
			const validJSON = `{"decision":"no","reason":"not an incident"}`
			invalid := map[string]map[string]any{
				"missing output": {},
			}
			if protocol == "responses" {
				incomplete := triggerModelJSONResponse(protocol, validJSON)
				incomplete["status"] = "incomplete"
				invalid["incomplete"] = incomplete
				multiple := triggerModelJSONResponse(protocol, validJSON)
				items := multiple["output"].([]map[string]any)
				multiple["output"] = append(items, items[1])
				invalid["multiple messages"] = multiple
			} else {
				truncated := triggerModelJSONResponse(protocol, validJSON)
				truncated["choices"].([]map[string]any)[0]["finish_reason"] = "length"
				invalid["truncated"] = truncated
				refusal := triggerModelJSONResponse(protocol, validJSON)
				refusal["choices"].([]map[string]any)[0]["message"].(map[string]any)["refusal"] = "refused"
				invalid["refusal"] = refusal
			}
			for name, content := range map[string]string{
				"non-JSON":           "not JSON",
				"duplicate decision": `{"decision":"yes","decision":"no","reason":"conflict"}`,
				"duplicate reason":   `{"decision":"yes","reason":"first","reason":"second"}`,
				"wrong value type":   `{"decision":true,"reason":"boolean"}`,
				"unknown decision":   `{"decision":"allow","reason":"invalid"}`,
				"blank reason":       `{"decision":"yes","reason":"  "}`,
				"missing reason":     `{"decision":"yes"}`,
				"unexpected field":   `{"decision":"yes","reason":"ok","extra":1}`,
				"trailing input":     `{"decision":"yes","reason":"ok"} {}`,
			} {
				invalid[name] = triggerModelJSONResponse(protocol, content)
			}
			for name, response := range invalid {
				t.Run(name, func(t *testing.T) {
					data, err := json.Marshal(response)
					if err != nil {
						t.Fatal(err)
					}
					if got, err := model.decodeDecision(bytes.NewReader(data)); err == nil || got != (triggerIntentDecision{}) {
						t.Fatalf("accepted invalid response: %+v, %v", got, err)
					}
				})
			}
			data, err := json.Marshal(triggerModelJSONResponse(protocol, validJSON))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := model.decodeDecision(bytes.NewReader(data)); err != nil || got.Decision != "no" || got.Reason != "not an incident" {
				t.Fatalf("valid negative response: %+v, %v", got, err)
			}
		})
	}
}

func TestMessageTriggerModelHTTPErrorAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/error/") {
			http.Error(w, "provider body must stay private", http.StatusBadGateway)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			model := MessageTriggerModel{APIType: protocol, BaseURL: server.URL + "/error", Model: "classifier-fixture"}
			if err := model.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), "provider body") {
				t.Fatalf("unexpected provider status error: %v", err)
			}
			model.BaseURL = server.URL
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := model.Probe(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("probe timeout = %v, want deadline exceeded", err)
			}
		})
	}
}

func TestMessageTriggerIntentConfirmationRunsOnlyAfterApproval(t *testing.T) {
	const reason = "The release request is missing stakeholder approval"
	const instruction = "Classify release requests and ask for confirmation when context is incomplete"
	const actionPrompt = "Summarize the approved release request"
	const original = "Please deploy this release"
	var modelCalls atomic.Int32
	request := make(chan triggerIntentInput, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		input, err := decodeTriggerModelTestInput(t, r.Body, "chat_completions")
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		request <- input
		args, err := json.Marshal(triggerIntentDecision{Decision: "confirm", Reason: reason})
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.NewEncoder(w).Encode(triggerModelJSONResponse("chat_completions", string(args))); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	platform := newMessageTriggerTestPlatform()
	agent := &cujAgent{}
	engine := NewEngine("intent-approval", agent, []Platform{platform}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = engine.Stop() })
	engine.SetAdminFrom("admin,other-admin")
	engine.SetMessageTriggers([]MessageTriggerRule{{
		Name: "release review", Enabled: true, Platform: platform.Name(), Action: "agent", Prompt: actionPrompt,
		TriggerMode: "intent", IntentPrompt: instruction,
		Groups: []MessageTriggerGroup{{Match: "and", Conditions: []MessageTriggerCondition{{Type: "group", Values: []string{"group"}}}}},
	}}, MessageTriggerModel{BaseURL: server.URL, Model: "classifier-fixture"})

	message := messageTriggerTestMessage(platform, "release", original, false)
	message.ExtraContent = "The request quotes a prior launch plan"
	session := engine.sessions.GetOrCreateActive(message.SessionKey)
	session.AddHistory("user", "Let us stage a release")
	session.AddHistory("tool", "not sent to the classifier")
	session.AddHistory("assistant", "Waiting for the request")
	var accepted atomic.Int32
	message.OnAccepted = func() { accepted.Add(1) }
	engine.ReceiveMessage(platform, message)
	card := platform.readCard(t)
	if card.reply != "source:release" || !strings.Contains(card.prompt, reason) || !strings.Contains(card.prompt, "release review") ||
		!slices.Equal(card.admins, []string{"admin", "other-admin"}) {
		t.Fatalf("unexpected confirmation card: %+v", card)
	}
	if accepted.Load() != 0 || len(platform.getSent()) != 0 {
		t.Fatal("the action ran before approval")
	}
	want := triggerIntentInput{Prompt: instruction, Session: triggerIntentSession{
		RecentMessages: []triggerIntentMessage{{Role: "user", Content: "Let us stage a release"}, {Role: "assistant", Content: "Waiting for the request"}},
		Trigger:        triggerIntentSource{Body: original, Context: message.ExtraContent, SenderID: "sender", Mentions: []string{"me"}},
	}}
	if got := <-request; !reflect.DeepEqual(got, want) {
		t.Fatalf("classifier input = %+v, want %+v", got, want)
	}

	if _, resolved := platform.confirm(card.id, "group", "sender", true); resolved {
		t.Fatal("non-administrator approved the request")
	}
	for _, setting := range []string{"", "*"} {
		engine.SetAdminFrom(setting)
		if _, resolved := platform.confirm(card.id, "group", "admin", true); resolved {
			t.Fatalf("approval used a previous or wildcard administrator for admin_from=%q", setting)
		}
	}
	engine.SetAdminFrom("other-admin")
	if _, resolved := platform.confirm(card.id, "group", "other-admin", true); !resolved {
		t.Fatal("explicit administrator could not approve the request")
	}
	if _, resolved := platform.confirm(card.id, "group", "other-admin", true); resolved {
		t.Fatal("confirmation could be used more than once")
	}
	env := &cujEnv{t: t, plat: platform.stubPlatformEngine}
	env.waitFor("approved Agent reply quoting the source", 3*time.Second, func() bool {
		return slices.ContainsFunc(platform.getSent(), func(reply string) bool { return strings.HasPrefix(reply, "source:release -> ok") })
	})
	if accepted.Load() != 1 || modelCalls.Load() != 1 {
		t.Fatalf("approval admitted %d turns after %d classifier calls", accepted.Load(), modelCalls.Load())
	}
	agent.mu.Lock()
	sessions := slices.Clone(agent.sessions)
	agent.mu.Unlock()
	if len(sessions) != 1 {
		t.Fatalf("approved action started %d Agent sessions", len(sessions))
	}
	if prompts := sessions[0].getSentPrompts(); len(prompts) != 1 || !strings.Contains(prompts[0], actionPrompt) || !strings.Contains(prompts[0], original) {
		t.Fatalf("approved Agent received unexpected prompts: %v", prompts)
	}
}
