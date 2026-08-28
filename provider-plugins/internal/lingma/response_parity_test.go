package lingma

import (
	"context"
	"encoding/json"
	"testing"

	nativeClaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/claude"
	nativeOpenAI "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/openai/responses"
	pluginClaude "github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/claude"
	pluginOpenAI "github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/openai/response"
)

// TestOpenAIResponseParityStream verifies plugin and native produce identical
// OpenAI streaming chunks for the same Lingma SSE input.
func TestOpenAIResponseParityStream(t *testing.T) {
	lingmaSSE := []byte(`data: {"body":"{\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}"}\n\ndata: {"body":"{\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}]}"}\n\ndata: [DONE]\n\n`)

	var pluginState, nativeState any
	pluginChunks := pluginOpenAI.ConvertLingmaResponseToOpenAI(context.Background(), "gm51model", nil, nil, lingmaSSE, &pluginState)
	nativeChunks := nativeOpenAI.ConvertLingmaResponseToOpenAI(context.Background(), "gm51model", nil, nil, lingmaSSE, &nativeState)

	if len(pluginChunks) != len(nativeChunks) {
		t.Fatalf("chunk count mismatch: plugin=%d, native=%d", len(pluginChunks), len(nativeChunks))
	}

	for i := range pluginChunks {
		var pluginJSON, nativeJSON map[string]any
		if err := json.Unmarshal(pluginChunks[i], &pluginJSON); err != nil {
			t.Fatalf("parse plugin chunk[%d]: %v", i, err)
		}
		if err := json.Unmarshal(nativeChunks[i], &nativeJSON); err != nil {
			t.Fatalf("parse native chunk[%d]: %v", i, err)
		}
		compareJSON(t, pluginJSON, nativeJSON, []string{"id"}, "chunk[]")
	}
}

// TestOpenAIResponseParityNonStream verifies plugin and native produce identical
// OpenAI non-stream responses for the same Lingma aggregated response.
func TestOpenAIResponseParityNonStream(t *testing.T) {
	lingmaResponse := []byte(`{"body":"{\"choices\":[{\"message\":{\"content\":\"Hello world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}"}`)

	pluginResult := pluginOpenAI.ConvertLingmaResponseToOpenAINonStream(context.Background(), "gm51model", nil, nil, lingmaResponse, nil)
	nativeResult := nativeOpenAI.ConvertLingmaResponseToOpenAINonStream(context.Background(), "gm51model", nil, nil, lingmaResponse, nil)

	var pluginJSON, nativeJSON map[string]any
	if err := json.Unmarshal(pluginResult, &pluginJSON); err != nil {
		t.Fatalf("parse plugin result: %v", err)
	}
	if err := json.Unmarshal(nativeResult, &nativeJSON); err != nil {
		t.Fatalf("parse native result: %v", err)
	}

	compareJSON(t, pluginJSON, nativeJSON, []string{"id"}, "result")
}

// TestClaudeResponseParityStream verifies plugin and native produce identical
// Claude streaming chunks for the same Lingma SSE input.
func TestClaudeResponseParityStream(t *testing.T) {
	lingmaSSE := []byte(`data: {"body":"{\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}"}\n\ndata: {"body":"{\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}]}"}\n\ndata: [DONE]\n\n`)

	var pluginState, nativeState any
	pluginChunks := pluginClaude.ConvertLingmaResponseToClaude(context.Background(), "gm51model", nil, nil, lingmaSSE, &pluginState)
	nativeChunks := nativeClaude.ConvertLingmaResponseToClaude(context.Background(), "gm51model", nil, nil, lingmaSSE, &nativeState)

	if len(pluginChunks) != len(nativeChunks) {
		t.Fatalf("chunk count mismatch: plugin=%d, native=%d", len(pluginChunks), len(nativeChunks))
	}

	for i := range pluginChunks {
		var pluginJSON, nativeJSON map[string]any
		if err := json.Unmarshal(pluginChunks[i], &pluginJSON); err != nil {
			t.Fatalf("parse plugin chunk[%d]: %v", i, err)
		}
		if err := json.Unmarshal(nativeChunks[i], &nativeJSON); err != nil {
			t.Fatalf("parse native chunk[%d]: %v", i, err)
		}
		compareJSON(t, pluginJSON, nativeJSON, []string{"id"}, "chunk[]")
	}
}

// TestClaudeResponseParityNonStream verifies plugin and native produce identical
// Claude non-stream responses for the same Lingma aggregated response.
func TestClaudeResponseParityNonStream(t *testing.T) {
	lingmaResponse := []byte(`{"body":"{\"choices\":[{\"message\":{\"content\":\"Hello world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}"}`)

	pluginResult := pluginClaude.ConvertLingmaResponseToClaudeNonStream(context.Background(), "gm51model", nil, nil, lingmaResponse, nil)
	nativeResult := nativeClaude.ConvertLingmaResponseToClaudeNonStream(context.Background(), "gm51model", nil, nil, lingmaResponse, nil)

	var pluginJSON, nativeJSON map[string]any
	if err := json.Unmarshal(pluginResult, &pluginJSON); err != nil {
		t.Fatalf("parse plugin result: %v", err)
	}
	if err := json.Unmarshal(nativeResult, &nativeJSON); err != nil {
		t.Fatalf("parse native result: %v", err)
	}

	compareJSON(t, pluginJSON, nativeJSON, []string{"id"}, "result")
}
