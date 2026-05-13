package responses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// ConvertLingmaResponseToOpenAI normalizes a single chunk of a Lingma streaming response to OpenAI format.
func ConvertLingmaResponseToOpenAI(_ context.Context, modelName string, _, _, rawJSON []byte, param *any) [][]byte {
	data, ok := lingmaSSEData(rawJSON)
	if !ok {
		return nil
	}
	state := lingmaStreamStateFromParam(param, modelName)
	res := gjson.ParseBytes(data)

	// 1. Try to parse as the double-JSON envelope: {"body":"..."}
	if body := res.Get("body"); body.Exists() && body.Type == gjson.String {
		inner := body.String()
		if inner == "[DONE]" {
			return [][]byte{}
		}
		innerJSON := []byte(inner)
		if gjson.GetBytes(innerJSON, "choices").Exists() {
			state.capture(innerJSON)
			return [][]byte{innerJSON}
		}
		if gjson.GetBytes(innerJSON, "usage").Exists() {
			return [][]byte{innerJSON}
		}
		if gjson.GetBytes(innerJSON, "id").Exists() || gjson.GetBytes(innerJSON, "model").Exists() {
			state.capture(innerJSON)
			return [][]byte{state.openAIStreamChunk(false, "")}
		}
		return nil
	}

	state.capture(data)

	// 2. Try to parse as finish event/usage event: {"usage":...}
	// We might want to convert this to an OpenAI finish chunk if it's not already.
	if usage := res.Get("usage"); usage.Exists() && !res.Get("choices").Exists() {
		// Normalizing usage info to OpenAI format if needed
		// For now, if it has usage but no choices, it's a metadata chunk.
		// OpenAI standard expects choices in every chunk except maybe the last one with usage.
		return [][]byte{data}
	}

	// 3. Fallback: if it already looks like OpenAI (has choices), pass through.
	if res.Get("choices").Exists() {
		return [][]byte{data}
	}

	if res.Get("totalDuration").Exists() || res.Get("serverDuration").Exists() || res.Get("firstTokenDuration").Exists() {
		if state.Finished {
			return nil
		}
		return [][]byte{state.openAIStreamChunk(true, "")}
	}

	// If it's [DONE], return empty
	if string(data) == "[DONE]" {
		return [][]byte{}
	}

	return nil
}

// ConvertLingmaResponseToOpenAINonStream translates a non-streaming Lingma response.
func ConvertLingmaResponseToOpenAINonStream(_ context.Context, modelName string, _, _, rawJSON []byte, _ *any) []byte {
	if bytes.Contains(rawJSON, []byte("data:")) || bytes.Contains(rawJSON, []byte("event:")) {
		return aggregateLingmaSSEToOpenAI(modelName, rawJSON)
	}

	res := gjson.ParseBytes(rawJSON)
	if body := res.Get("body"); body.Exists() && body.Type == gjson.String {
		return []byte(body.String())
	}

	return rawJSON
}

func lingmaSSEData(raw []byte) ([]byte, bool) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return nil, false
	}
	if bytes.HasPrefix(data, []byte("event:")) {
		return nil, false
	}
	if bytes.HasPrefix(data, []byte("data:")) {
		data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return nil, false
	}
	return data, true
}

type lingmaNonStreamAggregate struct {
	ID           string
	Model        string
	Content      strings.Builder
	FinishReason string
	Usage        json.RawMessage
}

func aggregateLingmaSSEToOpenAI(modelName string, raw []byte) []byte {
	agg := &lingmaNonStreamAggregate{Model: modelName}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(nil, 5*1024*1024)
	for scanner.Scan() {
		data, ok := lingmaSSEData(scanner.Bytes())
		if !ok {
			continue
		}
		collectLingmaOpenAIFragment(agg, data)
	}
	if agg.ID == "" {
		agg.ID = "chatcmpl-" + uuid.New().String()
	}
	if strings.TrimSpace(agg.Model) == "" {
		agg.Model = modelName
	}
	if agg.FinishReason == "" {
		agg.FinishReason = "stop"
	}

	message := map[string]any{
		"role":    "assistant",
		"content": agg.Content.String(),
	}
	out := map[string]any{
		"id":      agg.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   agg.Model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": agg.FinishReason,
			},
		},
	}
	if len(agg.Usage) > 0 {
		var usage any
		if err := json.Unmarshal(agg.Usage, &usage); err == nil {
			out["usage"] = usage
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return encoded
}

func collectLingmaOpenAIFragment(agg *lingmaNonStreamAggregate, data []byte) {
	if agg == nil || len(data) == 0 {
		return
	}
	res := gjson.ParseBytes(data)
	if body := res.Get("body"); body.Exists() && body.Type == gjson.String {
		inner := body.String()
		if inner == "" || inner == "[DONE]" {
			return
		}
		collectLingmaOpenAIFragment(agg, []byte(inner))
		return
	}
	if id := strings.TrimSpace(res.Get("id").String()); id != "" && agg.ID == "" {
		agg.ID = id
	}
	if model := strings.TrimSpace(res.Get("model").String()); model != "" {
		agg.Model = model
	}
	if usage := res.Get("usage"); usage.Exists() {
		agg.Usage = normalizeLingmaUsage(usage)
	}
	res.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		if content := choice.Get("delta.content").String(); content != "" {
			agg.Content.WriteString(content)
		}
		if content := choice.Get("message.content").String(); content != "" {
			agg.Content.WriteString(content)
		}
		if finishReason := strings.TrimSpace(choice.Get("finish_reason").String()); finishReason != "" {
			agg.FinishReason = finishReason
		}
		return true
	})
}

func normalizeLingmaUsage(usage gjson.Result) json.RawMessage {
	if !usage.Exists() {
		return nil
	}
	promptTokens := usage.Get("prompt_tokens").Int()
	if promptTokens == 0 {
		promptTokens = usage.Get("input_tokens").Int()
	}
	completionTokens := usage.Get("completion_tokens").Int()
	if completionTokens == 0 {
		completionTokens = usage.Get("output_tokens").Int()
	}
	totalTokens := usage.Get("total_tokens").Int()
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}
	out := map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      totalTokens,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(usage.Raw)
	}
	return encoded
}

type lingmaStreamState struct {
	ID           string
	Model        string
	FinishReason string
	Finished     bool
}

func lingmaStreamStateFromParam(param *any, modelName string) *lingmaStreamState {
	if param != nil {
		if state, ok := (*param).(*lingmaStreamState); ok && state != nil {
			if strings.TrimSpace(state.Model) == "" {
				state.Model = modelName
			}
			return state
		}
	}
	state := &lingmaStreamState{Model: modelName}
	if param != nil {
		*param = state
	}
	return state
}

func (s *lingmaStreamState) capture(raw []byte) {
	if s == nil {
		return
	}
	res := gjson.ParseBytes(raw)
	if id := strings.TrimSpace(res.Get("id").String()); id != "" {
		s.ID = id
	}
	if model := strings.TrimSpace(res.Get("model").String()); model != "" {
		s.Model = model
	}
	res.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		if fr := strings.TrimSpace(choice.Get("finish_reason").String()); fr != "" {
			s.FinishReason = fr
			s.Finished = true
		}
		return true
	})
}

func (s *lingmaStreamState) openAIStreamChunk(finished bool, finishReason string) []byte {
	id := ""
	model := ""
	if s != nil {
		id = s.ID
		model = s.Model
	}
	if id == "" {
		id = "chatcmpl-" + uuid.New().String()
	}
	if model == "" {
		model = "auto"
	}
	choice := map[string]any{
		"index": 0,
		"delta": map[string]any{
			"role":    "assistant",
			"content": "",
		},
		"finish_reason": nil,
	}
	if finished {
		choice["delta"] = map[string]any{}
		reason := finishReason
		if reason == "" && s != nil {
			reason = s.FinishReason
		}
		if reason == "" {
			reason = "stop"
		}
		choice["finish_reason"] = reason
	}
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{choice},
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return encoded
}
