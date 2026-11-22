package core

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var triggerGeminiModelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func geminiTriggerModelID(model string) string {
	id := strings.TrimPrefix(strings.TrimSpace(model), "models/")
	if !triggerGeminiModelIDPattern.MatchString(id) {
		return ""
	}
	return id
}

func (m MessageTriggerModel) geminiEndpoint(base string) (string, error) {
	id := geminiTriggerModelID(m.Model)
	parsed, err := url.Parse(base)
	if id == "" || err != nil {
		return "", errors.New("invalid Gemini model endpoint")
	}
	if strings.Trim(parsed.Path, "/") == "" {
		parsed.Path = "/v1beta"
	}
	return url.JoinPath(parsed.String(), "models", id+":generateContent")
}

func geminiTriggerPayload(content string, images []ImageAttachment) ([]byte, error) {
	parts := []map[string]any{{"text": content}}
	for _, image := range images {
		parts = append(parts, map[string]any{
			"inlineData": map[string]any{"mimeType": image.MimeType, "data": image.Data},
		})
	}
	return json.Marshal(map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": triggerIntentInstructions}}},
		"contents":          []map[string]any{{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{
			"responseMimeType":   "application/json",
			"responseJsonSchema": triggerDecisionSchema,
		},
	})
}

func extractTriggerGeminiText(data []byte) (string, error) {
	var parsed struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	invalid := errors.New("invalid Gemini generateContent result")
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Candidates) != 1 {
		return "", invalid
	}
	candidate := parsed.Candidates[0]
	if candidate.FinishReason != "" && candidate.FinishReason != "STOP" {
		return "", invalid
	}
	var text strings.Builder
	for _, part := range candidate.Content.Parts {
		if !part.Thought {
			text.WriteString(part.Text)
		}
	}
	return text.String(), nil
}
