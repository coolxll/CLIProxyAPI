package executor

import (
	"testing"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/tidwall/gjson"
)

func TestBuildTraeToolCommitRequestUsesEncodedToolName(t *testing.T) {
	creds := &traeauth.TraeCredentials{UserID: "user-1"}
	encodedID, err := encodeTraeToolID(traeToolState{
		SessionID:      "session-1",
		ConversationID: "conversation-1",
		TaskID:         "task-1",
		AgentRunID:     "run-1",
		NativeID:       "thought-0",
		Name:           "LS",
	})
	if err != nil {
		t.Fatalf("encode tool id: %v", err)
	}

	req, err := buildTraeToolCommitRequest(creds, []gjson.Result{
		gjson.Parse(`{"role":"tool","tool_call_id":"` + encodedID + `","name":"rewritten","content":"ok"}`),
	})
	if err != nil {
		t.Fatalf("build commit request: %v", err)
	}
	if got := gjson.GetBytes(req.LogBody, "toolcall_results.0.toolcall_name").String(); got != "LS" {
		t.Fatalf("toolcall_name = %q, want encoded state name LS; body=%s", got, string(req.LogBody))
	}
}
