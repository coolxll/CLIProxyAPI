package executor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/tidwall/gjson"
)

type traeToolState struct {
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	TaskID         string `json:"task_id"`
	AgentRunID     string `json:"agent_run_id"`
	NativeID       string `json:"native_id"`
	Name           string `json:"name"`
}

func encodeTraeToolID(state traeToolState) (string, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return "trae_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeTraeToolID(id string) (traeToolState, error) {
	var state traeToolState
	raw := strings.TrimPrefix(id, "trae_")
	if raw == id {
		return state, fmt.Errorf("invalid trae tool_call_id prefix")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, err
	}
	if state.SessionID == "" || state.ConversationID == "" || state.TaskID == "" || state.AgentRunID == "" || state.NativeID == "" {
		return state, fmt.Errorf("incomplete trae tool state")
	}
	return state, nil
}

func openAIMessageText(message gjson.Result) string {
	content := message.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var builder strings.Builder
		for _, part := range content.Array() {
			text := firstNonEmpty(
				part.Get("text").String(),
				part.Get("text_content").String(),
				part.Get("input_text").String(),
			)
			if text == "" {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(text)
		}
		return builder.String()
	}
	return content.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func setTraeCommonHeaders(header http.Header, creds *traeauth.TraeCredentials) {
	header.Set("Authorization", "Cloud-IDE-JWT "+creds.JWTToken)
	header.Set("X-App-Id", "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8")
	header.Set("x-app-version", "default")
	header.Set("x-ide-version-code", "20260508")
	header.Set("x-app-version-code", "20260401")
	header.Set("x-device-brand", "Lenovo")
	header.Set("x-device-cpu", "AMD")
	header.Set("x-device-id", creds.DeviceID)
	header.Set("x-machine-id", creds.MachineID)
	header.Set("x-os-version", "Linux")
	header.Set("x-device-type", "linux")
	header.Set("x-ide-version", "3.3.55")
	header.Set("x-ide-version-type", "stable")
	header.Set("request-traffic-type", "prod")
	header.Set("get-svc", "1")
}
