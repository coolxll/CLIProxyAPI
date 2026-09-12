package opencode

import (
	"testing"
)

func TestParseExternalToolCalls(t *testing.T) {
	t.Run("function_calls array", func(t *testing.T) {
		text := `Here is the weather:
<function_calls>[{"id":"call_tokyo","name":"weather_lookup","arguments":{"city":"Tokyo","unit":"celsius"}}]</function_calls>
Let me know if you need more.`

		calls, clean := parseExternalToolCalls(text)
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].ID != "call_tokyo" {
			t.Errorf("got call id %q, want call_tokyo", calls[0].ID)
		}
		if calls[0].Function.Name != "weather_lookup" {
			t.Errorf("got fn name %q, want weather_lookup", calls[0].Function.Name)
		}
		if clean != "Here is the weather:\n\nLet me know if you need more." {
			t.Errorf("unexpected clean text: %q", clean)
		}
	})

	t.Run("single tool_call object", func(t *testing.T) {
		text := `<tool_call>{"id":"call_calc","name":"calculator","arguments":{"expr":"2+2"}}</tool_call>`
		calls, clean := parseExternalToolCalls(text)
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Function.Name != "calculator" {
			t.Errorf("got name %q, want calculator", calls[0].Function.Name)
		}
		if clean != "" {
			t.Errorf("expected empty clean text, got %q", clean)
		}
	})

	t.Run("no tool calls", func(t *testing.T) {
		text := "Hello world, how are you today?"
		calls, clean := parseExternalToolCalls(text)
		if len(calls) != 0 {
			t.Errorf("expected 0 calls, got %d", len(calls))
		}
		if clean != text {
			t.Errorf("got clean %q, want %q", clean, text)
		}
	})
}

func TestStripFunctionCallMarkup(t *testing.T) {
	raw := "Result: <function_calls>[...]</function_calls> done."
	clean := stripFunctionCallMarkup(raw)
	if clean != "Result:  done." {
		t.Errorf("unexpected strip result: %q", clean)
	}
}
