package responses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/helpers"
	"github.com/tidwall/gjson"
)

// ConvertLingmaResponseToOpenAI normalizes a single chunk of a Lingma streaming response to OpenAI format.
func ConvertLingmaResponseToOpenAI(_ context.Context, modelName string, _, _, rawJSON []byte, param *any) [][]byte {
	state := lingmaStreamStateFromParam(param, modelName)
	if helpers.IsLingmaDone(rawJSON) {
		if state.Finished {
			return nil
		}
		state.Finished = true
		return [][]byte{state.openAIStreamChunk(true, "")}
	}

	data, ok := lingmaSSEData(rawJSON)
	if !ok {
		return nil
	}
	res := gjson.ParseBytes(data)

	// 1. Try to parse as the double-JSON envelope: {"body":"..."}
	if body := res.Get("body"); body.Exists() && body.Type == gjson.String {
		inner := body.String()
		if inner == "[DONE]" {
			if state.Finished {
				return nil
			}
			state.Finished = true
			return [][]byte{state.openAIStreamChunk(true, "")}
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

	if state.HasError {
		state.Finished = true // Mark stream as finished on error
		errType := state.ErrorType
		if errType == "" {
			errType = "server_error"
		}
		errMsg := state.ErrorMsg
		if errMsg == "" {
			errMsg = "unknown error from lingma"
		}
		errChunk := map[string]any{
			"error": map[string]any{
				"message": errMsg,
				"type":    errType,
			},
		}
		if encoded, err := json.Marshal(errChunk); err == nil {
			return [][]byte{encoded}
		}
	}

	// 2. Try to parse as finish event/usage event: {"usage":...}
	// We might want to convert this to an OpenAI finish chunk if it's not already.
	if usage := res.Get("usage"); usage.Exists() && !res.Get("choices").Exists() {
		normalizedUsage := normalizeLingmaUsage(usage)
		if normalizedUsage != nil {
			out := map[string]any{
				"id":      state.ID,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   state.Model,
				"choices": []map[string]any{},
			}
			var usageVal any
			if err := json.Unmarshal(normalizedUsage, &usageVal); err == nil {
				out["usage"] = usageVal
			}
			if encoded, err := json.Marshal(out); err == nil {
				return [][]byte{encoded}
			}
		}
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
	ToolCalls    []json.RawMessage
	HasToolCalls bool
	HasError     bool
	ErrorMsg     string
	ErrorType    string
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

	if agg.HasError {
		errType := agg.ErrorType
		if errType == "" {
			errType = "server_error"
		}
		errMsg := agg.ErrorMsg
		if errMsg == "" {
			errMsg = "unknown error from lingma"
		}
		out := map[string]any{
			"error": map[string]any{
				"message": errMsg,
				"type":    errType,
			},
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return raw
		}
		return encoded
	}

	message := map[string]any{
		"role":    "assistant",
		"content": agg.Content.String(),
	}
	if agg.HasToolCalls && len(agg.ToolCalls) > 0 {
		message["tool_calls"] = agg.ToolCalls
		if agg.FinishReason == "" || agg.FinishReason == "stop" {
			agg.FinishReason = "tool_calls"
		}
	}
	if agg.FinishReason == "" {
		agg.FinishReason = "stop"
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
	if errResult := res.Get("error"); errResult.Exists() {
		agg.HasError = true
		agg.ErrorMsg = errResult.Get("message").String()
		if agg.ErrorMsg == "" {
			agg.ErrorMsg = errResult.Get("msg").String()
		}
		agg.ErrorType = errResult.Get("type").String()
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
		// Collect tool_calls from delta (streaming) or message (non-streaming)
		if tcs := choice.Get("delta.tool_calls"); tcs.Exists() && tcs.IsArray() {
			agg.HasToolCalls = true
			tcs.ForEach(func(_, tc gjson.Result) bool {
				agg.ToolCalls = append(agg.ToolCalls, json.RawMessage(tc.Raw))
				return true
			})
		}
		if tcs := choice.Get("message.tool_calls"); tcs.Exists() && tcs.IsArray() {
			agg.HasToolCalls = true
			tcs.ForEach(func(_, tc gjson.Result) bool {
				agg.ToolCalls = append(agg.ToolCalls, json.RawMessage(tc.Raw))
				return true
			})
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

	// Map cached tokens from various possible locations
	if v := usage.Get("prompt_tokens_details.cached_tokens"); v.Exists() && v.Int() > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": v.Int()}
	} else if v := usage.Get("cached_tokens"); v.Exists() && v.Int() > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": v.Int()}
	} else if v := usage.Get("cache_read_input_tokens"); v.Exists() && v.Int() > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": v.Int()}
	}

	// Map reasoning tokens
	if v := usage.Get("completion_tokens_details.reasoning_tokens"); v.Exists() && v.Int() > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": v.Int()}
	} else if v := usage.Get("reasoning_tokens"); v.Exists() && v.Int() > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": v.Int()}
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
	HasToolCalls bool
	HasError     bool
	ErrorMsg     string
	ErrorType    string
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
	if errResult := res.Get("error"); errResult.Exists() {
		s.HasError = true
		s.ErrorMsg = errResult.Get("message").String()
		if s.ErrorMsg == "" {
			s.ErrorMsg = errResult.Get("msg").String()
		}
		s.ErrorType = errResult.Get("type").String()
		s.Finished = true
	}
	res.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		if fr := strings.TrimSpace(choice.Get("finish_reason").String()); fr != "" {
			s.FinishReason = fr
			s.Finished = true
		}
		if choice.Get("delta.tool_calls").Exists() || choice.Get("message.tool_calls").Exists() {
			s.HasToolCalls = true
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
			// Only default to tool_calls if we have tool calls and no explicit finish reason
			if s != nil && s.HasToolCalls && s.FinishReason == "" {
				reason = "tool_calls"
			} else {
				reason = "stop"
			}
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
