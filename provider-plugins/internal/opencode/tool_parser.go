package opencode

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

var (
	functionCallsTagRegex = regexp.MustCompile(`(?s)<function_calls>(.*?)</function_calls>`)
	toolCallTagRegex      = regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>`)
)

type parsedToolCallInput struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// parseExternalToolCalls extracts function calls embedded in text markup.
func parseExternalToolCalls(text string) ([]openaiToolCall, string) {
	clean := text
	var toolCalls []openaiToolCall

	// 1. Check <function_calls>...</function_calls>
	matches := functionCallsTagRegex.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(m) > 1 {
			rawJSON := strings.TrimSpace(m[1])
			parsed := parseCallsJSON(rawJSON)
			toolCalls = append(toolCalls, parsed...)
		}
	}
	clean = functionCallsTagRegex.ReplaceAllString(clean, "")

	// 2. Check <tool_call>...</tool_call>
	toolMatches := toolCallTagRegex.FindAllStringSubmatch(clean, -1)
	for _, m := range toolMatches {
		if len(m) > 1 {
			rawJSON := strings.TrimSpace(m[1])
			parsed := parseSingleOrMultiCallJSON(rawJSON)
			toolCalls = append(toolCalls, parsed...)
		}
	}
	clean = toolCallTagRegex.ReplaceAllString(clean, "")

	return toolCalls, strings.TrimSpace(clean)
}

func parseCallsJSON(raw string) []openaiToolCall {
	var results []openaiToolCall
	parsed := gjson.Parse(raw)
	if !parsed.IsArray() {
		return parseSingleOrMultiCallJSON(raw)
	}

	for i, item := range parsed.Array() {
		callID := item.Get("id").String()
		if callID == "" {
			callID = fmt.Sprintf("call_%s", strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
		}
		name := item.Get("name").String()
		if name == "" {
			name = item.Get("function.name").String()
		}
		if name == "" {
			continue
		}
		args := item.Get("arguments").Raw
		if args == "" {
			args = item.Get("function.arguments").Raw
		}
		if args == "" {
			args = "{}"
		}

		results = append(results, openaiToolCall{
			Index: i,
			ID:    callID,
			Type:  "function",
			Function: openaiToolFunction{
				Name:      name,
				Arguments: args,
			},
		})
	}
	return results
}

func parseSingleOrMultiCallJSON(raw string) []openaiToolCall {
	var results []openaiToolCall
	parsed := gjson.Parse(raw)
	if parsed.IsObject() {
		callID := parsed.Get("id").String()
		if callID == "" {
			callID = fmt.Sprintf("call_%s", strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
		}
		name := parsed.Get("name").String()
		if name == "" {
			name = parsed.Get("function.name").String()
		}
		if name != "" {
			args := parsed.Get("arguments").Raw
			if args == "" {
				args = parsed.Get("function.arguments").Raw
			}
			if args == "" {
				args = "{}"
			}
			results = append(results, openaiToolCall{
				Index: 0,
				ID:    callID,
				Type:  "function",
				Function: openaiToolFunction{
					Name:      name,
					Arguments: args,
				},
			})
		}
	}
	return results
}

func stripFunctionCallMarkup(text string) string {
	clean := functionCallsTagRegex.ReplaceAllString(text, "")
	clean = toolCallTagRegex.ReplaceAllString(clean, "")
	return strings.TrimSpace(clean)
}
