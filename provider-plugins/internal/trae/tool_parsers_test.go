package trae

import (
	"testing"
)

func TestTraeThoughtToolParser(t *testing.T) {
	t.Run("plain text", func(t *testing.T) {
		var parser traeThoughtToolParser
		result := parser.Append("This is plain text without tool calls")
		flushed := parser.Flush()
		result.Content += flushed

		if result.Content != "This is plain text without tool calls" {
			t.Errorf("Content mismatch: got %q", result.Content)
		}
		if len(result.ToolCalls) != 0 {
			t.Errorf("ToolCalls count mismatch: got %d", len(result.ToolCalls))
		}
	})
}

func TestTraeInlineToolCallParser(t *testing.T) {
	t.Run("plain text", func(t *testing.T) {
		var parser traeInlineToolCallParser
		result := parser.Append("Regular text without tool calls")
		flushed := parser.Flush()
		result.Content += flushed

		if result.Content != "Regular text without tool calls" {
			t.Errorf("Content mismatch: got %q", result.Content)
		}
		if len(result.ToolCalls) != 0 {
			t.Errorf("ToolCalls count mismatch: got %d", len(result.ToolCalls))
		}
	})

	t.Run("tool_calls JSON format", func(t *testing.T) {
		var parser traeInlineToolCallParser
		input := `tool_calls=[{"name":"Bash","arguments":{"command":"echo test"}}]`
		result := parser.Append(input)
		flushed := parser.Flush()
		result.Content += flushed

		if result.Content != "" {
			t.Errorf("Content mismatch: got %q", result.Content)
		}
		if len(result.ToolCalls) != 1 {
			t.Errorf("ToolCalls count mismatch: got %d, want 1", len(result.ToolCalls))
		}
	})
}

func TestDeepSeekThinkTagStripper(t *testing.T) {
	t.Run("no think tags", func(t *testing.T) {
		var stripper deepSeekThinkTagStripper
		result := stripper.Append("Regular text")
		result += stripper.Flush()

		if result != "Regular text" {
			t.Errorf("Output mismatch: got %q", result)
		}
	})
}

func TestStripTrailingToolCallResidue(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		hasToolCall bool
		wantOutput  string
	}{
		{
			name:        "no residue without tool call",
			input:       "Clean text",
			hasToolCall: false,
			wantOutput:  "Clean text",
		},
		{
			name:        "only trailing braces with tool call",
			input:       "}}",
			hasToolCall: true,
			wantOutput:  "",
		},
		{
			name:        "only trailing brackets with tool call",
			input:       "]]",
			hasToolCall: true,
			wantOutput:  "",
		},
		{
			name:        "only mixed trailing with tool call",
			input:       "}]",
			hasToolCall: true,
			wantOutput:  "",
		},
		{
			name:        "non-trailing characters preserved",
			input:       "Text with abc",
			hasToolCall: true,
			wantOutput:  "Text with abc",
		},
		{
			name:        "empty string",
			input:       "",
			hasToolCall: true,
			wantOutput:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripTrailingToolCallResidue(tt.input, tt.hasToolCall)

			if result != tt.wantOutput {
				t.Errorf("Output mismatch: got %q, want %q", result, tt.wantOutput)
			}
		})
	}
}
