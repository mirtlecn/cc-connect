package core

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func (m *ManagementServer) SetMessageTriggerConfigAccess(
	get func(string) ([]MessageTriggerRule, MessageTriggerModel, error),
	save func(string, string, []MessageTriggerRule) ([]MessageTriggerRule, MessageTriggerModel, error),
) {
	m.getMessageTriggers = get
	m.saveMessageTriggers = save
}

func (m *ManagementServer) SetMessageTriggerModelAccess(
	get func() (MessageTriggerModel, error), save func(MessageTriggerModel) error,
) {
	m.getMessageTriggerModel = get
	m.saveMessageTriggerModel = save
}

type messageTriggerModelResponse struct {
	MessageTriggerModel
	KeyConfigured bool `json:"key_configured"`
}

func publicMessageTriggerModel(model MessageTriggerModel) messageTriggerModelResponse {
	model.APIType = model.protocol()
	return messageTriggerModelResponse{MessageTriggerModel: model, KeyConfigured: model.APIKey != "" || model.APIKeyEnv != ""}
}

func (m *ManagementServer) handleMessageTriggerModel(w http.ResponseWriter, r *http.Request) {
	if m.token == "" && !isLocalTriggerManagementRequest(r) {
		mgmtError(w, http.StatusForbidden, "message trigger configuration requires a management token or local same-origin access")
		return
	}
	if m.getMessageTriggerModel == nil || m.saveMessageTriggerModel == nil {
		mgmtError(w, http.StatusServiceUnavailable, "message trigger model configuration unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		model, err := m.getMessageTriggerModel()
		if err != nil {
			slog.Warn("message trigger: model configuration could not be read")
			mgmtError(w, http.StatusInternalServerError, "message trigger model could not be read")
			return
		}
		mgmtJSON(w, http.StatusOK, publicMessageTriggerModel(model))
	case http.MethodPut:
		m.saveVerifiedMessageTriggerModel(w, r)
	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or PUT only")
	}
}

func (m *ManagementServer) saveVerifiedMessageTriggerModel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MessageTriggerModel
		APIKey *string `json:"api_key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
		mgmtError(w, http.StatusBadRequest, "invalid model configuration")
		return
	}
	model := input.MessageTriggerModel
	model.BaseURL = strings.TrimSpace(model.BaseURL)
	model.Model = strings.TrimSpace(model.Model)
	model.APIType = model.protocol()
	m.messageTriggerModelMu.Lock()
	defer m.messageTriggerModelMu.Unlock()
	if input.APIKey != nil {
		model.APIKey = strings.TrimSpace(*input.APIKey)
		if model.APIKey == "" && *input.APIKey != "" {
			mgmtError(w, http.StatusBadRequest, "api_key must not be whitespace only")
			return
		}
	} else {
		previous, err := m.getMessageTriggerModel()
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, "message trigger model could not be read")
			return
		}
		if strings.TrimRight(strings.TrimSpace(previous.BaseURL), "/") == strings.TrimRight(model.BaseURL, "/") {
			model.APIKey = previous.APIKey
			model.APIKeyEnv = previous.APIKeyEnv
		}
	}
	if err := model.Probe(r.Context()); err != nil {
		mgmtError(w, http.StatusBadRequest, "model tool-call test failed: "+err.Error())
		return
	}
	if err := m.saveMessageTriggerModel(model); err != nil {
		slog.Warn("message trigger: model configuration could not be saved")
		mgmtError(w, http.StatusInternalServerError, "message trigger model could not be saved")
		return
	}
	m.mu.RLock()
	for _, e := range m.engines {
		e.updateMessageTriggerModel(model)
	}
	m.mu.RUnlock()
	mgmtJSON(w, http.StatusOK, publicMessageTriggerModel(model))
}

type messageTriggerRulesRequest struct {
	Rules    []MessageTriggerRule `json:"rules"`
	Revision string               `json:"revision"`
}

type messageTriggerRulesResponse struct {
	Rules              []MessageTriggerRule `json:"rules"`
	Revision           string               `json:"revision"`
	SemanticConfigured bool                 `json:"semantic_configured"`
}

func (m *ManagementServer) handleProjectMessageTriggers(w http.ResponseWriter, r *http.Request, name string, e *Engine) {
	if m.token == "" && !isLocalTriggerManagementRequest(r) {
		mgmtError(w, http.StatusForbidden, "message trigger configuration requires a management token or local same-origin access")
		return
	}
	if m.getMessageTriggers == nil || m.saveMessageTriggers == nil {
		mgmtError(w, http.StatusServiceUnavailable, "message trigger configuration unavailable")
		return
	}

	var rules []MessageTriggerRule
	var model MessageTriggerModel
	var err error
	switch r.Method {
	case http.MethodGet:
		rules, model, err = m.getMessageTriggers(name)
	case http.MethodPut:
		var request messageTriggerRulesRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || request.Rules == nil || request.Revision == "" || decoder.Decode(new(any)) != io.EOF {
			mgmtError(w, http.StatusBadRequest, "expected a rules array and revision")
			return
		}
		err = e.ReloadMessageTriggers(func() ([]MessageTriggerRule, MessageTriggerModel, error) {
			rules, model, err = m.saveMessageTriggers(name, request.Revision, request.Rules)
			return rules, model, err
		})
	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or PUT only")
		return
	}

	switch {
	case errors.Is(err, ErrMessageTriggerRulesChanged):
		mgmtError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidMessageTriggerRules):
		mgmtError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.Warn("message trigger configuration failed", "project", name, "operation", r.Method, "error", err)
		mgmtError(w, http.StatusInternalServerError, "message trigger configuration could not be read or saved")
	default:
		if rules == nil {
			rules = []MessageTriggerRule{}
		}
		mgmtJSON(w, http.StatusOK, messageTriggerRulesResponse{
			Rules: rules, Revision: MessageTriggerRulesRevision(rules), SemanticConfigured: model.available(),
		})
	}
}

// The management server can run without a token. Keep a configuration that
// controls local command execution inaccessible to remote requests in that mode.
func isLocalTriggerManagementRequest(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(peer).IsLoopback() {
		return false
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = strings.Trim(r.Host, "[]")
	}
	if !strings.EqualFold(host, "localhost") && !net.ParseIP(host).IsLoopback() {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
	}
	return true
}
