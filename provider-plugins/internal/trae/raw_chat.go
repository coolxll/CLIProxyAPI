package trae

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/google/uuid"
	traeenc "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/trae"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func buildTraeRawChatRequest(protocol, upstreamModel string, openaiReq []byte, metadata map[string]any) (*traeRequestBuildResult, error) {
	openaiReq = sanitizeTraeRawChatOpenAIRequest(openaiReq)
	modelConfig := resolveRawChatModelConfig(upstreamModel, protocol)
	if modelName := metadataString(metadata, traeModelNameMeta); modelName != "" {
		modelConfig.ModelName = modelName
	}
	if configName := metadataString(metadata, traeConfigMeta); configName != "" {
		modelConfig.ConfigName = configName
	}

	innerPayload := buildTraeRawChatInnerPayload(openaiReq, protocol)
	innerBytes, err := json.Marshal(innerPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal trae raw chat payload: %w", err)
	}
	encrypted, err := traeenc.EncryptMessage(innerBytes)
	if err != nil {
		return nil, fmt.Errorf("encrypt trae raw chat payload: %w", err)
	}

	sessionID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	convID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	var targetURL string
	var requestBody []byte
	extraHeaders := make(http.Header)
	if protocol == traeProtocolV1 {
		targetURL = "https://trae-api-cn.mchost.guru/api/ide/v1/llm_raw_chat"
		v1Envelope := map[string]any{
			"model_name": modelConfig.ModelName,
			"message":    encrypted.Message,
		}
		// V1 raw chat accepts tools as plaintext in the outer envelope.
		if rawTools := gjson.GetBytes(openaiReq, "tools"); rawTools.Exists() && rawTools.IsArray() && len(rawTools.Array()) > 0 {
			v1Envelope["tools"] = json.RawMessage(rawTools.Raw)
		}
		requestBody, err = json.Marshal(v1Envelope)
	} else {
		targetURL = "https://trae-api-cn.mchost.guru/api/ide/v2/llm_raw_chat"
		extraHeaders.Set("X-App-Function", "utils")
		extraHeaders.Set("X-Ide-Function", "utils")
		extraHeaders.Set("x-ide-version-code", "20260401")
		// V2 raw chat does not support tools; omit them from the outer envelope.
		requestBody, err = json.Marshal(map[string]any{
			"model_name":      modelConfig.ModelName,
			"config_name":     modelConfig.ConfigName,
			"config_source":   1,
			"messages":        []any{},
			"session_id":      sessionID,
			"conversation_id": convID,
			"message":         encrypted.Message,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("marshal trae %s raw chat envelope: %w", protocol, err)
	}

	return &traeRequestBuildResult{
		TargetURL:      targetURL,
		RequestBody:    requestBody,
		LogBody:        innerBytes,
		RequestPin:     encrypted.RequestPin,
		RequestAt:      encrypted.RequestAt,
		SessionID:      sessionID,
		ConversationID: convID,
		Protocol:       protocol,
		ExtraHeaders:   extraHeaders,
	}, nil
}

func sanitizeTraeRawChatOpenAIRequest(openaiReq []byte) []byte {
	if len(openaiReq) == 0 || !gjson.ValidBytes(openaiReq) {
		return openaiReq
	}
	sanitized := openaiReq
	for _, path := range []string{"betas", "thinking", "context_management", "output_config"} {
		next, err := sjson.DeleteBytes(sanitized, path)
		if err != nil {
			return sanitized
		}
		sanitized = next
	}
	return sanitized
}

func buildTraeRawChatInnerPayload(openaiReq []byte, protocol string) any {
	messages := buildTraeRawChatMessages(openaiReq, protocol)
	// V1 and V2 raw chat do not support tools inside the encrypted payload.
	if protocol == traeProtocolV1 || protocol == traeProtocolV2 {
		return messages
	}
	if rawTools := gjson.GetBytes(openaiReq, "tools"); rawTools.Exists() && rawTools.IsArray() && len(rawTools.Array()) > 0 {
		return map[string]any{
			"messages": messages,
			"tools":    json.RawMessage(rawTools.Raw),
		}
	}
	return messages
}

func buildTraeRawChatMessages(openaiReq []byte, protocol string) []map[string]any {
	openAIMessages := gjson.GetBytes(openaiReq, "messages").Array()
	messages := make([]map[string]any, 0, len(openAIMessages)+1)
	skipInvalidToolResultIDs := make(map[string]bool)
	if protocol == traeProtocolV1 {
		if toolInstructions := buildTraeToolShimInstructions(openaiReq); toolInstructions != "" {
			messages = append(messages, map[string]any{
				"role": "system",
				"content": []map[string]string{
					{
						"type": "text",
						"text": toolInstructions,
					},
				},
			})
		}
	}
	for i, msg := range openAIMessages {
		role := firstNonEmpty(msg.Get("role").String(), "user")
		text := openAIMessageText(msg)
		if protocol == traeProtocolV1 && role == "assistant" {
			if ids, ok := invalidEmptyClaudeToolHistoryIDs(openAIMessages, i); ok {
				for _, id := range ids {
					skipInvalidToolResultIDs[id] = true
				}
				continue
			}
			messages = append(messages, map[string]any{
				"role": "assistant",
				"content": []map[string]string{
					{
						"type": "text",
						"text": text,
					},
				},
			})
			continue
		}
		if role == "tool" {
			if protocol == traeProtocolV1 {
				if skipInvalidToolResultIDs[msg.Get("tool_call_id").String()] {
					continue
				}
				messages = append(messages, traeRawChatToolResultMessage(msg, text))
				continue
			}
			messages = append(messages, traeRawChatToolResultMessage(msg, text))
			continue
		}
		messages = append(messages, map[string]any{
			"role": role,
			"content": []map[string]string{
				{
					"type": "text",
					"text": text,
				},
			},
		})
	}
	return messages
}

func traeRawChatToolResultMessage(msg gjson.Result, text string) map[string]any {
	toolCallID := msg.Get("tool_call_id").String()
	toolName := msg.Get("name").String()
	if toolName == "" {
		if state, errDecode := decodeTraeToolID(toolCallID); errDecode == nil {
			toolName = state.Name
		}
	}
	escapedText := html.EscapeString(text)
	var resultText string
	if toolName != "" {
		resultText = fmt.Sprintf("<tool_result>\n<tool_call_id>%s</tool_call_id>\n<name>%s</name>\n<result>%s</result>\n</tool_result>", toolCallID, toolName, escapedText)
	} else if toolCallID != "" {
		resultText = fmt.Sprintf("<tool_result>\n<tool_call_id>%s</tool_call_id>\n<result>%s</result>\n</tool_result>", toolCallID, escapedText)
	} else {
		resultText = text
	}
	return map[string]any{
		"role": "user",
		"content": []map[string]string{
			{
				"type": "text",
				"text": resultText,
			},
		},
	}
}

func invalidEmptyClaudeToolHistoryIDs(messages []gjson.Result, idx int) ([]string, bool) {
	if idx < 0 || idx >= len(messages) {
		return nil, false
	}
	toolCalls := messages[idx].Get("tool_calls")
	if !toolCalls.Exists() || !toolCalls.IsArray() {
		return nil, false
	}
	calls := toolCalls.Array()
	if len(calls) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(calls))
	for _, call := range calls {
		if !traeToolCallArgumentsEmpty(call) {
			return nil, false
		}
		id := strings.TrimSpace(call.Get("id").String())
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, false
	}
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	for i := idx + 1; i < len(messages) && i <= idx+2; i++ {
		if messageHasClaudeInputValidationError(messages[i], idSet) {
			return ids, true
		}
	}
	return nil, false
}

func traeToolCallArgumentsEmpty(call gjson.Result) bool {
	args := call.Get("function.arguments")
	if !args.Exists() {
		args = call.Get("arguments")
	}
	if !args.Exists() {
		return true
	}
	raw := strings.TrimSpace(args.Raw)
	if args.Type == gjson.String {
		raw = strings.TrimSpace(args.String())
	}
	if raw == "" || raw == "null" {
		return true
	}
	if gjson.Valid(raw) {
		parsed := gjson.Parse(raw)
		return parsed.IsObject() && len(parsed.Map()) == 0
	}
	return false
}

func messageHasClaudeInputValidationError(message gjson.Result, ids map[string]bool) bool {
	hasInputValidationError := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "inputvalidationerror") &&
			strings.Contains(s, "required parameter") &&
			strings.Contains(s, "missing")
	}
	if message.Get("role").String() == "tool" {
		return ids[message.Get("tool_call_id").String()] && hasInputValidationError(openAIMessageText(message))
	}
	content := message.Get("content")
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		if part.Get("type").String() != "tool_result" {
			continue
		}
		if !ids[part.Get("tool_use_id").String()] {
			continue
		}
		text := firstNonEmpty(
			part.Get("content").String(),
			part.Get("text").String(),
		)
		if hasInputValidationError(text) {
			return true
		}
	}
	return false
}
