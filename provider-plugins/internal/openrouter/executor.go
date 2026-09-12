package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
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
		return nil, fmt.Errorf("decode openrouter execute request: %w", err)
	}

	creds, errCreds := credentialsFromStorage(req.StorageJSON)
	if errCreds != nil {
		return nil, errCreds
	}

	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	from := sdktranslator.Format(normalizeFormat(req.ExecutorRequest))
	openaiFormat := sdktranslator.FormatOpenAI

	targetModel, errResolve := resolveRequestedModel(host, creds, req.Model)
	if errResolve != nil {
		return nil, errResolve
	}

	endpoint := fmt.Sprintf("%s/chat/completions", creds.baseURL())
	headers := buildAuthHeaders(creds)

	maxAttempts := 1
	if creds.isAutoFailover() {
		maxAttempts = 2
	}

	currentModel := targetModel
	var lastResp pluginapi.HTTPResponse
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		openaiReq := sdktranslator.TranslateRequest(from, openaiFormat, currentModel, req.Payload, true)
		preparedBody := prepareOpenRouterPayload(openaiReq, currentModel, false)

		resp, errDo := host.do(pluginapi.HTTPRequest{
			Method:  http.MethodPost,
			URL:     endpoint,
			Headers: headers,
			Body:    preparedBody,
		})
		if errDo != nil {
			lastErr = fmt.Errorf("OpenRouter upstream call failed: %w", errDo)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			globalCooldowns.markCooldown(currentModel, 10*time.Minute)
			if creds.isAutoFailover() && attempt < maxAttempts {
				fallback, errFallback := nextLiveFallback(host, creds, currentModel)
				if errFallback != nil {
					return nil, errFallback
				}
				currentModel = fallback
				continue
			}
			return nil, fmt.Errorf("OpenRouter 429 rate limit exceeded for model %q", currentModel)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("OpenRouter upstream returned HTTP %d: %s", resp.StatusCode, string(resp.Body))
		}

		lastResp = resp
		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}

	processedBody := normalizeOpenRouterResponse(lastResp.Body)

	var translateParam any
	respPayload := processedBody
	if from != openaiFormat {
		respPayload = sdktranslator.TranslateNonStream(context.Background(), openaiFormat, from, req.Model, req.OriginalRequest, nil, processedBody, &translateParam)
	}

	return pluginruntime.OK(pluginapi.ExecutorResponse{
		Headers: lastResp.Headers,
		Payload: respPayload,
	})
}

func (p *Plugin) executeStream(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode openrouter stream request: %w", err)
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return nil, fmt.Errorf("openrouter output stream ID is required")
	}

	creds, errCreds := credentialsFromStorage(req.StorageJSON)
	if errCreds != nil {
		return nil, errCreds
	}

	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	from := sdktranslator.Format(normalizeFormat(req.ExecutorRequest))
	openaiFormat := sdktranslator.FormatOpenAI

	targetModel, errResolve := resolveRequestedModel(host, creds, req.Model)
	if errResolve != nil {
		return nil, errResolve
	}

	endpoint := fmt.Sprintf("%s/chat/completions", creds.baseURL())
	headers := buildAuthHeaders(creds)

	maxAttempts := 1
	if creds.isAutoFailover() {
		maxAttempts = 2
	}

	currentModel := targetModel
	var streamResp hostHTTPStreamResponse
	var streamErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		openaiReq := sdktranslator.TranslateRequest(from, openaiFormat, currentModel, req.Payload, true)
		preparedBody := prepareOpenRouterPayload(openaiReq, currentModel, true)

		sResp, errStream := host.doStream(pluginapi.HTTPRequest{
			Method:  http.MethodPost,
			URL:     endpoint,
			Headers: headers,
			Body:    preparedBody,
		})
		if errStream != nil {
			streamErr = fmt.Errorf("OpenRouter stream call failed: %w", errStream)
			continue
		}

		if sResp.StatusCode == http.StatusTooManyRequests {
			host.closeHTTPStream(sResp.StreamID)
			globalCooldowns.markCooldown(currentModel, 10*time.Minute)
			if creds.isAutoFailover() && attempt < maxAttempts {
				fallback, errFallback := nextLiveFallback(host, creds, currentModel)
				if errFallback != nil {
					return nil, errFallback
				}
				currentModel = fallback
				continue
			}
			return nil, fmt.Errorf("OpenRouter 429 rate limit exceeded for model %q", currentModel)
		}

		if sResp.StatusCode != http.StatusOK {
			host.closeHTTPStream(sResp.StreamID)
			return nil, fmt.Errorf("OpenRouter upstream stream returned HTTP %d", sResp.StatusCode)
		}

		streamResp = sResp
		streamErr = nil
		break
	}

	if streamErr != nil {
		return nil, streamErr
	}

	if !p.beginStream(req.StreamID, host, streamResp.StreamID) {
		host.closeHTTPStream(streamResp.StreamID)
		return nil, fmt.Errorf("OpenRouter plugin is shutting down")
	}

	go func() {
		defer p.endStream(req.StreamID)
		defer host.closeHTTPStream(streamResp.StreamID)

		pumpErr := p.pumpOpenRouterStream(host, req.StreamID, streamResp.StreamID, req.Model, req.OriginalRequest, from)
		host.closeOutputStream(req.StreamID, pumpErr)
	}()

	respHeaders := http.Header{
		"Content-Type":  []string{"text/event-stream"},
		"Cache-Control": []string{"no-cache"},
	}
	return pluginruntime.OK(executorStreamResponse{Headers: respHeaders})
}

func prepareOpenRouterPayload(openaiReq []byte, modelID string, stream bool) []byte {
	modified := openaiReq
	var errSet error
	modified, errSet = sjson.SetBytes(modified, "model", modelID)
	if errSet != nil {
		modified = openaiReq
	}
	modified, _ = sjson.SetBytes(modified, "stream", stream)

	// Enable reasoning by default for OpenRouter
	if !gjson.GetBytes(modified, "include_reasoning").Exists() {
		modified, _ = sjson.SetBytes(modified, "include_reasoning", true)
	}

	return modified
}

func normalizeOpenRouterResponse(body []byte) []byte {
	// If response contains reasoning inside choices.0.message.reasoning,
	// ensure it's mapped to reasoning_content if reasoning_content is empty
	reasoning := gjson.GetBytes(body, "choices.0.message.reasoning")
	reasoningContent := gjson.GetBytes(body, "choices.0.message.reasoning_content")
	if reasoning.Exists() && (!reasoningContent.Exists() || reasoningContent.String() == "") {
		modified, err := sjson.SetBytes(body, "choices.0.message.reasoning_content", reasoning.String())
		if err == nil {
			return modified
		}
	}
	return body
}

func (p *Plugin) pumpOpenRouterStream(
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

				// Normalize reasoning in delta
				lineToSend := line
				deltaReasoning := gjson.GetBytes(data, "choices.0.delta.reasoning")
				deltaReasoningContent := gjson.GetBytes(data, "choices.0.delta.reasoning_content")
				if deltaReasoning.Exists() && (!deltaReasoningContent.Exists() || deltaReasoningContent.String() == "") {
					if modData, errMod := sjson.SetBytes(data, "choices.0.delta.reasoning_content", deltaReasoning.String()); errMod == nil {
						lineToSend = append([]byte("data: "), modData...)
					}
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
					lineWithEnd := append(lineToSend, '\n', '\n')
					if errEmit := host.emit(downstreamStreamID, lineWithEnd, usageDetail); errEmit != nil {
						return errEmit
					}
				} else {
					translated := sdktranslator.TranslateStream(ctx, openaiFormat, targetFormat, model, origReq, nil, lineToSend, &translateParam)
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

func normalizeFormat(req pluginapi.ExecutorRequest) string {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != "" {
		return format
	}
	return "openai"
}
