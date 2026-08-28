package helpers

import (
	"testing"
)

func TestIsLingmaDone(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected bool
	}{
		{
			name:     "plain DONE",
			input:    []byte("[DONE]"),
			expected: true,
		},
		{
			name:     "DONE with data prefix",
			input:    []byte("data: [DONE]"),
			expected: true,
		},
		{
			name:     "DONE with data prefix and spaces",
			input:    []byte("data:  [DONE]  "),
			expected: true,
		},
		{
			name:     "DONE in JSON envelope",
			input:    []byte(`{"body":"[DONE]"}`),
			expected: true,
		},
		{
			name:     "DONE in JSON envelope with data prefix",
			input:    []byte(`data: {"body":"[DONE]"}`),
			expected: true,
		},
		{
			name:     "DONE in JSON envelope with spaces",
			input:    []byte(`  {"body":"[DONE]"}  `),
			expected: true,
		},
		{
			name:     "normal chunk",
			input:    []byte(`{"choices":[{"delta":{"content":"hello"}}]}`),
			expected: false,
		},
		{
			name:     "normal chunk with data prefix",
			input:    []byte(`data: {"choices":[{"delta":{"content":"hello"}}]}`),
			expected: false,
		},
		{
			name:     "empty body in JSON",
			input:    []byte(`{"body":""}`),
			expected: false,
		},
		{
			name:     "different body value",
			input:    []byte(`{"body":"something else"}`),
			expected: false,
		},
		{
			name:     "invalid JSON",
			input:    []byte(`not valid json`),
			expected: false,
		},
		{
			name:     "empty input",
			input:    []byte(""),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsLingmaDone(tt.input)
			if result != tt.expected {
				t.Errorf("IsLingmaDone(%q) = %v, want %v", string(tt.input), result, tt.expected)
			}
		})
	}
}
