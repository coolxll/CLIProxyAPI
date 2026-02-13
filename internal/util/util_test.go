package util

import "testing"

func TestAnonymizeString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"short", "abc", "ba7816bf8f01cfea"},
		{"api_key", "sk-1234567890abcdef1234567890abcdef", "2dfacb4231b34bb4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AnonymizeString(tt.input)
			if got != tt.expected {
				t.Errorf("AnonymizeString(%q) = %q, want %q", tt.input, got, tt.expected)
			}
			if tt.input != "" && len(got) != 16 {
				t.Errorf("AnonymizeString(%q) length = %d, want 16", tt.input, len(got))
			}
		})
	}
}

func TestAnonymizeString_Stability(t *testing.T) {
	input := "stable-secret-key"
	got1 := AnonymizeString(input)
	got2 := AnonymizeString(input)
	if got1 != got2 {
		t.Errorf("AnonymizeString is not stable: %q != %q", got1, got2)
	}
}
