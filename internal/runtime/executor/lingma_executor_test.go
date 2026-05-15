package executor

import (
	"context"
	"net/http"
	"testing"

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
