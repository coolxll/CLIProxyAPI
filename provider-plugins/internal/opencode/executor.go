package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type executorRPCRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type executorStreamResponse struct {
	Headers http.Header `json:"headers,omitempty"`
}

func (p *Plugin) execute(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode opencode execute request: %w", err)
	}

	creds, errCreds := credentialsFromStorage(req.StorageJSON)
	if errCreds != nil {
		return nil, errCreds
	}

	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	if creds.isCloudZen() {
		return p.executeCloudZen(host, creds, req)
	}
	return p.executeDaemon(host, creds, req)
}

func (p *Plugin) executeStream(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode opencode stream request: %w", err)
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return nil, fmt.Errorf("opencode output stream ID is required")
	}

	creds, errCreds := credentialsFromStorage(req.StorageJSON)
	if errCreds != nil {
		return nil, errCreds
	}

	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	if creds.isCloudZen() {
		return p.executeCloudZenStream(host, creds, req)
	}
	return p.executeDaemonStream(host, creds, req)
}

func (p *Plugin) executeDaemon(host hostRPC, creds credentials, req executorRPCRequest) ([]byte, error) {
	// Force stream=true for canonical execution aggregation
	streamReq := req
	streamReq.Stream = true
	if len(streamReq.OriginalRequest) > 0 {
		streamVal := gjson.GetBytes(streamReq.OriginalRequest, "stream")
		if !streamVal.Exists() || streamVal.Type == gjson.False {
			if modified, errSet := sjson.SetBytes(streamReq.OriginalRequest, "stream", true); errSet == nil {
				streamReq.OriginalRequest = modified
			}
		}
	}

	modelRef, promptReq, openaiReq, errPrepare := p.prepareRequestWithCreds(host, creds, streamReq)
	if errPrepare != nil {
		return nil, errPrepare
	}

	var aggregate bytes.Buffer
	openaiFormat := sdktranslator.FormatOpenAI
	processor := newOpencodeStreamProcessor(
		context.Background(),
		host,
		req,
		"", // will be set once session is created
		openaiFormat,
		func(payload []byte, _ *pluginapi.UsageDetail) error {
			if len(payload) > 0 {
				aggregate.Write(payload)
				aggregate.WriteByte('\n')
			}
			return nil
		},
	)

	errRun := p.runExecution(host, creds, modelRef, promptReq, processor, nil)
	if errRun != nil {
		return nil, errRun
	}

	from := sdktranslator.Format(normalizeFormat(req.ExecutorRequest))
	return p.aggregateToResponse(aggregate.Bytes(), req, http.Header{}, from, openaiFormat, openaiReq)
}

func (p *Plugin) executeDaemonStream(host hostRPC, creds credentials, req executorRPCRequest) ([]byte, error) {
	modelRef, promptReq, _, errPrepare := p.prepareRequestWithCreds(host, creds, req)
	if errPrepare != nil {
		return nil, errPrepare
	}

	if !p.beginStream(req.StreamID, host, "") {
		return nil, fmt.Errorf("OpenCode plugin is shutting down")
	}

	go func() {
		defer p.endStream(req.StreamID)
		targetFormat := sdktranslator.Format(normalizeFormat(req.ExecutorRequest))
		processor := newOpencodeStreamProcessor(
			context.Background(),
			host,
			req,
			"",
			targetFormat,
			func(payload []byte, usage *pluginapi.UsageDetail) error {
				return host.emit(req.StreamID, payload, usage)
			},
		)

		streamErr := p.runExecution(host, creds, modelRef, promptReq, processor, func(eventStreamID string) {
			p.updateUpstreamStreamID(req.StreamID, eventStreamID)
		})
		host.closeOutputStream(req.StreamID, streamErr)
	}()

	headers := http.Header{
		"Content-Type":  []string{"text/event-stream"},
		"Cache-Control": []string{"no-cache"},
	}
	return pluginruntime.OK(executorStreamResponse{Headers: headers})
}

func (p *Plugin) prepareRequest(host hostRPC, req executorRPCRequest) (credentials, OpenCodeModelRef, SessionPromptRequest, []byte, error) {
	creds, errCreds := credentialsFromStorage(req.StorageJSON)
	if errCreds != nil {
		return credentials{}, OpenCodeModelRef{}, SessionPromptRequest{}, nil, errCreds
	}
	modelRef, promptReq, openaiReq, errPrepare := p.prepareRequestWithCreds(host, creds, req)
	return creds, modelRef, promptReq, openaiReq, errPrepare
}

func (p *Plugin) prepareRequestWithCreds(host hostRPC, creds credentials, req executorRPCRequest) (OpenCodeModelRef, SessionPromptRequest, []byte, error) {
	availableModels, _ := fetchModels(host, creds)
	modelRef := resolveModelRef(req.Model, availableModels)
	if creds.isFreeOnly() && !isFreeModel(modelRef.ModelID) {
		return OpenCodeModelRef{}, SessionPromptRequest{}, nil, fmt.Errorf("model %q is not a free model; this credential is configured for free models only", modelRef.ModelID)
	}

	from := sdktranslator.Format(normalizeFormat(req.ExecutorRequest))
	openaiFormat := sdktranslator.FormatOpenAI
	openaiReq := sdktranslator.TranslateRequest(from, openaiFormat, modelRef.ModelID, req.Payload, true)

	promptReq := buildOpenCodePromptRequest(modelRef, openaiReq)
	return modelRef, promptReq, openaiReq, nil
}

func buildOpenCodePromptRequest(modelRef OpenCodeModelRef, openaiReq []byte) SessionPromptRequest {
	parsed := gjson.ParseBytes(openaiReq)
	messages := parsed.Get("messages").Array()

	var systemChunks []string
	var parts []OpenCodePart

	for _, m := range messages {
		role := strings.ToLower(m.Get("role").String())
		content := m.Get("content")

		if role == "system" {
			text := strings.TrimSpace(content.String())
			if text != "" {
				systemChunks = append(systemChunks, text)
			}
			continue
		}

		if role == "assistant" {
			toolCalls := m.Get("tool_calls").Array()
			if len(toolCalls) > 0 {
				var calls []map[string]any
				for i, tc := range toolCalls {
					callID := tc.Get("id").String()
					if callID == "" {
						callID = fmt.Sprintf("call_%d", i+1)
					}
					fnName := tc.Get("function.name").String()
					fnArgs := tc.Get("function.arguments").Raw
					if fnArgs == "" {
						fnArgs = "{}"
					}
					var parsedArgs any
					_ = json.Unmarshal([]byte(fnArgs), &parsedArgs)
					calls = append(calls, map[string]any{
						"id":        callID,
						"name":      fnName,
						"arguments": parsedArgs,
					})
				}
				rawJSON, _ := json.Marshal(calls)
				parts = append(parts, OpenCodePart{
					Type: "text",
					Text: fmt.Sprintf("ASSISTANT: <function_calls>%s</function_calls>", string(rawJSON)),
				})
			}
			if content.Exists() && content.String() != "" {
				parts = append(parts, OpenCodePart{
					Type: "text",
					Text: fmt.Sprintf("ASSISTANT: %s", content.String()),
				})
			}
			continue
		}

		if role == "tool" {
			text := strings.TrimSpace(content.String())
			toolCallID := m.Get("tool_call_id").String()
			toolName := m.Get("name").String()
			resultJSON, _ := json.Marshal(map[string]string{
				"tool_call_id": toolCallID,
				"name":         toolName,
				"content":      text,
			})
			parts = append(parts, OpenCodePart{
				Type: "text",
				Text: fmt.Sprintf("TOOL_RESULT: %s", string(resultJSON)),
			})
			continue
		}

		// User message or default
		if content.IsArray() {
			for _, part := range content.Array() {
				partType := part.Get("type").String()
				if partType == "text" {
					text := part.Get("text").String()
					parts = append(parts, OpenCodePart{
						Type: "text",
						Text: fmt.Sprintf("USER: %s", text),
					})
				} else if partType == "image_url" {
					urlVal := part.Get("image_url.url").String()
					if urlVal == "" {
						urlVal = part.Get("image_url").String()
					}
					parts = append(parts, OpenCodePart{
						Type:     "file",
						URL:      urlVal,
						Filename: "image",
					})
				}
			}
		} else {
			text := content.String()
			if text != "" {
				parts = append(parts, OpenCodePart{
					Type: "text",
					Text: fmt.Sprintf("USER: %s", text),
				})
			}
		}
	}

	promptReq := SessionPromptRequest{
		Model:  modelRef,
		System: strings.Join(systemChunks, "\n\n"),
		Parts:  parts,
	}

	if maxTokens := parsed.Get("max_tokens"); maxTokens.Exists() {
		val := int(maxTokens.Int())
		promptReq.MaxTokens = &val
	}
	if temp := parsed.Get("temperature"); temp.Exists() {
		val := temp.Float()
		promptReq.Temperature = &val
	}
	if topP := parsed.Get("top_p"); topP.Exists() {
		val := topP.Float()
		promptReq.TopP = &val
	}
	if stop := parsed.Get("stop"); stop.Exists() {
		if stop.IsArray() {
			for _, s := range stop.Array() {
				promptReq.Stop = append(promptReq.Stop, s.String())
			}
		} else if s := stop.String(); s != "" {
			promptReq.Stop = []string{s}
		}
	}

	return promptReq
}

func (p *Plugin) runExecution(
	host hostRPC,
	creds credentials,
	modelRef OpenCodeModelRef,
	promptReq SessionPromptRequest,
	processor *opencodeStreamProcessor,
	onUpstreamStream func(string),
) error {
	const maxAttempts = 2
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt*500) * time.Millisecond)
		}

		errAttempt := p.executeAttempt(host, creds, modelRef, promptReq, processor, onUpstreamStream)
		if errAttempt == nil {
			return nil
		}
		lastErr = errAttempt
		if !isTransientError(errAttempt) {
			return errAttempt
		}
	}

	return lastErr
}

func (p *Plugin) executeAttempt(
	host hostRPC,
	creds credentials,
	modelRef OpenCodeModelRef,
	promptReq SessionPromptRequest,
	processor *opencodeStreamProcessor,
	onUpstreamStream func(string),
) error {
	baseURL := creds.baseURL()
	authHeaders := buildAuthHeaders(creds)

	// Step 1: Open GET /event stream for SSE events
	eventHeaders := authHeaders.Clone()
	eventHeaders.Set("Accept", "text/event-stream")
	eventStreamResp, errStream := host.doStream(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     fmt.Sprintf("%s/event", baseURL),
		Headers: eventHeaders,
	})
	if errStream != nil {
		return fmt.Errorf("subscribe to OpenCode events: %w", errStream)
	}
	defer host.closeHTTPStream(eventStreamResp.StreamID)
	if onUpstreamStream != nil {
		onUpstreamStream(eventStreamResp.StreamID)
	}

	// Step 2: Create Session via POST /session
	createHeaders := authHeaders.Clone()
	createHeaders.Set("Content-Type", "application/json")
	createResp, errCreate := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     fmt.Sprintf("%s/session", baseURL),
		Headers: createHeaders,
		Body:    []byte(`{}`),
	})
	if errCreate != nil {
		return fmt.Errorf("create OpenCode session: %w", errCreate)
	}
	if createResp.StatusCode != http.StatusOK && createResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("create OpenCode session returned HTTP %d: %s", createResp.StatusCode, string(createResp.Body))
	}

	var sessionResp SessionCreateResponse
	if errUnmarshal := json.Unmarshal(createResp.Body, &sessionResp); errUnmarshal != nil || sessionResp.ID == "" {
		return fmt.Errorf("parse OpenCode session id from: %s", string(createResp.Body))
	}
	sessionID := sessionResp.ID
	processor.sessionID = sessionID

	if creds.isAutoCleanup() {
		defer func() {
			delHeaders := authHeaders.Clone()
			_, _ = host.do(pluginapi.HTTPRequest{
				Method:  http.MethodDelete,
				URL:     fmt.Sprintf("%s/session/%s", baseURL, sessionID),
				Headers: delHeaders,
			})
		}()
	}

	// Start stream processor
	if errStart := processor.start(); errStart != nil {
		return errStart
	}

	// Step 3: Send Prompt via POST /session/{id}/message
	promptBody, errMarshal := json.Marshal(promptReq)
	if errMarshal != nil {
		return fmt.Errorf("marshal prompt request: %w", errMarshal)
	}

	promptRespChan := make(chan error, 1)
	go func() {
		pHeaders := authHeaders.Clone()
		pHeaders.Set("Content-Type", "application/json")
		pResp, errPrompt := host.do(pluginapi.HTTPRequest{
			Method:  http.MethodPost,
			URL:     fmt.Sprintf("%s/session/%s/message", baseURL, sessionID),
			Headers: pHeaders,
			Body:    promptBody,
		})
		if errPrompt != nil {
			promptRespChan <- errPrompt
			return
		}
		if pResp.StatusCode != http.StatusOK && pResp.StatusCode != http.StatusCreated {
			promptRespChan <- fmt.Errorf("OpenCode prompt failed with HTTP %d: %s", pResp.StatusCode, string(pResp.Body))
			return
		}
		promptRespChan <- nil
	}()

	// Step 4: Consume SSE events from GET /event
	streamErr := p.consumeEventStream(host, eventStreamResp.StreamID, processor)
	promptErr := <-promptRespChan

	if streamErr != nil {
		return streamErr
	}
	return promptErr
}

func (p *Plugin) consumeEventStream(host hostRPC, streamID string, processor *opencodeStreamProcessor) error {
	var pending []byte
	var currentEventName string
	var currentDataLines []string

	dispatchCurrentEvent := func() (bool, error) {
		if len(currentDataLines) == 0 {
			currentEventName = ""
			return false, nil
		}
		joinedData := strings.Join(currentDataLines, "\n")
		currentDataLines = nil
		eventName := currentEventName
		currentEventName = ""

		if strings.TrimSpace(joinedData) == "[DONE]" {
			return true, processor.finish()
		}

		var event OpenCodeEvent
		if err := json.Unmarshal([]byte(joinedData), &event); err == nil {
			if event.Type == "" && eventName != "" {
				event.Type = eventName
			}
			if len(event.Properties) == 0 {
				event.Properties = json.RawMessage(joinedData)
			}
			done, errProc := processor.processEvent(event)
			if errProc != nil {
				return false, errProc
			}
			if done {
				return true, nil
			}
		}
		return false, nil
	}

	for {
		chunk, errRead := host.readHTTPStream(streamID)
		if errRead != nil {
			return errRead
		}
		pending = append(pending, chunk.Payload...)

		for {
			newline := bytes.IndexByte(pending, '\n')
			if newline < 0 {
				break
			}
			lineBytes := bytes.TrimSuffix(pending[:newline], []byte{'\r'})
			pending = pending[newline+1:]
			line := string(lineBytes)

			if len(strings.TrimSpace(line)) == 0 {
				// Empty line delimits an SSE event
				done, errDispatch := dispatchCurrentEvent()
				if errDispatch != nil {
					return errDispatch
				}
				if done {
					return nil
				}
				continue
			}

			if strings.HasPrefix(line, "event:") {
				currentEventName = strings.TrimSpace(line[6:])
			} else if strings.HasPrefix(line, "data:") {
				currentDataLines = append(currentDataLines, strings.TrimSpace(line[5:]))
			}
		}

		if chunk.Error != "" {
			return errors.New(chunk.Error)
		}
		if chunk.Done {
			_, errDispatch := dispatchCurrentEvent()
			if errDispatch != nil {
				return errDispatch
			}
			if !processor.isFinished {
				return processor.finish()
			}
			return nil
		}
	}
}

func (p *Plugin) aggregateToResponse(
	aggregate []byte,
	req executorRPCRequest,
	headers http.Header,
	from, openaiFormat sdktranslator.Format,
	openaiReq []byte,
) ([]byte, error) {
	var aggregatedContent strings.Builder
	var aggregatedReasoning strings.Builder
	var toolCalls []openaiToolCall
	var finalModel string
	var chatID string
	var finalUsage openaiUsage
	var hasUsage bool

	for _, line := range bytes.Split(aggregate, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		data := trimmed
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			data = bytes.TrimSpace(trimmed[5:])
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}

		var chunk openaiChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}
		if chunk.ID != "" {
			chatID = chunk.ID
		}
		if chunk.Model != "" {
			finalModel = chunk.Model
		}
		if chunk.Usage != nil {
			finalUsage = *chunk.Usage
			hasUsage = true
		}
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			if choice.Delta.Content != "" {
				aggregatedContent.WriteString(choice.Delta.Content)
			}
			if choice.Delta.ReasoningContent != "" {
				aggregatedReasoning.WriteString(choice.Delta.ReasoningContent)
			}
			if len(choice.Delta.ToolCalls) > 0 {
				toolCalls = append(toolCalls, choice.Delta.ToolCalls...)
			}
		}
	}

	if finalModel == "" {
		finalModel = req.Model
	}
	if chatID == "" {
		chatID = fmt.Sprintf("chatcmpl-%d", time.Now().Unix())
	}

	contentStr := aggregatedContent.String()
	parsedCalls, cleanContent := parseExternalToolCalls(contentStr)
	if len(parsedCalls) > 0 {
		toolCalls = append(toolCalls, parsedCalls...)
		contentStr = cleanContent
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	if !hasUsage {
		promptTokens := (len(openaiReq) + 3) / 4
		completionTokens := (len(contentStr) + 3) / 4
		reasoningTokens := (aggregatedReasoning.Len() + 3) / 4
		finalUsage = openaiUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens + reasoningTokens,
			TotalTokens:      promptTokens + completionTokens + reasoningTokens,
		}
		if reasoningTokens > 0 {
			finalUsage.CompletionTokensDetails = &openaiReasoningUsage{
				ReasoningTokens: reasoningTokens,
			}
		}
	}

	resp := openaiResponse{
		ID:      chatID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   finalModel,
		Choices: []openaiResponseChoice{
			{
				Index: 0,
				Message: openaiResponseMessage{
					Role:             "assistant",
					Content:          contentStr,
					ReasoningContent: aggregatedReasoning.String(),
					ToolCalls:        toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: finalUsage,
	}

	respRaw, errMarshal := json.Marshal(resp)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal OpenAI response: %w", errMarshal)
	}

	var translateParam any
	respPayload := respRaw
	if from != openaiFormat {
		respPayload = sdktranslator.TranslateNonStream(context.Background(), openaiFormat, from, req.Model, req.OriginalRequest, nil, respRaw, &translateParam)
	}

	return pluginruntime.OK(pluginapi.ExecutorResponse{
		Headers: headers,
		Payload: respPayload,
	})
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	transientKeywords := []string{
		"insufficient balance",
		"creditserror",
		"rate limit",
		"too many requests",
		"worker request limit",
		"overloaded",
		"temporarily unavailable",
		"502",
		"503",
		"bad gateway",
		"stream error",
	}
	for _, kw := range transientKeywords {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

func normalizeFormat(req pluginapi.ExecutorRequest) string {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != "" {
		return format
	}
	return "openai"
}

func (p *Plugin) executeCloudZen(host hostRPC, creds credentials, req executorRPCRequest) ([]byte, error) {
	from := sdktranslator.Format(normalizeFormat(req.ExecutorRequest))
	openaiFormat := sdktranslator.FormatOpenAI
	modelID := resolveCloudZenModel(req.Model)

	key := creds.effectiveAPIKey()
	if isFreeModel(modelID) {
		if key == "" || (creds.isAnonymous() && key == "public") {
			key = "public"
		}
	} else if key == "" || key == "public" {
		return nil, fmt.Errorf("model %q is a paid model on OpenCode Zen; please configure an API key or use a free model (e.g., deepseek-v4-flash-free, big-pickle, mimo-v2.5-free, nemotron-3.5-lightning-free, kimi-k2.5-free)", modelID)
	} else if creds.isFreeOnly() {
		return nil, fmt.Errorf("model %q is not a free model; this credential is configured for free models only", modelID)
	}

	openaiReq := sdktranslator.TranslateRequest(from, openaiFormat, modelID, req.Payload, true)
	preparedBody := prepareCloudZenPayload(openaiReq, modelID, false)

	ids := deriveRequestIDs(req.Headers, preparedBody)
	headers := buildCloudZenHeaders(creds, ids, key, false)

	endpoint := fmt.Sprintf("%s/v1/chat/completions", creds.baseURL())
	resp, errDo := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: headers,
		Body:    preparedBody,
	})
	if errDo != nil {
		return nil, fmt.Errorf("OpenCode Zen upstream call failed: %w", errDo)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenCode Zen upstream returned HTTP %d: %s", resp.StatusCode, string(resp.Body))
	}

	processedBody := postProcessCloudZenResponse(resp.Body, modelID)

	var translateParam any
	respPayload := processedBody
	if from != openaiFormat {
		respPayload = sdktranslator.TranslateNonStream(context.Background(), openaiFormat, from, req.Model, req.OriginalRequest, nil, processedBody, &translateParam)
	}

	return pluginruntime.OK(pluginapi.ExecutorResponse{
		Headers: resp.Headers,
		Payload: respPayload,
	})
}

func (p *Plugin) executeCloudZenStream(host hostRPC, creds credentials, req executorRPCRequest) ([]byte, error) {
	from := sdktranslator.Format(normalizeFormat(req.ExecutorRequest))
	openaiFormat := sdktranslator.FormatOpenAI
	modelID := resolveCloudZenModel(req.Model)

	key := creds.effectiveAPIKey()
	if isFreeModel(modelID) {
		if key == "" || (creds.isAnonymous() && key == "public") {
			key = "public"
		}
	} else if key == "" || key == "public" {
		return nil, fmt.Errorf("model %q is a paid model on OpenCode Zen; please configure an API key or use a free model (e.g., deepseek-v4-flash-free, big-pickle, mimo-v2.5-free, nemotron-3.5-lightning-free, kimi-k2.5-free)", modelID)
	} else if creds.isFreeOnly() {
		return nil, fmt.Errorf("model %q is not a free model; this credential is configured for free models only", modelID)
	}

	openaiReq := sdktranslator.TranslateRequest(from, openaiFormat, modelID, req.Payload, true)
	preparedBody := prepareCloudZenPayload(openaiReq, modelID, true)

	ids := deriveRequestIDs(req.Headers, preparedBody)
	headers := buildCloudZenHeaders(creds, ids, key, false)

	endpoint := fmt.Sprintf("%s/v1/chat/completions", creds.baseURL())
	streamResp, errStream := host.doStream(pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: headers,
		Body:    preparedBody,
	})
	if errStream != nil {
		return nil, fmt.Errorf("OpenCode Zen upstream stream failed: %w", errStream)
	}
	if streamResp.StatusCode != http.StatusOK {
		host.closeHTTPStream(streamResp.StreamID)
		return nil, fmt.Errorf("OpenCode Zen upstream stream returned HTTP %d", streamResp.StatusCode)
	}

	if !p.beginStream(req.StreamID, host, streamResp.StreamID) {
		host.closeHTTPStream(streamResp.StreamID)
		return nil, fmt.Errorf("OpenCode plugin is shutting down")
	}

	go func() {
		defer p.endStream(req.StreamID)
		defer host.closeHTTPStream(streamResp.StreamID)

		streamErr := p.pumpCloudZenStream(host, req.StreamID, streamResp.StreamID, req.Model, req.OriginalRequest, from)
		host.closeOutputStream(req.StreamID, streamErr)
	}()

	respHeaders := http.Header{
		"Content-Type":  []string{"text/event-stream"},
		"Cache-Control": []string{"no-cache"},
	}
	return pluginruntime.OK(executorStreamResponse{Headers: respHeaders})
}

func prepareCloudZenPayload(openaiReq []byte, modelID string, stream bool) []byte {
	modified := openaiReq
	var errSet error
	modified, errSet = sjson.SetBytes(modified, "model", modelID)
	if errSet != nil {
		modified = openaiReq
	}
	modified, _ = sjson.SetBytes(modified, "stream", stream)

	if isFreeModel(modelID) {
		toolsVal := gjson.GetBytes(modified, "tools")
		if toolsVal.Exists() && toolsVal.IsArray() && len(toolsVal.Array()) > 0 {
			toolsJSON := toolsVal.Raw
			modified, _ = sjson.DeleteBytes(modified, "tools")
			modified, _ = sjson.DeleteBytes(modified, "tool_choice")

			toolInstruction := fmt.Sprintf("\n\n# Tools\nYou have access to the following tools:\n<tools>\n%s\n</tools>\nTo invoke a tool, respond ONLY with:\n<function_calls>[{\"name\": \"function_name\", \"arguments\": {...}}]</function_calls>", toolsJSON)

			messages := gjson.GetBytes(modified, "messages").Array()
			hasSystem := false
			for i, m := range messages {
				if strings.ToLower(m.Get("role").String()) == "system" {
					existing := m.Get("content").String()
					modified, _ = sjson.SetBytes(modified, fmt.Sprintf("messages.%d.content", i), existing+toolInstruction)
					hasSystem = true
					break
				}
			}
			if !hasSystem {
				sysMsg := map[string]any{
					"role":    "system",
					"content": strings.TrimPrefix(toolInstruction, "\n\n"),
				}
				var currentMsgs []any
				if errUnmarshal := json.Unmarshal([]byte(gjson.GetBytes(modified, "messages").Raw), &currentMsgs); errUnmarshal == nil {
					currentMsgs = append([]any{sysMsg}, currentMsgs...)
					modified, _ = sjson.SetBytes(modified, "messages", currentMsgs)
				}
			}
		}
	}
	return modified
}

func postProcessCloudZenResponse(body []byte, modelID string) []byte {
	choices := gjson.GetBytes(body, "choices").Array()
	if len(choices) == 0 {
		return body
	}
	content := choices[0].Get("message.content").String()
	toolCalls, cleanContent := parseExternalToolCalls(content)
	if len(toolCalls) > 0 {
		modified := body
		modified, _ = sjson.SetBytes(modified, "choices.0.message.tool_calls", toolCalls)
		modified, _ = sjson.SetBytes(modified, "choices.0.message.content", cleanContent)
		modified, _ = sjson.SetBytes(modified, "choices.0.finish_reason", "tool_calls")
		return modified
	}
	return body
}

func (p *Plugin) pumpCloudZenStream(
	host hostRPC,
	downstreamStreamID string,
	upstreamStreamID string,
	model string,
	origReq []byte,
	targetFormat sdktranslator.Format,
) error {
	ctx := context.Background()
	openaiFormat := sdktranslator.FormatOpenAI
	var translateParam any

	var buffer []byte
	for {
		readResp, errRead := host.readHTTPStream(upstreamStreamID)
		if errRead != nil {
			return errRead
		}
		if len(readResp.Payload) > 0 {
			buffer = append(buffer, readResp.Payload...)
			for {
				newlineIdx := bytes.IndexByte(buffer, '\n')
				if newlineIdx == -1 {
					break
				}
				line := bytes.TrimRight(buffer[:newlineIdx], "\r")
				buffer = buffer[newlineIdx+1:]

				if len(line) == 0 {
					continue
				}

				if !bytes.HasPrefix(line, []byte("data: ")) {
					continue
				}

				data := bytes.TrimPrefix(line, []byte("data: "))
				if bytes.Equal(data, []byte("[DONE]")) {
					if targetFormat == openaiFormat {
						_ = host.emit(downstreamStreamID, []byte("data: [DONE]\n\n"), nil)
					} else {
						translated := sdktranslator.TranslateStream(ctx, openaiFormat, targetFormat, model, origReq, nil, line, &translateParam)
						for _, tChunk := range translated {
							_ = host.emit(downstreamStreamID, tChunk, nil)
						}
					}
					return nil
				}

				var usageDetail *pluginapi.UsageDetail
				if usageVal := gjson.GetBytes(data, "usage"); usageVal.Exists() {
					usageDetail = &pluginapi.UsageDetail{
						InputTokens:     usageVal.Get("prompt_tokens").Int(),
						OutputTokens:    usageVal.Get("completion_tokens").Int(),
						TotalTokens:     usageVal.Get("total_tokens").Int(),
						CachedTokens:    usageVal.Get("prompt_tokens_details.cached_tokens").Int(),
						ReasoningTokens: usageVal.Get("completion_tokens_details.reasoning_tokens").Int(),
					}
				}

				if targetFormat == openaiFormat {
					lineWithEnd := append(line, '\n', '\n')
					if errEmit := host.emit(downstreamStreamID, lineWithEnd, usageDetail); errEmit != nil {
						return errEmit
					}
				} else {
					translated := sdktranslator.TranslateStream(ctx, openaiFormat, targetFormat, model, origReq, nil, line, &translateParam)
					for _, tChunk := range translated {
						if len(tChunk) > 0 {
							if errEmit := host.emit(downstreamStreamID, tChunk, usageDetail); errEmit != nil {
								return errEmit
							}
						}
					}
				}
			}
		}
		if readResp.Done {
			break
		}
	}
	return nil
}
