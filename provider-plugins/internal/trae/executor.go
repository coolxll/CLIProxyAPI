package trae

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// executorRPCRequest extends the plugin API executor request with host callback routing.
type executorRPCRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// executorHTTPRequest extends the plugin API HTTP request with host callback routing.
type executorHTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// executorStreamResponse is the local streaming response type (no Chunks channel).
type executorStreamResponse struct {
	Headers http.Header `json:"headers,omitempty"`
}

// execute handles non-streaming execution by aggregating streaming chunks.
func (p *Plugin) execute(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode trae execute request: %w", err)
	}

	// Force stream=true for Claude translator compatibility
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

	// Build and open upstream stream
	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	creds, build, openaiReq, err := p.prepareRequest(host, streamReq)
	if err != nil {
		return nil, err
	}
	upstream, err := p.openUpstreamStream(host, creds, build)
	if err != nil {
		return nil, err
	}

	// Aggregate all synthesized OpenAI SSE chunks
	var aggregate bytes.Buffer
	var translateParam any
	from := sdktranslator.Format(req.SourceFormat)
	openaiFormat := sdktranslator.FormatOpenAI

	// Emit initial assistant role chunk into aggregate
	initChunk := p.buildInitChunk(req.Model)
	initBytes := []byte("data: " + string(initChunk))
	firstTranslated := sdktranslator.TranslateStream(context.Background(), openaiFormat, from, req.Model, req.OriginalRequest, nil, initBytes, &translateParam)
	for _, ft := range firstTranslated {
		aggregate.Write(ft)
		aggregate.WriteByte('\n')
	}

	// Process upstream stream and collect synthesized chunks
	_, _, err = p.consumeUpstreamStream(host, upstream.StreamID, func(line []byte) error {
		chunks := p.processSSELine(line, req, build, openaiReq, from, openaiFormat, &translateParam)
		for _, chunk := range chunks {
			aggregate.Write(chunk)
			aggregate.WriteByte('\n')
		}
		return nil
	})
	host.closeHTTPStream(upstream.StreamID)
	if err != nil {
		return nil, err
	}

	// Parse aggregated OpenAI SSE and build non-streaming response
	return p.aggregateToResponse(aggregate.Bytes(), req, upstream.Headers, from, openaiFormat)
}

// executeStream handles streaming execution requests.
func (p *Plugin) executeStream(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode trae stream request: %w", err)
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return nil, fmt.Errorf("trae output stream ID is required")
	}

	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	creds, build, openaiReq, err := p.prepareRequest(host, req)
	if err != nil {
		return nil, err
	}
	upstream, err := p.openUpstreamStream(host, creds, build)
	if err != nil {
		return nil, err
	}

	go p.runStream(req, build, openaiReq, upstream)
	return pluginOK(executorStreamResponse{Headers: upstream.Headers})
}

// runStream processes the upstream stream and emits synthesized chunks to the output stream.
func (p *Plugin) runStream(req executorRPCRequest, build *traeRequestBuildResult, openaiReq []byte, upstream hostHTTPStreamResponse) {
	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	from := sdktranslator.Format(req.SourceFormat)
	openaiFormat := sdktranslator.FormatOpenAI
	var translateParam any

	// Emit initial assistant role chunk
	initChunk := p.buildInitChunk(req.Model)
	initBytes := []byte("data: " + string(initChunk))
	firstTranslated := sdktranslator.TranslateStream(context.Background(), openaiFormat, from, req.Model, req.OriginalRequest, nil, initBytes, &translateParam)
	for _, ft := range firstTranslated {
		if err := host.emit(req.StreamID, ft, nil); err != nil {
			host.closeOutputStream(req.StreamID, err)
			return
		}
	}

	// Process upstream stream
	sawData, sawDone, streamErr := p.consumeUpstreamStream(host, upstream.StreamID, func(line []byte) error {
		chunks := p.processSSELine(line, req, build, openaiReq, from, openaiFormat, &translateParam)
		for _, chunk := range chunks {
			if err := host.emit(req.StreamID, chunk, nil); err != nil {
				return err
			}
		}
		return nil
	})
	host.closeHTTPStream(upstream.StreamID)

	if streamErr == nil && sawDone {
		host.closeOutputStream(req.StreamID, nil)
		return
	}
	if streamErr == nil {
		if !sawData {
			streamErr = newStatusError(http.StatusBadGateway, "Trae upstream connection closed before response data")
		} else {
			streamErr = newStatusError(http.StatusBadGateway, "Trae upstream stream ended before completion")
		}
	}
	host.closeOutputStream(req.StreamID, streamErr)
}

// prepareRequest parses credentials, resolves protocol, and builds the Trae request.
func (p *Plugin) prepareRequest(host hostRPC, req executorRPCRequest) (credentials, *traeRequestBuildResult, []byte, error) {
	creds, err := credentialsFromStorage(req.StorageJSON)
	if err != nil {
		return credentials{}, nil, nil, err
	}

	baseModel := req.Model
	protocol, upstreamModel := resolveTraeProtocol(baseModel, req.Metadata)

	from := sdktranslator.Format(req.SourceFormat)
	openaiFormat := sdktranslator.FormatOpenAI
	openaiReq := sdktranslator.TranslateRequest(from, openaiFormat, upstreamModel, req.Payload, true)
	messages := gjson.GetBytes(openaiReq, "messages").Array()

	// Check for tool commit (V3 only)
	isToolCommit := false
	var toolMessages []gjson.Result
	if protocol == traeProtocolV3 {
		for i := len(messages) - 1; i >= 0; i-- {
			role := messages[i].Get("role").String()
			if role == "tool" {
				toolMessages = append([]gjson.Result{messages[i]}, toolMessages...)
				isToolCommit = true
			} else if isToolCommit {
				break
			}
		}
	}

	var build *traeRequestBuildResult
	if isToolCommit && len(toolMessages) > 0 {
		build, err = buildTraeToolCommitRequest(creds, toolMessages)
	} else if protocol == traeProtocolV1 || protocol == traeProtocolV2 {
		build, err = buildTraeRawChatRequest(protocol, upstreamModel, openaiReq, req.Metadata)
	} else {
		build, err = buildTraeV3CreateTaskRequest(creds, upstreamModel, openaiReq, messages, req.Metadata)
	}
	if err != nil {
		return credentials{}, nil, nil, err
	}
	return creds, build, openaiReq, nil
}

// openUpstreamStream opens the upstream HTTP stream for the given build result.
func (p *Plugin) openUpstreamStream(host hostRPC, creds credentials, build *traeRequestBuildResult) (hostHTTPStreamResponse, error) {
	httpReq := pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     build.TargetURL,
		Headers: make(http.Header),
		Body:    build.RequestBody,
	}
	httpReq.Headers.Set("Content-Type", "application/json")
	setTraeCommonHeaders(httpReq.Headers, creds)
	httpReq.Headers.Set("X-Ide-Session-Id", build.SessionID)
	httpReq.Headers.Set("X-Request-Pin", build.RequestPin)
	httpReq.Headers.Set("X-Requested-At", strconv.FormatInt(build.RequestAt, 10))
	httpReq.Headers.Set("Accept", "text/event-stream")
	httpReq.Headers.Set("Cache-Control", "no-cache")
	for name, values := range build.ExtraHeaders {
		for _, value := range values {
			httpReq.Headers.Set(name, value)
		}
	}

	upstream, err := host.doStream(httpReq)
	if err != nil {
		return hostHTTPStreamResponse{}, fmt.Errorf("open upstream stream: %w", err)
	}
	return upstream, nil
}

// buildInitChunk creates the initial assistant role chunk.
func (p *Plugin) buildInitChunk(model string) []byte {
	initChunk := openaiChunk{
		ID:      fmt.Sprintf("chatcmpl-%s", strings.ReplaceAll(uuid.New().String(), "-", "")),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openaiChoice{
			{Index: 0, Delta: openaiDelta{Role: "assistant"}},
		},
	}
	data, _ := json.Marshal(initChunk)
	return data
}

// processSSELine processes a single SSE line and returns synthesized OpenAI chunks.
func (p *Plugin) processSSELine(line []byte, req executorRPCRequest, build *traeRequestBuildResult, openaiReq []byte, from, openaiFormat sdktranslator.Format, translateParam *any) [][]byte {
	// This is a simplified version - full implementation would handle all SSE event types
	// For now, just pass through valid data lines
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
		if string(data) == "[DONE]" {
			return [][]byte{line}
		}
		if gjson.ValidBytes(data) {
			// Translate the chunk
			translated := sdktranslator.TranslateStream(context.Background(), openaiFormat, from, req.Model, req.OriginalRequest, nil, line, translateParam)
			return translated
		}
	}
	return nil
}

// consumeUpstreamStream reads the upstream stream line by line.
func (p *Plugin) consumeUpstreamStream(host hostRPC, streamID string, onLine func([]byte) error) (sawData, sawDone bool, resultErr error) {
	var pending []byte
	processLine := func(line []byte) error {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(bytes.TrimSpace(line)) == 0 {
			return nil
		}
		sawData = true
		if bytes.Equal(bytes.TrimSpace(line), []byte("data: [DONE]")) {
			sawDone = true
		}
		return onLine(line)
	}
	for {
		chunk, errRead := host.readHTTPStream(streamID)
		if errRead != nil {
			return sawData, sawDone, errRead
		}
		pending = append(pending, chunk.Payload...)
		for {
			newline := bytes.IndexByte(pending, '\n')
			if newline < 0 {
				break
			}
			line := bytes.Clone(pending[:newline])
			pending = pending[newline+1:]
			if errLine := processLine(line); errLine != nil {
				return sawData, sawDone, errLine
			}
			if sawDone {
				return sawData, true, nil
			}
		}
		if chunk.Error != "" {
			return sawData, sawDone, errors.New(chunk.Error)
		}
		if chunk.Done {
			if len(bytes.TrimSpace(pending)) > 0 {
				if errLine := processLine(bytes.Clone(pending)); errLine != nil {
					return sawData, sawDone, errLine
				}
			}
			return sawData, sawDone, nil
		}
	}
}

// aggregateToResponse parses aggregated OpenAI SSE and builds a non-streaming response.
func (p *Plugin) aggregateToResponse(aggregate []byte, req executorRPCRequest, headers http.Header, from, openaiFormat sdktranslator.Format) ([]byte, error) {
	var aggregatedContent strings.Builder
	var aggregatedReasoning strings.Builder
	var toolCalls []openaiToolCall
	var finalModel string
	var chatID string
	var finalUsage openaiUsage
	var hasUsage bool

	for _, line := range bytes.Split(aggregate, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
		if string(data) == "[DONE]" || !gjson.ValidBytes(data) {
			continue
		}

		root := gjson.ParseBytes(data)
		if id := root.Get("id").String(); id != "" && chatID == "" {
			chatID = id
		}
		if model := root.Get("model").String(); model != "" {
			finalModel = model
		}
		if root.Get("choices").Exists() {
			if contentVal := root.Get("choices.0.delta.content"); contentVal.Exists() {
				aggregatedContent.WriteString(contentVal.String())
			}
			if reasoningVal := root.Get("choices.0.delta.reasoning_content"); reasoningVal.Exists() {
				aggregatedReasoning.WriteString(reasoningVal.String())
			}
			if tcVal := root.Get("choices.0.delta.tool_calls"); tcVal.Exists() && tcVal.IsArray() {
				for _, tc := range tcVal.Array() {
					idx := int(tc.Get("index").Int())
					found := false
					for i := range toolCalls {
						if toolCalls[i].Index == idx {
							if id := tc.Get("id").String(); id != "" {
								toolCalls[i].ID = id
							}
							if name := tc.Get("function.name").String(); name != "" {
								toolCalls[i].Function.Name += name
							}
							if args := tc.Get("function.arguments").String(); args != "" {
								toolCalls[i].Function.Arguments += args
							}
							found = true
							break
						}
					}
					if !found {
						toolCalls = append(toolCalls, openaiToolCall{
							Index: idx,
							ID:    tc.Get("id").String(),
							Type:  tc.Get("type").String(),
							Function: openaiFunction{
								Name:      tc.Get("function.name").String(),
								Arguments: tc.Get("function.arguments").String(),
							},
						})
					}
				}
			}
			if fr := root.Get("choices.0.finish_reason").String(); fr != "" {
				// Terminal chunk
			}
		}
		if usageVal := root.Get("usage"); usageVal.Exists() {
			finalUsage = openAIUsageFromResult(usageVal)
			hasUsage = true
		}
	}

	// Promote reasoning to content if no text
	if aggregatedContent.Len() == 0 && aggregatedReasoning.Len() > 0 && len(toolCalls) == 0 {
		aggregatedContent.WriteString(aggregatedReasoning.String())
		aggregatedReasoning.Reset()
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	if chatID == "" {
		chatID = fmt.Sprintf("chatcmpl-%s", strings.ReplaceAll(uuid.New().String(), "-", ""))
	}
	if finalModel == "" {
		finalModel = req.Model
	}

	openaiResp := openaiNonStreamResponse{
		ID:      chatID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   finalModel,
		Choices: []openaiNonStreamChoice{
			{
				Index: 0,
				Message: openaiMessage{
					Role:             "assistant",
					Content:          aggregatedContent.String(),
					ReasoningContent: aggregatedReasoning.String(),
					ToolCalls:        toolCalls,
				},
				FinishReason: finishReason,
			},
		},
	}
	if hasUsage {
		openaiResp.Usage = finalUsage
	}

	openaiRespBytes, err := json.Marshal(openaiResp)
	if err != nil {
		return nil, err
	}

	var translateParam any
	translatedNonStream := sdktranslator.TranslateNonStream(context.Background(), openaiFormat, from, req.Model, req.OriginalRequest, nil, openaiRespBytes, &translateParam)

	resp := pluginapi.ExecutorResponse{
		Payload: translatedNonStream,
		Headers: headers,
	}
	return pluginruntime.OK(resp)
}

// countTokens handles token counting requests.
func (p *Plugin) countTokens(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode trae token count request: %w", err)
	}
	if req.SourceFormat == "" {
		return nil, fmt.Errorf("source format is required")
	}

	baseModel := req.Model
	_, upstreamModel := resolveTraeProtocol(baseModel, req.Metadata)

	from := sdktranslator.Format(req.SourceFormat)
	to := sdktranslator.FormatOpenAI
	translated := sdktranslator.TranslateRequest(from, to, upstreamModel, req.Payload, false)

	// Simple token estimation (plugin doesn't have tokenizer)
	tokenCount := int64(len(translated) / 4)

	usageJSON := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":0,"total_tokens":%d}`, tokenCount, tokenCount)
	translatedUsage := sdktranslator.TranslateTokenCount(context.Background(), to, from, tokenCount, []byte(usageJSON))

	resp := pluginapi.ExecutorResponse{
		Payload: translatedUsage,
	}
	return pluginruntime.OK(resp)
}

// httpRequest handles direct HTTP passthrough requests.
func (p *Plugin) httpRequest(raw []byte) ([]byte, error) {
	var req executorHTTPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode trae HTTP request: %w", err)
	}
	headers := req.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	resp, err := (hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}).do(pluginapi.HTTPRequest{
		Method:  req.Method,
		URL:     req.URL,
		Headers: headers,
		Body:    req.Body,
	})
	if err != nil {
		return nil, err
	}
	return pluginOK(pluginapi.ExecutorHTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Headers,
		Body:       resp.Body,
	})
}

// newStatusError creates a status error with the given HTTP status code and message.
func newStatusError(code int, msg string) error {
	return traeStatusErr{code: code, msg: msg}
}
