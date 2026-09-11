package lingma

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	lingmahelpers "github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/helpers"
	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	largeThinkingBodyWarningBytes   = 128 * 1024
	largeThinkingToolHistoryWarning = 20
	thinkingFallbackHeaderValue     = "lingma-thinking-disabled"
)

type executorRPCRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type executorHTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type executorStreamResponse struct {
	Headers http.Header `json:"headers,omitempty"`
}

type requestProfile struct {
	BodyBytes     int
	Messages      int
	ToolCalls     int
	ToolResults   int
	Tools         int
	LargeThinking bool
}

type fallbackDecision struct {
	Key      string
	Eligible bool
	Applied  bool
}

func (p *Plugin) execute(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma execute request: %w", errUnmarshal)
	}
	format := normalizeFormat(req.ExecutorRequest)
	if format == formatOpenAIResponse {
		return nil, newStatusError(http.StatusNotImplemented, "Lingma openai-response format is not supported")
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	creds, errCredentials := credentialsFromStorage(req.StorageJSON)
	if errCredentials != nil {
		return nil, errCredentials
	}
	body, errTranslate := translateRequestToLingma(format, baseModel, req.Payload, true)
	if errTranslate != nil {
		return nil, errTranslate
	}
	body = applyRequestThinking(body, req.ExecutorRequest, format)
	profile := inspectRequest(body, baseModel)
	body, fallback := p.applyThinkingFallback(body, req.Payload, baseModel, format, profile)
	if profile.LargeThinking && !fallback.Applied {
		p.logLargeThinking(hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}, profile, baseModel)
	}

	plan := newUpstreamPlan(p, hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}, creds, req.AuthAttributes, body, baseModel, profile, fallback)
	var aggregate []byte
	var responseHeaders http.Header
	var attemptBody []byte
	for {
		upstream, currentBody, errOpen := plan.do()
		if errOpen != nil {
			p.rememberThinkingFallback(errOpen, fallback, profile)
			return nil, normalizeUpstreamError(errOpen, profile)
		}
		aggregate = upstream.Body
		responseHeaders = upstream.Headers.Clone()
		attemptBody = currentBody
		if status, sseErr, isErr := findSSEError(aggregate); isErr {
			if shouldRetryStatus(status) && plan.hasNext() {
				plan.logRetry(sseErr, status)
				continue
			}
			return nil, sseErr
		}
		if !aggregateHasDone(aggregate) {
			incomplete := newStatusError(http.StatusBadGateway, "Lingma upstream stream ended before completion")
			if plan.hasNext() {
				plan.logRetry(incomplete, 0)
				continue
			}
			return nil, incomplete
		}
		break
	}

	output, errResponse := translateNonStreamFromLingma(format, req.Model, req.OriginalRequest, attemptBody, aggregate)
	if errResponse != nil {
		return nil, errResponse
	}
	usage, _ := parseAggregateUsage(aggregate)
	return pluginOK(pluginapi.ExecutorResponse{
		Payload: output,
		Headers: responseHeadersWithFallback(responseHeaders, plan.fallbackApplied),
		Usage:   usage,
	})
}

func (p *Plugin) executeStream(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma stream request: %w", errUnmarshal)
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return nil, fmt.Errorf("Lingma output stream ID is required")
	}
	format := normalizeFormat(req.ExecutorRequest)
	if format == formatOpenAIResponse {
		return nil, newStatusError(http.StatusNotImplemented, "Lingma openai-response format is not supported")
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	creds, errCredentials := credentialsFromStorage(req.StorageJSON)
	if errCredentials != nil {
		return nil, errCredentials
	}
	body, errTranslate := translateRequestToLingma(format, baseModel, req.Payload, true)
	if errTranslate != nil {
		return nil, errTranslate
	}
	body = applyRequestThinking(body, req.ExecutorRequest, format)
	profile := inspectRequest(body, baseModel)
	body, fallback := p.applyThinkingFallback(body, req.Payload, baseModel, format, profile)
	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	if profile.LargeThinking && !fallback.Applied {
		p.logLargeThinking(host, profile, baseModel)
	}
	plan := newUpstreamPlan(p, host, creds, req.AuthAttributes, body, baseModel, profile, fallback)
	upstream, attemptBody, errOpen := plan.openStream()
	if errOpen != nil {
		p.rememberThinkingFallback(errOpen, fallback, profile)
		return nil, normalizeUpstreamError(errOpen, profile)
	}
	if !p.beginStream(req.StreamID, host, upstream.StreamID) {
		host.closeHTTPStream(upstream.StreamID)
		return nil, fmt.Errorf("Lingma plugin is shutting down")
	}
	responseHeaders := responseHeadersWithFallback(upstream.Headers, plan.fallbackApplied)
	go func() {
		defer p.endStream(req.StreamID)
		p.runStream(req, format, baseModel, attemptBody, profile, fallback, plan, upstream)
	}()
	return pluginOK(executorStreamResponse{Headers: responseHeaders})
}

func (p *Plugin) runStream(req executorRPCRequest, format, baseModel string, attemptBody []byte, profile requestProfile, fallback fallbackDecision, plan *upstreamPlan, upstream hostHTTPStreamResponse) {
	host := plan.host
	var translateState any
	emittedOutput := false
	for {
		sawData, sawDone, streamErr := p.consumeUpstreamStream(host, upstream.StreamID, func(line []byte) error {
			if detail, ok := parseStreamUsage(line); ok {
				if errEmit := host.emit(req.StreamID, nil, detail); errEmit != nil {
					return errEmit
				}
			}
			frames, errTranslate := translateStreamFromLingma(format, req.Model, req.OriginalRequest, attemptBody, bytes.Clone(line), &translateState)
			if errTranslate != nil {
				return errTranslate
			}
			for _, frame := range frames {
				if len(frame) == 0 {
					continue
				}
				if errEmit := host.emit(req.StreamID, frame, nil); errEmit != nil {
					return errEmit
				}
				emittedOutput = true
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
				streamErr = newStatusError(http.StatusBadGateway, "Lingma upstream connection closed before response data")
			} else {
				streamErr = newStatusError(http.StatusBadGateway, "Lingma upstream stream ended before completion")
			}
		}
		status := http.StatusBadGateway
		if sc, ok := streamErr.(interface{ StatusCode() int }); ok && sc != nil {
			status = sc.StatusCode()
		}
		if !emittedOutput && shouldRetryStatus(status) && plan.hasNext() && !p.isShuttingDown() {
			plan.logRetry(streamErr, status)
			next, nextBody, errNext := plan.openStream()
			if errNext == nil {
				upstream, attemptBody = next, nextBody
				if p.updateStreamUpstream(req.StreamID, upstream.StreamID) {
					translateState = nil
					continue
				}
				host.closeHTTPStream(upstream.StreamID)
				streamErr = fmt.Errorf("Lingma plugin is shutting down")
			}
			if errNext != nil {
				streamErr = errNext
			}
		}
		if !emittedOutput {
			p.rememberThinkingFallback(streamErr, fallback, profile)
		}
		host.closeOutputStream(req.StreamID, normalizeUpstreamError(streamErr, profile))
		return
	}
}

func (p *Plugin) consumeUpstreamStream(host hostRPC, streamID string, onLine func([]byte) error) (sawData, sawDone bool, resultErr error) {
	var pending []byte
	processLine := func(line []byte) error {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(bytes.TrimSpace(line)) == 0 {
			return nil
		}
		sawData = true
		if errInfo, isErr := lingmahelpers.ParseLingmaError(line); isErr {
			return newStatusError(errInfo.StatusCode, errInfo.Message)
		}
		if lingmahelpers.IsLingmaDone(line) {
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

func (p *Plugin) countTokens(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma token count request: %w", errUnmarshal)
	}
	format := normalizeFormat(req.ExecutorRequest)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	openAIRequest := req.Payload
	if format == formatClaude {
		openAIRequest = openaiclaude.ConvertClaudeRequestToOpenAI(baseModel, req.Payload, false)
	} else if format != formatOpenAI {
		return nil, newStatusError(http.StatusBadRequest, fmt.Sprintf("Lingma token count format %q is not supported", format))
	}
	encoder, errEncoder := TokenizerForModel(baseModel)
	if errEncoder != nil {
		return nil, fmt.Errorf("initialize Lingma tokenizer: %w", errEncoder)
	}
	count, errCount := CountOpenAIChatTokens(encoder, openAIRequest)
	if errCount != nil {
		return nil, fmt.Errorf("count Lingma tokens: %w", errCount)
	}
	payload := BuildOpenAIUsageJSON(count)
	if format == formatClaude {
		payload = translatorcommon.ClaudeInputTokensJSON(count)
	}
	return pluginOK(pluginapi.ExecutorResponse{Payload: payload})
}

func (p *Plugin) httpRequest(raw []byte) ([]byte, error) {
	var req executorHTTPRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma HTTP request: %w", errUnmarshal)
	}
	headers := req.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	applyCustomHeaders(headers, req.Attributes)
	resp, errDo := (hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}).do(pluginapi.HTTPRequest{
		Method:    req.Method,
		URL:       req.URL,
		Headers:   headers,
		Body:      req.Body,
		Transport: pluginapi.HTTPTransportOptions{ForceHTTP11: p.configSnapshot().ForceHTTP11},
	})
	if errDo != nil {
		return nil, errDo
	}
	return pluginOK(pluginapi.ExecutorHTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Headers,
		Body:       resp.Body,
	})
}

func inspectRequest(body []byte, modelName string) requestProfile {
	profile := requestProfile{BodyBytes: len(body)}
	if !strings.EqualFold(strings.TrimSpace(modelName), "gm51model") || len(body) == 0 || !gjson.ValidBytes(body) {
		return profile
	}
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		profile.Messages = len(messages.Array())
		messages.ForEach(func(_, message gjson.Result) bool {
			if toolCalls := message.Get("tool_calls"); toolCalls.IsArray() {
				profile.ToolCalls += len(toolCalls.Array())
			}
			if strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "tool") {
				profile.ToolResults++
			}
			return true
		})
	}
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		profile.Tools = len(tools.Array())
	}
	profile.LargeThinking = gjson.GetBytes(body, "model_config.is_reasoning").Bool() &&
		profile.BodyBytes >= largeThinkingBodyWarningBytes &&
		profile.ToolCalls+profile.ToolResults >= largeThinkingToolHistoryWarning
	return profile
}

func (p *Plugin) applyThinkingFallback(body, source []byte, model, format string, profile requestProfile) ([]byte, fallbackDecision) {
	decision := fallbackDecision{}
	config := p.configSnapshot()
	if !config.ThinkingFallback || !profile.LargeThinking {
		return body, decision
	}
	decision.Key = fallbackKey(model, format, source)
	decision.Eligible = decision.Key != ""
	if !decision.Eligible || !p.fallback.consume(decision.Key) {
		return body, decision
	}
	decision.Applied = true
	return disableThinking(body), decision
}

func (p *Plugin) rememberThinkingFallback(err error, decision fallbackDecision, profile requestProfile) {
	config := p.configSnapshot()
	if err == nil || !config.ThinkingFallback || !decision.Eligible || decision.Applied || !profile.LargeThinking {
		return
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "context canceled") && !strings.Contains(message, "context cancelled") {
		return
	}
	p.fallback.mark(decision.Key, config.ThinkingFallbackTTL)
}

func fallbackKey(model, format string, payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(model))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(format))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil))
}

func disableThinking(body []byte) []byte {
	result, errSet := sjson.SetBytes(body, "model_config.is_reasoning", false)
	if errSet != nil {
		return body
	}
	result, _ = sjson.SetBytes(result, "model_config.source", "")
	result, _ = sjson.SetBytes(result, "agent_id", lingmahelpers.AgentCommon)
	return result
}

func responseHeadersWithFallback(headers http.Header, applied bool) http.Header {
	result := headers.Clone()
	if applied {
		if result == nil {
			result = make(http.Header)
		}
		result.Set("X-CLIProxy-Fallback", thinkingFallbackHeaderValue)
	}
	return result
}

func normalizeUpstreamError(err error, profile requestProfile) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "deadline exceeded") || strings.Contains(message, "timeout") {
		detail := "Lingma upstream timeout while waiting for response data"
		if profile.LargeThinking {
			detail += "; gm51model thinking may stall on large tool-call histories, reduce the history or disable reasoning"
		}
		return newStatusError(http.StatusGatewayTimeout, detail)
	}
	if strings.Contains(message, "unexpected eof") || message == "eof" {
		return newStatusError(http.StatusBadGateway, "Lingma upstream connection closed unexpectedly")
	}
	return err
}

func aggregateHasDone(data []byte) bool {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if lingmahelpers.IsLingmaDone(line) {
			return true
		}
	}
	return false
}

func findSSEError(data []byte) (int, error, bool) {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if info, ok := lingmahelpers.ParseLingmaError(line); ok {
			return info.StatusCode, newStatusError(info.StatusCode, info.Message), true
		}
	}
	if info, ok := lingmahelpers.ParseLingmaError(data); ok {
		return info.StatusCode, newStatusError(info.StatusCode, info.Message), true
	}
	return 0, nil, false
}

func (p *Plugin) logLargeThinking(host hostRPC, profile requestProfile, model string) {
	host.log("warn", "Lingma large thinking request may stall upstream", map[string]any{
		"provider":     ProviderID,
		"model":        model,
		"body_bytes":   profile.BodyBytes,
		"messages":     profile.Messages,
		"tool_calls":   profile.ToolCalls,
		"tool_results": profile.ToolResults,
		"tools":        profile.Tools,
	})
}

func chatURL(apiBaseURL string, body []byte, model string) string {
	agentID := gjson.GetBytes(body, "agent_id").String()
	if agentID == "" {
		agentID = lingmahelpers.AgentID(model)
	}
	return fmt.Sprintf("%s/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=%s&Encode=1", strings.TrimRight(apiBaseURL, "/"), agentID)
}

func encodeChatRequest(creds credentials, config pluginConfig, body []byte, model string, attributes map[string]string) (pluginapi.HTTPRequest, error) {
	requestURL := chatURL(config.APIBaseURL, body, model)
	encodedBody := lingmaencoding.Encode(body)
	headers, errHeaders := buildHeaders(creds, string(encodedBody), requestURL, time.Now())
	if errHeaders != nil {
		return pluginapi.HTTPRequest{}, errHeaders
	}
	applyCustomHeaders(headers, attributes)
	return pluginapi.HTTPRequest{
		Method:    http.MethodPost,
		URL:       requestURL,
		Headers:   headers,
		Body:      []byte(encodedBody),
		Transport: pluginapi.HTTPTransportOptions{ForceHTTP11: config.ForceHTTP11},
	}, nil
}

func marshalStorage(creds credentials) []byte {
	raw, _ := json.Marshal(creds)
	return raw
}

func isContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || strings.Contains(strings.ToLower(err.Error()), "context canceled")
}
