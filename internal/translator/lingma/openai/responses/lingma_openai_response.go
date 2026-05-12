package responses

import (
	"context"

	"github.com/tidwall/gjson"
)

// ConvertLingmaResponseToOpenAI normalizes a single chunk of a Lingma streaming response to OpenAI format.
func ConvertLingmaResponseToOpenAI(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) [][]byte {
	// Handle SSE data prefix if present (though usually rawJSON is the data part)
	// In CLIProxyAPI, rawJSON is usually just the data part of the SSE line.

	res := gjson.ParseBytes(rawJSON)

	// 1. Try to parse as the double-JSON envelope: {"body":"..."}
	if body := res.Get("body"); body.Exists() && body.Type == gjson.String {
		inner := body.String()
		if inner == "[DONE]" {
			return [][]byte{}
		}
		return [][]byte{[]byte(inner)}
	}

	// 2. Try to parse as finish event/usage event: {"usage":...}
	// We might want to convert this to an OpenAI finish chunk if it's not already.
	if usage := res.Get("usage"); usage.Exists() && !res.Get("choices").Exists() {
		// Normalizing usage info to OpenAI format if needed
		// For now, if it has usage but no choices, it's a metadata chunk.
		// OpenAI standard expects choices in every chunk except maybe the last one with usage.
		return [][]byte{rawJSON}
	}

	// 3. Fallback: if it already looks like OpenAI (has choices), pass through.
	if res.Get("choices").Exists() {
		return [][]byte{rawJSON}
	}

	// If it's [DONE], return empty
	if string(rawJSON) == "[DONE]" {
		return [][]byte{}
	}

	return [][]byte{rawJSON}
}

// ConvertLingmaResponseToOpenAINonStream translates a non-streaming Lingma response.
func ConvertLingmaResponseToOpenAINonStream(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) []byte {
	res := gjson.ParseBytes(rawJSON)

	// Handle double-JSON envelope
	if body := res.Get("body"); body.Exists() && body.Type == gjson.String {
		return []byte(body.String())
	}

	return rawJSON
}
