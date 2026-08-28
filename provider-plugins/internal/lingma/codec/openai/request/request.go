package chat_completions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/helpers"
)

const lingmaMaxTokensHardLimit = 16384

// ConvertOpenAIRequestToLingma translates an OpenAI chat completions request
// into the Lingma agent_chat_generation format, wrapped and Lingma encoded.
func ConvertOpenAIRequestToLingma(modelName string, inputRawJSON []byte, stream bool) []byte {
	res := gjson.ParseBytes(inputRawJSON)

	// 1. Extract parameters
	temperature := 0.1
	if t := res.Get("temperature"); t.Exists() {
		temperature = t.Float()
	}

	parameters := map[string]any{
		"temperature": temperature,
	}
	if maxTokens := res.Get("max_tokens"); maxTokens.Exists() {
		value := maxTokens.Int()
		if value > lingmaMaxTokensHardLimit {
			value = lingmaMaxTokensHardLimit
		}
		parameters["max_tokens"] = value
	}
	if topP := res.Get("top_p"); topP.Exists() {
		parameters["top_p"] = topP.Float()
	}
	if stop := res.Get("stop"); stop.Exists() {
		parameters["stop"] = stop.Value()
	}

	// 2. Map messages
	var lingmaMessages []any
	res.Get("messages").ForEach(func(key, value gjson.Result) bool {
		role := value.Get("role").String()
		msg := map[string]any{
			"role": role,
		}

		// Handle content: could be string or array of content parts
		contentVal := value.Get("content")
		if contentVal.Exists() {
			if contentVal.IsArray() {
				hasNonText := false
				contentVal.ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() != "text" {
						hasNonText = true
						return false
					}
					return true
				})
				if hasNonText {
					// Preserve the full content array for multimodal (images, audio, etc.)
					msg["content"] = contentVal.Value()
				} else {
					// Text-only: join into a single string
					var textParts []string
					contentVal.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" {
							textParts = append(textParts, part.Get("text").String())
						}
						return true
					})
					msg["content"] = strings.Join(textParts, "\n")
				}
			} else {
				msg["content"] = contentVal.String()
			}
		}

		// Merge reasoning_content into content to preserve thinking context across providers.
		if reasoning := value.Get("reasoning_content"); reasoning.Exists() && reasoning.String() != "" {
			reasoningText := reasoning.String()
			thoughtBlock := "<thought>" + reasoningText + "</thought>"
			if existing, ok := msg["content"]; ok {
				switch v := existing.(type) {
				case string:
					msg["content"] = thoughtBlock + "\n" + v
				default:
					// For array content (multimodal), prepend thought block as a text element
					if arr, ok := existing.([]any); ok {
						thoughtElement := map[string]any{
							"type": "text",
							"text": thoughtBlock,
						}
						msg["content"] = append([]any{thoughtElement}, arr...)
					} else {
						// Log warning for unexpected content type
						log.Warnf("unexpected content type %T when merging reasoning_content, discarding reasoning", existing)
					}
				}
			} else {
				msg["content"] = thoughtBlock
			}
		}

		// Pass through tool_calls for assistant messages
		if toolCalls := value.Get("tool_calls"); toolCalls.Exists() {
			msg["tool_calls"] = toolCalls.Value()
		}

		// Pass through tool_call_id for tool messages
		if toolCallID := value.Get("tool_call_id"); toolCallID.Exists() {
			msg["tool_call_id"] = toolCallID.String()
		}

		lingmaMessages = append(lingmaMessages, msg)
		return true
	})

	requestID := uuid.New().String()

	// Derive deterministic session_id from conversation content hash
	sessionID := generateSessionID(inputRawJSON)

	// 3. Build inner Lingma agent_chat_generation body
	isReasoning := !helpers.IsAgentCommonModel(modelName)
	if re := res.Get("reasoning_effort"); re.Exists() && re.String() == "none" {
		isReasoning = false
	}
	if ir := res.Get("is_reasoning"); ir.Exists() {
		isReasoning = ir.Bool()
	}

	// When thinking is disabled, use agent_common to prevent upstream from
	// returning reasoning content.
	agentID := helpers.AgentID(modelName)
	modelConfigSource := helpers.ModelConfigSource(modelName)
	if !isReasoning {
		agentID = helpers.AgentCommon
		modelConfigSource = ""
	}

	innerBody := map[string]any{
		"request_id":       requestID,
		"request_set_id":   "",
		"chat_record_id":   requestID,
		"stream":           stream,
		"image_urls":       nil,
		"is_reply":         false,
		"is_retry":         false,
		"session_id":       sessionID,
		"code_language":    "",
		"source":           0,
		"version":          "3",
		"chat_prompt":      "",
		"aliyun_user_type": "enterprise_standard",
		"agent_id":         agentID,
		"task_id":          "question_refine",
		"model_config": map[string]any{
			"key":                   modelName,
			"display_name":          "",
			"model":                 "",
			"format":                "",
			"is_vl":                 false,
			"is_reasoning":          isReasoning,
			"api_key":               "",
			"url":                   "",
			"source":                modelConfigSource,
			"max_input_tokens":      0,
			"enable":                false,
			"price_factor":          0,
			"original_price_factor": 0,
			"is_default":            false,
			"is_new":                false,
			"exclude_tags":          nil,
			"tags":                  nil,
			"icon":                  nil,
			"strategies":            nil,
		},
		"messages": lingmaMessages,
		"business": map[string]any{
			"product":  "ide",
			"version":  "0.11.0",
			"type":     "chat",
			"id":       uuid.New().String(),
			"begin_at": 0,
			"stage":    "start",
			"name":     "api-bridge",
			"relation": map[string]any{},
		},
		"parameters": parameters,
	}

	// Add tools if present
	if tools := res.Get("tools"); tools.Exists() && tools.IsArray() {
		innerBody["tools"] = tools.Value()
	}

	// Add tool_choice if present
	if toolChoice := res.Get("tool_choice"); toolChoice.Exists() {
		innerBody["tool_choice"] = toolChoice.Value()
	}

	innerJSON, _ := json.Marshal(innerBody)

	// 4. Return raw JSON bytes (encoding is handled by executor after thinking application).
	return innerJSON
}

// generateSessionID produces a deterministic session ID from the request content.
func generateSessionID(rawJSON []byte) string {
	hash := sha256.Sum256(rawJSON)
	return hex.EncodeToString(hash[:16])
}
