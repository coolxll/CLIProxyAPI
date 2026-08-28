package lingma

import (
	"encoding/json"
	"testing"
)

// TestPreserveClaudeCodeThinkingParity verifies plugin and native produce identical
// results when preserving Claude Code thinking configuration.
func TestPreserveClaudeCodeThinkingParity(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		source       string
		sourceFormat string
	}{
		{
			name:         "effort_none",
			body:         `{"model_config":{"is_reasoning":true,"source":"system"},"agent_id":"agent_chat"}`,
			source:       `{"output_config":{"effort":"none"}}`,
			sourceFormat: "claude",
		},
		{
			name:         "effort_off",
			body:         `{"model_config":{"is_reasoning":true,"source":"system"},"agent_id":"agent_chat"}`,
			source:       `{"output_config":{"effort":"off"}}`,
			sourceFormat: "claude",
		},
		{
			name:         "effort_disabled",
			body:         `{"model_config":{"is_reasoning":true,"source":"system"},"agent_id":"agent_chat"}`,
			source:       `{"output_config":{"effort":"disabled"}}`,
			sourceFormat: "claude",
		},
		{
			name:         "effort_active",
			body:         `{"model_config":{"is_reasoning":false,"source":""},"agent_id":"agent_common"}`,
			source:       `{"output_config":{"effort":"active"}}`,
			sourceFormat: "claude",
		},
		{
			name:         "thinking_type_disabled",
			body:         `{"model_config":{"is_reasoning":true,"source":"system"},"agent_id":"agent_chat"}`,
			source:       `{"thinking":{"type":"disabled"}}`,
			sourceFormat: "claude",
		},
		{
			name:         "thinking_type_enabled",
			body:         `{"model_config":{"is_reasoning":false,"source":""},"agent_id":"agent_common"}`,
			source:       `{"thinking":{"type":"enabled","budget_tokens":1000}}`,
			sourceFormat: "claude",
		},
		{
			name:         "thinking_type_enabled_zero_budget",
			body:         `{"model_config":{"is_reasoning":false,"source":""},"agent_id":"agent_common"}`,
			source:       `{"thinking":{"type":"enabled","budget_tokens":0}}`,
			sourceFormat: "claude",
		},
		{
			name:         "thinking_type_adaptive",
			body:         `{"model_config":{"is_reasoning":false,"source":""},"agent_id":"agent_common"}`,
			source:       `{"thinking":{"type":"adaptive"}}`,
			sourceFormat: "claude",
		},
		{
			name:         "thinking_type_auto",
			body:         `{"model_config":{"is_reasoning":false,"source":""},"agent_id":"agent_common"}`,
			source:       `{"thinking":{"type":"auto"}}`,
			sourceFormat: "claude",
		},
		{
			name:         "non_claude_format",
			body:         `{"model_config":{"is_reasoning":true,"source":"system"},"agent_id":"agent_chat"}`,
			source:       `{"output_config":{"effort":"none"}}`,
			sourceFormat: "openai",
		},
		{
			name:         "empty_source",
			body:         `{"model_config":{"is_reasoning":true,"source":"system"},"agent_id":"agent_chat"}`,
			source:       ``,
			sourceFormat: "claude",
		},
		{
			name:         "invalid_source_json",
			body:         `{"model_config":{"is_reasoning":true,"source":"system"},"agent_id":"agent_chat"}`,
			source:       `not json`,
			sourceFormat: "claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginResult := preserveClaudeCodeThinking([]byte(tt.body), []byte(tt.source), tt.sourceFormat)
			nativeResult := nativePreserveLingmaClaudeCodeThinkingForTest([]byte(tt.body), []byte(tt.source), tt.sourceFormat)

			var pluginJSON, nativeJSON map[string]any
			if err := json.Unmarshal(pluginResult, &pluginJSON); err != nil {
				t.Fatalf("parse plugin result: %v\nraw: %s", err, pluginResult)
			}
			if err := json.Unmarshal(nativeResult, &nativeJSON); err != nil {
				t.Fatalf("parse native result: %v\nraw: %s", err, nativeResult)
			}

			compareJSON(t, pluginJSON, nativeJSON, nil, "result")
		})
	}
}

// TestClaudeCodeThinkingEnabledParity verifies plugin and native produce identical
// results when determining if Claude Code thinking is enabled.
func TestClaudeCodeThinkingEnabledParity(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{"effort_none", `{"output_config":{"effort":"none"}}`},
		{"effort_off", `{"output_config":{"effort":"off"}}`},
		{"effort_disabled", `{"output_config":{"effort":"disabled"}}`},
		{"effort_active", `{"output_config":{"effort":"active"}}`},
		{"effort_high", `{"output_config":{"effort":"high"}}`},
		{"effort_empty", `{"output_config":{"effort":""}}`},
		{"thinking_disabled", `{"thinking":{"type":"disabled"}}`},
		{"thinking_enabled", `{"thinking":{"type":"enabled","budget_tokens":1000}}`},
		{"thinking_enabled_zero", `{"thinking":{"type":"enabled","budget_tokens":0}}`},
		{"thinking_adaptive", `{"thinking":{"type":"adaptive"}}`},
		{"thinking_auto", `{"thinking":{"type":"auto"}}`},
		{"thinking_unknown", `{"thinking":{"type":"unknown"}}`},
		{"no_thinking_fields", `{"other":"field"}`},
		{"empty", `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginEnabled, pluginOK := claudeCodeThinkingEnabled([]byte(tt.source))
			nativeEnabled, nativeOK := nativeClaudeCodeThinkingEnabledForTest([]byte(tt.source))

			if pluginOK != nativeOK {
				t.Errorf("ok mismatch: plugin=%v, native=%v", pluginOK, nativeOK)
			}
			if pluginOK && nativeOK && pluginEnabled != nativeEnabled {
				t.Errorf("enabled mismatch: plugin=%v, native=%v", pluginEnabled, nativeEnabled)
			}
		})
	}
}
