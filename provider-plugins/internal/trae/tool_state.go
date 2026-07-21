package trae

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// traeToolState encodes session/conversation/task correlation into tool call IDs
// so that V3 tool commit requests can reconstruct the required context.
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
