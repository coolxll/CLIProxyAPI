package chat_completions

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	"github.com/tidwall/gjson"
)

// ConvertOpenAIRequestToLingma translates an OpenAI chat completions request
// into the Lingma agent_chat_generation format, wrapped and Lingma encoded.
func ConvertOpenAIRequestToLingma(modelName string, inputRawJSON []byte, stream bool) []byte {
	res := gjson.ParseBytes(inputRawJSON)

	// 1. Extract parameters
	temperature := 0.1
	if t := res.Get("temperature"); t.Exists() {
		temperature = t.Float()
	}

	// 2. Map messages
	var lingmaMessages []any
	res.Get("messages").ForEach(func(key, value gjson.Result) bool {
		msg := map[string]any{
			"role":    value.Get("role").String(),
			"content": value.Get("content").String(),
		}
		lingmaMessages = append(lingmaMessages, msg)
		return true
	})

	requestID := uuid.New().String()

	// 3. Build inner Lingma agent_chat_generation body
	// Based on BuildLingmaBody in lingma-tap
	innerBody := map[string]any{
		"request_id":       requestID,
		"request_set_id":   "",
		"chat_record_id":   requestID,
		"stream":           stream,
		"image_urls":       nil,
		"is_reply":         false,
		"is_retry":         false,
		"session_id":       uuid.New().String(),
		"code_language":    "",
		"source":           0,
		"version":          "3",
		"chat_prompt":      "",
		"aliyun_user_type": "enterprise_standard",
		"agent_id":         "agent_common",
		"task_id":          "question_refine",
		"model_config": map[string]any{
			"key": modelName,
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
		},
		"parameters": map[string]any{
			"temperature": temperature,
		},
	}

	// Add tools if present
	if tools := res.Get("tools"); tools.Exists() && tools.IsArray() {
		innerBody["tools"] = tools.Value()
	}

	innerJSON, _ := json.Marshal(innerBody)

	// 4. Wrap in the standard Lingma wrapper
	// wrapper: {"payload":"<inner_json_as_string>","encodeVersion":"1"}
	wrapper := map[string]string{
		"payload":       string(innerJSON),
		"encodeVersion": "1",
	}
	wrapperJSON, _ := json.Marshal(wrapper)

	// 5. Lingma Encode the entire wrapper
	encoded := lingma.Encode(wrapperJSON)

	return []byte(encoded)
}
