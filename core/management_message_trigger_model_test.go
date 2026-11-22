package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testMessageTriggerModelManagementToken = "management-fixture-token"

func requestManagementMessageTriggerModel(t *testing.T, handler http.Handler, method string, input map[string]any) (int, mgmtResponse) {
	t.Helper()
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "/api/v1/message-trigger-model", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testMessageTriggerModelManagementToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	var response mgmtResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode model settings response: %v", err)
	}
	return recorder.Code, response
}

func assertManagementTriggerModelKeyStatus(t *testing.T, code int, response mgmtResponse, configured bool, secrets ...string) {
	t.Helper()
	if code != http.StatusOK || !response.OK {
		t.Fatalf("model settings returned HTTP %d", code)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := json.Unmarshal(data["key_configured"], &got); err != nil || got != configured {
		t.Fatalf("key_configured = %v, %v, want %v", got, err, configured)
	}
	for _, field := range []string{"api_key", "api_key_env"} {
		if _, ok := data[field]; ok {
			t.Errorf("model settings exposed %s", field)
		}
	}
	for _, secret := range secrets {
		if bytes.Contains(response.Data, []byte(secret)) || strings.Contains(response.Error, secret) {
			t.Fatal("model settings exposed a credential value")
		}
	}
}

func managementTriggerModelProbe(t *testing.T, succeeds bool) (*httptest.Server, <-chan string) {
	t.Helper()
	auth := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Error("unexpected model probe route")
		}
		if !succeeds {
			http.Error(w, "fixture upstream response", http.StatusUnauthorized)
			return
		}
		if err := json.NewEncoder(w).Encode(triggerModelJSONResponse("chat_completions", `{"decision":"yes","reason":"ok"}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server, auth
}

func assertManagementTriggerModelProbeAuth(t *testing.T, received <-chan string, want string) {
	t.Helper()
	select {
	case got := <-received:
		if got != want {
			t.Fatal("model probe sent unexpected authorization")
		}
	default:
		t.Fatal("management request did not probe the model")
	}
}

func TestMgmtMessageTriggerModelDirectKeyLifecycle(t *testing.T) {
	const firstKey = "direct-model-fixture-token"
	const rejectedKey = "rejected-model-fixture-token"
	first, firstAuth := managementTriggerModelProbe(t, true)
	second, secondAuth := managementTriggerModelProbe(t, true)
	failing, failingAuth := managementTriggerModelProbe(t, false)

	var stored MessageTriggerModel
	saves := 0
	server := NewManagementServer(0, testMessageTriggerModelManagementToken, nil)
	server.SetMessageTriggerModelAccess(func() (MessageTriggerModel, error) {
		return stored, nil
	}, func(model MessageTriggerModel) error {
		stored = model
		saves++
		return nil
	})
	handler := server.buildHandler(http.NewServeMux())

	code, response := requestManagementMessageTriggerModel(t, handler, http.MethodPut, map[string]any{
		"base_url": first.URL + "/v1", "model": "classifier-fixture", "api_key": firstKey,
	})
	assertManagementTriggerModelKeyStatus(t, code, response, true, firstKey)
	assertManagementTriggerModelProbeAuth(t, firstAuth, "Bearer "+firstKey)
	if stored.APIKey != firstKey || saves != 1 {
		t.Fatal("successful probe did not persist the submitted direct key")
	}

	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodGet, nil)
	assertManagementTriggerModelKeyStatus(t, code, response, true, firstKey)

	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodPut, map[string]any{
		"base_url": first.URL + "/v1/", "model": "classifier-fixture-v2",
	})
	assertManagementTriggerModelKeyStatus(t, code, response, true, firstKey)
	assertManagementTriggerModelProbeAuth(t, firstAuth, "Bearer "+firstKey)
	if stored.APIKey != firstKey || stored.Model != "classifier-fixture-v2" || saves != 2 {
		t.Fatal("same base URL update discarded or changed the saved key")
	}

	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodPut, map[string]any{
		"base_url": first.URL + "/v1", "model": stored.Model, "api_key": "  ",
	})
	if code != http.StatusBadRequest || response.OK || stored.APIKey != firstKey || saves != 2 {
		t.Fatal("whitespace-only input changed the saved key")
	}
	select {
	case <-firstAuth:
		t.Fatal("invalid key input probed the endpoint")
	default:
	}

	beforeFailure := stored
	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodPut, map[string]any{
		"base_url": failing.URL + "/v1", "model": "classifier-fixture", "api_key": rejectedKey,
	})
	assertManagementTriggerModelProbeAuth(t, failingAuth, "Bearer "+rejectedKey)
	if code != http.StatusBadRequest || response.OK || !strings.Contains(response.Error, "model structured output test failed") {
		t.Fatalf("failed model probe returned HTTP %d, want 400", code)
	}
	if stored != beforeFailure || saves != 2 {
		t.Fatal("failed model probe wrote settings")
	}
	if strings.Contains(response.Error, rejectedKey) || strings.Contains(response.Error, "fixture upstream response") {
		t.Fatal("failed model probe exposed credentials or upstream response details")
	}

	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodPut, map[string]any{
		"base_url": second.URL + "/v1", "model": "classifier-fixture",
	})
	assertManagementTriggerModelKeyStatus(t, code, response, false, firstKey, rejectedKey)
	assertManagementTriggerModelProbeAuth(t, secondAuth, "")
	if stored.APIKey != "" || stored.APIKeyEnv != "" || stored.BaseURL != second.URL+"/v1" || saves != 3 {
		t.Fatal("changing the base URL kept incompatible credentials in settings")
	}

	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodGet, nil)
	assertManagementTriggerModelKeyStatus(t, code, response, false, firstKey, rejectedKey)
}

func TestMgmtMessageTriggerModelLegacyCredentialDoesNotLeaveBaseURL(t *testing.T) {
	const envName = "CC_TRIGGER_MODEL_MANAGEMENT_TEST_KEY"
	const envValue = "legacy-model-fixture-token"
	t.Setenv(envName, envValue)
	first, firstAuth := managementTriggerModelProbe(t, true)
	second, secondAuth := managementTriggerModelProbe(t, true)
	stored := MessageTriggerModel{BaseURL: first.URL + "/v1", Model: "classifier-fixture", APIKeyEnv: envName}
	server := NewManagementServer(0, testMessageTriggerModelManagementToken, nil)
	server.SetMessageTriggerModelAccess(func() (MessageTriggerModel, error) {
		return stored, nil
	}, func(model MessageTriggerModel) error {
		stored = model
		return nil
	})
	handler := server.buildHandler(http.NewServeMux())

	code, response := requestManagementMessageTriggerModel(t, handler, http.MethodGet, nil)
	assertManagementTriggerModelKeyStatus(t, code, response, true, envName, envValue)
	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodPut, map[string]any{
		"base_url": first.URL + "/v1", "model": "classifier-fixture",
	})
	assertManagementTriggerModelKeyStatus(t, code, response, true, envName, envValue)
	assertManagementTriggerModelProbeAuth(t, firstAuth, "Bearer "+envValue)
	if stored.APIKeyEnv != envName {
		t.Fatal("updating the original endpoint discarded the environment credential")
	}

	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodPut, map[string]any{
		"base_url": second.URL + "/v1", "model": "classifier-fixture",
	})
	assertManagementTriggerModelKeyStatus(t, code, response, false, envName, envValue)
	assertManagementTriggerModelProbeAuth(t, secondAuth, "")
	if stored.APIKeyEnv != "" || stored.APIKey != "" {
		t.Fatal("changing endpoints retained the legacy credentials")
	}
}

func TestMgmtMessageTriggerModelClearMissingLegacyCredential(t *testing.T) {
	const envName = "CC_TRIGGER_MODEL_MANAGEMENT_MISSING_KEY"
	t.Setenv(envName, "")
	endpoint, auth := managementTriggerModelProbe(t, true)
	stored := MessageTriggerModel{BaseURL: endpoint.URL + "/v1", Model: "classifier-fixture", APIKeyEnv: envName}
	server := NewManagementServer(0, testMessageTriggerModelManagementToken, nil)
	server.SetMessageTriggerModelAccess(func() (MessageTriggerModel, error) {
		return stored, nil
	}, func(model MessageTriggerModel) error {
		stored = model
		return nil
	})
	handler := server.buildHandler(http.NewServeMux())

	code, response := requestManagementMessageTriggerModel(t, handler, http.MethodGet, nil)
	assertManagementTriggerModelKeyStatus(t, code, response, true, envName)
	code, response = requestManagementMessageTriggerModel(t, handler, http.MethodPut, map[string]any{
		"base_url": stored.BaseURL, "model": stored.Model, "api_key": "",
	})
	assertManagementTriggerModelKeyStatus(t, code, response, false, envName)
	assertManagementTriggerModelProbeAuth(t, auth, "")
	if stored.APIKeyEnv != "" {
		t.Fatal("the cleared environment key remained in the model settings")
	}
}
