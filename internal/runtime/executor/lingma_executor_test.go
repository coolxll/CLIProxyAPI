package executor

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestLingmaExecutorExecuteReturns501ForOpenAIResponseFormat(t *testing.T) {
	e := &LingmaExecutor{}
	req := cliproxyexecutor.Request{Model: "test-model", Payload: []byte(`{}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format("openai-response")}
	_, err := e.Execute(context.Background(), nil, req, opts)
	if err == nil {
		t.Fatal("expected error for openai-response format, got nil")
	}
	statusErr, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}
	if statusErr.code != http.StatusNotImplemented {
		t.Fatalf("status code = %d, want %d", statusErr.code, http.StatusNotImplemented)
	}
}

func TestLingmaExecutorExecuteStreamReturns501ForOpenAIResponseFormat(t *testing.T) {
	e := &LingmaExecutor{}
	req := cliproxyexecutor.Request{Model: "test-model", Payload: []byte(`{}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format("openai-response")}
	_, err := e.ExecuteStream(context.Background(), nil, req, opts)
	if err == nil {
		t.Fatal("expected error for openai-response format, got nil")
	}
	statusErr, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}
	if statusErr.code != http.StatusNotImplemented {
		t.Fatalf("status code = %d, want %d", statusErr.code, http.StatusNotImplemented)
	}
}

func TestLingmaExecutorCountTokensReturnsApproximation(t *testing.T) {
	e := &LingmaExecutor{}
	payload := []byte(`{"model":"test","messages":[{"role":"user","content":"Hello, how are you?"}],"stream":false}`)
	req := cliproxyexecutor.Request{Model: "test-model", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format("claude")}
	resp, err := e.CountTokens(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("CountTokens returned empty payload")
	}
}

func TestPreserveLingmaClaudeCodeThinkingAdaptiveEffort(t *testing.T) {
	body := []byte(`{"model_config":{"is_reasoning":false}}`)
	source := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`)

	out := preserveLingmaClaudeCodeThinking(body, source, "claude")

	if got := gjson.GetBytes(out, "model_config.is_reasoning").Bool(); !got {
		t.Fatalf("model_config.is_reasoning = %v, want true; body=%s", got, string(out))
	}
}

func TestPreserveLingmaClaudeCodeThinkingDisabled(t *testing.T) {
	body := []byte(`{"model_config":{"is_reasoning":true}}`)
	source := []byte(`{"thinking":{"type":"disabled"}}`)

	out := preserveLingmaClaudeCodeThinking(body, source, "claude")

	if got := gjson.GetBytes(out, "model_config.is_reasoning").Bool(); got {
		t.Fatalf("model_config.is_reasoning = %v, want false; body=%s", got, string(out))
	}
}

func TestParseLingmaModelsCategorizedResponse(t *testing.T) {
	raw := []byte(`{
		"chat": [
			{"key": "dashscope_qmodel", "display_name": "DashScope QModel"}
		],
		"developer": {
			"dev": {"modelId": "dev_model", "displayName": "Developer Model"}
		}
	}`)

	models := parseLingmaModels(raw, 123)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "dashscope_qmodel" || models[0].DisplayName != "DashScope QModel" {
		t.Fatalf("models[0] = %#v", models[0])
	}
	if models[1].ID != "dev_model" || models[1].DisplayName != "Developer Model" {
		t.Fatalf("models[1] = %#v", models[1])
	}
}

func TestParseLingmaModelsArrayResponse(t *testing.T) {
	raw := []byte(`[
		{"modelName": "qwen-2.5-max"},
		{"id": "qwen-2.5-max"},
		{"model_id": "qwen-coder", "name": "Qwen Coder"}
	]`)

	models := parseLingmaModels(raw, 123)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "qwen-2.5-max" {
		t.Fatalf("models[0].ID = %q, want qwen-2.5-max", models[0].ID)
	}
	if models[1].ID != "qwen-coder" || models[1].DisplayName != "Qwen Coder" {
		t.Fatalf("models[1] = %#v", models[1])
	}
}

// TestLingmaChatURLAgentIDMatchesBody verifies that the URL's AgentId query
// param matches the body's agent_id after translation, for both reasoning-on
// (agent_chat) and reasoning-off (agent_common) requests. The translator may
// flip agent_id to agent_common when reasoning is disabled; the URL builder
// must follow so URL and body never disagree.
func TestLingmaChatURLAgentIDMatchesBody(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		payload   string
		wantAgent string
	}{
		{
			name:      "reasoning_off_forces_agent_common",
			model:     "gm51model",
			payload:   `{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":true,"reasoning_effort":"none"}`,
			wantAgent: "agent_common",
		},
		{
			name:      "reasoning_on_keeps_agent_chat",
			model:     "gm51model",
			payload:   `{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantAgent: "agent_chat",
		},
		{
			name:      "agent_common_model_stays_agent_common",
			model:     "kmodel",
			payload:   `{"model":"kmodel","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantAgent: "agent_common",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from := sdktranslator.FormatOpenAI
			to := sdktranslator.FromString("lingma")
			body := sdktranslator.TranslateRequest(from, to, tc.model, []byte(tc.payload), true)
			// ApplyThinking runs after translation in the executor; it must not
			// change agent_id (only model_config.is_reasoning). Run it to mirror
			// the real pipeline and assert agent_id is stable.
			body, _ = thinking.ApplyThinking(body, tc.model, from.String(), to.String(), "lingma")

			bodyAgent := gjson.GetBytes(body, "agent_id").String()
			if bodyAgent != tc.wantAgent {
				t.Fatalf("body agent_id = %q, want %q", bodyAgent, tc.wantAgent)
			}

			// The executor builds the URL from the final body's agent_id.
			chatURL := lingmaChatURLForAgent(lingmaAgentIDFromBody(body, tc.model))
			u, err := url.Parse(chatURL)
			if err != nil {
				t.Fatalf("parse chatURL: %v", err)
			}
			urlAgent := u.Query().Get("AgentId")
			if urlAgent != bodyAgent {
				t.Fatalf("URL AgentId = %q, body agent_id = %q; they must match", urlAgent, bodyAgent)
			}
		})
	}
}

// TestLingmaAgentIDFromBodyFallback asserts the defensive fallback: when the
// body lacks an agent_id field, we derive it from the model name so the URL
// still gets a valid AgentId rather than empty.
func TestLingmaAgentIDFromBodyFallback(t *testing.T) {
	got := lingmaAgentIDFromBody([]byte(`{"model_config":{}}`), "gm51model")
	if got != "agent_chat" {
		t.Fatalf("lingmaAgentIDFromBody fallback = %q, want agent_chat", got)
	}
	got = lingmaAgentIDFromBody([]byte(`{"agent_id":"agent_common"}`), "gm51model")
	if got != "agent_common" {
		t.Fatalf("lingmaAgentIDFromBody = %q, want agent_common", got)
	}
}
