package helpers

import "testing"

func TestIsAgentCommonModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"kmodel", true},
		{"mmodel", true},
		{"KMODEL", true},
		{"Mmodel", true},
		{" kmodel ", true},
		{"dashscope_qmodel", false},
		{"org_auto", false},
		{"dashscope_qwen_plus_20250428_thinking", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsAgentCommonModel(tt.model); got != tt.want {
			t.Errorf("IsAgentCommonModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestAgentID(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"kmodel", "agent_common"},
		{"mmodel", "agent_common"},
		{"dashscope_qmodel", "agent_chat"},
		{"org_auto", "agent_chat"},
		{"", "agent_chat"},
	}
	for _, tt := range tests {
		if got := AgentID(tt.model); got != tt.want {
			t.Errorf("AgentID(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestModelConfigSource(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"kmodel", ""},
		{"mmodel", ""},
		{"dashscope_qmodel", "system"},
		{"org_auto", "system"},
		{"", "system"},
	}
	for _, tt := range tests {
		if got := ModelConfigSource(tt.model); got != tt.want {
			t.Errorf("ModelConfigSource(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}
