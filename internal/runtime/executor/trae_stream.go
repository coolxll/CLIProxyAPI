package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type openaiDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

type openaiToolCall struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function openaiFunction `json:"function,omitempty"`
}

type openaiFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiChoice struct {
	Index        int         `json:"index"`
	Delta        openaiDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type openaiChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
}

type openaiUsage struct {
	PromptTokens            int64                    `json:"prompt_tokens"`
	CompletionTokens        int64                    `json:"completion_tokens"`
	TotalTokens             int64                    `json:"total_tokens"`
	PromptTokensDetails     *openaiPromptDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *openaiCompletionDetails `json:"completion_tokens_details,omitempty"`
}

type openaiPromptDetails struct {
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

type openaiCompletionDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
}

func (e *TraeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	protocol, upstreamModel := resolveTraeProtocol(baseModel, opts.Metadata)
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		return nil, fmt.Errorf("trae auth check failed: %w", err)
	}

	from := opts.SourceFormat
	openaiFormat := sdktranslator.FromString("openai")
	openaiReq := sdktranslator.TranslateRequest(from, openaiFormat, upstreamModel, req.Payload, true)
	messages := gjson.GetBytes(openaiReq, "messages").Array()

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

	build, err := e.buildTraeRequest(auth, creds, protocol, upstreamModel, openaiReq, messages, isToolCommit, toolMessages, opts)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, build.TargetURL, bytes.NewReader(build.RequestBody))
	if err != nil {
		return nil, err
	}

	// Request settings matching reverse-engineered headers
	httpReq.Header.Set("Content-Type", "application/json")
	setTraeCommonHeaders(httpReq.Header, creds)
	httpReq.Header.Set("X-Ide-Session-Id", build.SessionID)
	httpReq.Header.Set("X-Request-Pin", build.RequestPin)
	httpReq.Header.Set("X-Requested-At", strconv.FormatInt(build.RequestAt, 10))
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	for name, values := range build.ExtraHeaders {
		for _, value := range values {
			httpReq.Header.Set(name, value)
		}
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       build.TargetURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      build.LogBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("trae executor: close response body error: %v", errClose)
		}
		return nil, traeStatusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("trae executor: close response body error: %v", errClose)
			}
		}()

		chatID := fmt.Sprintf("chatcmpl-%s", strings.ReplaceAll(uuid.New().String(), "-", ""))

		// First, yield standard opening OpenAI assistant role delta
		initOpenAIChunk := openaiChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []openaiChoice{
				{
					Index: 0,
					Delta: openaiDelta{
						Role: "assistant",
					},
				},
			},
		}

		initJSON, _ := json.Marshal(initOpenAIChunk)
		initChunk := []byte("data: " + string(initJSON))

		var translateParam any
		firstTranslated := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, initChunk, &translateParam)
		for _, ft := range firstTranslated {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: ft}:
			case <-ctx.Done():
				return
			}
		}

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		taskID := "unknown"
		agentRunID := "unknown"
		tcIndex := 0
		thoughtToolIndex := 0
		inlineToolIndex := 0
		hasToolCall := false
		hasContentDelta := false
		hasReasoningDelta := false
		finishReason := "stop"
		var accumulatedUsage openaiUsage
		hasUsage := false
		var accumulatedReasoning strings.Builder
		var thoughtToolParser traeThoughtToolParser
		var inlineContentToolParser traeInlineToolCallParser
		var inlineReasoningToolParser traeInlineToolCallParser
		var reasoningThinkStripper deepSeekThinkTagStripper
		pendingHistoryContent := ""
		normalizeToolName := buildTraeToolNameNormalizer(openaiReq, opts.OriginalRequest)
		emittedToolSignatures := make(map[string]struct{})
		shouldEmitToolCall := func(name, arguments string) bool {
			signature := traeToolSignature(name, arguments)
			if signature == "" {
				return true
			}
			if _, exists := emittedToolSignatures[signature]; exists {
				return false
			}
			emittedToolSignatures[signature] = struct{}{}
			return true
		}
		buildToolCalls := func(parsed []traeThoughtToolCall, nativePrefix, logContext string, nextNativeIndex *int) []openaiToolCall {
			toolCalls := make([]openaiToolCall, 0, len(parsed))
			for _, toolCall := range parsed {
				toolName := normalizeToolName(toolCall.Name)
				toolArguments := normalizeTraeToolArguments(toolName, toolCall.Arguments)
				if !shouldEmitToolCall(toolName, toolArguments) {
					continue
				}
				nativeID := fmt.Sprintf("%s-%d", nativePrefix, *nextNativeIndex)
				(*nextNativeIndex)++
				state := traeToolState{
					SessionID:      build.SessionID,
					ConversationID: build.ConversationID,
					TaskID:         taskID,
					AgentRunID:     agentRunID,
					NativeID:       nativeID,
					Name:           toolName,
				}
				encodedID, errEncode := encodeTraeToolID(state)
				if errEncode != nil {
					log.Errorf("trae executor: encode %s tool id error: %v", logContext, errEncode)
					continue
				}
				hasToolCall = true
				toolCalls = append(toolCalls, openaiToolCall{
					Index: tcIndex,
					ID:    encodedID,
					Type:  "function",
					Function: openaiFunction{
						Name:      toolName,
						Arguments: toolArguments,
					},
				})
				tcIndex++
			}
			return toolCalls
		}

		inQueue := false
		queueDone := make(chan struct{})
		var queueDoneOnce sync.Once
		var queueHeartbeatStarted atomic.Bool
		closeQueueHeartbeat := func() {
			queueDoneOnce.Do(func() {
				close(queueDone)
			})
		}
		defer closeQueueHeartbeat()

		var currentEvent string
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				continue
			}

			if bytes.HasPrefix(trimmedLine, []byte("event:")) {
				currentEvent = strings.TrimSpace(string(bytes.TrimPrefix(trimmedLine, []byte("event:"))))
				continue
			}

			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				continue
			}

			dataBytes := bytes.TrimSpace(bytes.TrimPrefix(trimmedLine, []byte("data:")))
			if len(dataBytes) == 0 {
				continue
			}

			dataStr := string(dataBytes)
			if !gjson.Valid(dataStr) {
				continue
			}

			evt := currentEvent
			currentEvent = ""

			if evt == "error" {
				message := firstNonEmpty(
					gjson.Get(dataStr, "message").String(),
					gjson.Get(dataStr, "error").String(),
					dataStr,
				)
				code := gjson.Get(dataStr, "code").String()
				if code != "" {
					message = fmt.Sprintf("trae error event %s: %s", code, message)
				} else {
					message = fmt.Sprintf("trae error event: %s", message)
				}
				streamErr := traeStatusErr{code: http.StatusBadGateway, msg: message}
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
				case <-ctx.Done():
				}
				return
			}

			if evt == "task_created" {
				if tID := gjson.Get(dataStr, "task_id").String(); tID != "" {
					taskID = tID
				}
				if aRunID := gjson.Get(dataStr, "agent_run_id").String(); aRunID != "" {
					agentRunID = aRunID
				}
			}

			if evt == "request_wait_in_queue" {
				inQueue = true
				if queueHeartbeatStarted.CompareAndSwap(false, true) {
					go func() {
						ticker := time.NewTicker(15 * time.Second)
						defer ticker.Stop()
						var heartbeatTranslateParam any
						for {
							select {
							case <-ticker.C:
								heartbeatChunk := openaiChunk{
									ID:      chatID,
									Object:  "chat.completion.chunk",
									Created: time.Now().Unix(),
									Model:   req.Model,
									Choices: []openaiChoice{
										{
											Index: 0,
											Delta: openaiDelta{},
										},
									},
								}
								heartbeatJSON, _ := json.Marshal(heartbeatChunk)
								heartbeatBytes := []byte("data: " + string(heartbeatJSON))
								translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, heartbeatBytes, &heartbeatTranslateParam)
								for _, tc := range translatedChunks {
									select {
									case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
									case <-queueDone:
										return
									case <-ctx.Done():
										return
									}
								}
							case <-queueDone:
								return
							case <-ctx.Done():
								return
							}
						}
					}()
				}
				continue
			}

			content := ""
			if evt == "history" {
				// Use history as an EOF fallback only. It is usually a full transcript snapshot,
				// so emitting it inline can replay stale assistant messages.
				pendingHistoryContent = ""
				historyMessages := gjson.Get(dataStr, "history_data.messages")
				if historyMessages.Exists() && historyMessages.Type == gjson.String {
					var rawMsgs struct {
						RawMessages []struct {
							Role    string `json:"role"`
							Content []struct {
								Type string `json:"type"`
								Text string `json:"text"`
							} `json:"content"`
						} `json:"raw_messages"`
					}
					if errUnmarshal := json.Unmarshal([]byte(historyMessages.String()), &rawMsgs); errUnmarshal != nil {
						log.Debugf("trae executor: history event unmarshal error: %v", errUnmarshal)
					} else {
						for i := len(rawMsgs.RawMessages) - 1; i >= 0; i-- {
							msg := rawMsgs.RawMessages[i]
							if msg.Role == "assistant" {
								var historyContent strings.Builder
								for _, c := range msg.Content {
									if c.Type == "text" && c.Text != "" {
										historyContent.WriteString(c.Text)
									}
								}
								if historyContent.Len() > 0 {
									pendingHistoryContent = historyContent.String()
								}
								break
							}
						}
					}
				}
				continue
			} else if val := gjson.Get(dataStr, "choices.0.delta.content"); val.Exists() && val.Type == gjson.String {
				content = val.String()
			} else if val := gjson.Get(dataStr, "response"); val.Exists() && val.Type == gjson.String {
				content = val.String()
			} else if val := gjson.Get(dataStr, "content"); val.Exists() && val.Type == gjson.String {
				content = val.String()
			} else if val := gjson.Get(dataStr, "text"); val.Exists() && val.Type == gjson.String {
				content = val.String()
			}

			reasoning := ""
			if val := gjson.Get(dataStr, "choices.0.delta.reasoning_content"); val.Exists() && val.Type == gjson.String {
				reasoning = val.String()
			} else if val := gjson.Get(dataStr, "reasoning_content"); val.Exists() && val.Type == gjson.String {
				reasoning = val.String()
			}

			var toolCalls []openaiToolCall
			if evt == "thought" {
				thoughtField := gjson.Get(dataStr, "thought")
				reasoningField := gjson.Get(dataStr, "reasoning_content")
				if thoughtField.Exists() && thoughtField.Type == gjson.String && thoughtField.String() != "" {
					// "thought" field: extract tool calls AND remaining text → public content
					parsed := thoughtToolParser.Append(thoughtField.String())
					content += parsed.Content
					toolCalls = append(toolCalls, buildToolCalls(parsed.ToolCalls, "thought", "thought", &thoughtToolIndex)...)
				} else if reasoningField.Exists() && reasoningField.Type == gjson.String && reasoningField.String() != "" {
					// "reasoning_content" field: extract tool calls only, do NOT leak
					// reasoning text into public content — it's already set as reasoning above.
					parsed := thoughtToolParser.Append(reasoningField.String())
					if len(parsed.ToolCalls) > 0 {
						toolCalls = append(toolCalls, buildToolCalls(parsed.ToolCalls, "thought", "thought", &thoughtToolIndex)...)
					}
				}
			}

			hasUpstreamUsage := false
			var usageData openaiUsage
			if evt == "token_usage" {
				usageData = openAIUsageFromResult(gjson.Parse(dataStr))
				hasUpstreamUsage = true
			} else if usageVal := gjson.Get(dataStr, "usage"); usageVal.Exists() {
				usageData = openAIUsageFromResult(usageVal)
				hasUpstreamUsage = true
			}

			if hasUpstreamUsage {
				accumulatedUsage.PromptTokens += usageData.PromptTokens
				accumulatedUsage.CompletionTokens += usageData.CompletionTokens
				accumulatedUsage.TotalTokens += usageData.TotalTokens
				if usageData.PromptTokensDetails != nil {
					if accumulatedUsage.PromptTokensDetails == nil {
						accumulatedUsage.PromptTokensDetails = &openaiPromptDetails{}
					}
					accumulatedUsage.PromptTokensDetails.CachedTokens += usageData.PromptTokensDetails.CachedTokens
				}
				if usageData.CompletionTokensDetails != nil {
					if accumulatedUsage.CompletionTokensDetails == nil {
						accumulatedUsage.CompletionTokensDetails = &openaiCompletionDetails{}
					}
					accumulatedUsage.CompletionTokensDetails.ReasoningTokens += usageData.CompletionTokensDetails.ReasoningTokens
				}
				hasUsage = true

				reporter.Publish(ctx, usageDetailFromOpenAIUsage(usageData))
				if evt == "token_usage" {
					continue
				}
			}

			if evt == "agent_event" || gjson.Get(dataStr, "event").String() == "agent_event" {
				payloadVal := gjson.Get(dataStr, "payload")
				if payloadVal.Exists() {
					payloadStr := payloadVal.String()
					if gjson.Valid(payloadStr) {
						if msgVal := gjson.Get(payloadStr, "message"); msgVal.Exists() && msgVal.Type == gjson.String {
							content += msgVal.String()
						}
					}
				}
			}

			if content != "" {
				parsed := inlineContentToolParser.Append(content)
				content = stripTrailingToolCallResidue(parsed.Content, len(parsed.ToolCalls) > 0)
				toolCalls = append(toolCalls, buildToolCalls(parsed.ToolCalls, "inline", "inline", &inlineToolIndex)...)
			}
			if reasoning != "" {
				reasoning = reasoningThinkStripper.Append(reasoning)
				parsed := inlineReasoningToolParser.Append(reasoning)
				reasoning = stripTrailingToolCallResidue(parsed.Content, len(parsed.ToolCalls) > 0)
				if strings.TrimSpace(reasoning) == "" {
					reasoning = ""
				}
				toolCalls = append(toolCalls, buildToolCalls(parsed.ToolCalls, "inline", "inline reasoning", &inlineToolIndex)...)
			}

			if tcVal := gjson.Get(dataStr, "choices.0.delta.tool_calls"); tcVal.Exists() && tcVal.IsArray() {
				for _, tc := range tcVal.Array() {
					toolName := normalizeToolName(tc.Get("function.name").String())
					toolArguments := normalizeTraeToolArguments(toolName, tc.Get("function.arguments").String())
					hasToolCall = true
					toolCalls = append(toolCalls, openaiToolCall{
						Index: int(tc.Get("index").Int()),
						ID:    tc.Get("id").String(),
						Type:  tc.Get("type").String(),
						Function: openaiFunction{
							Name:      toolName,
							Arguments: toolArguments,
						},
					})
				}
			}

			toolName := ""
			toolPayload := ""
			nativeToolCallID := ""
			if evt == "tool_call" || gjson.Get(dataStr, "tool_name").Exists() || gjson.Get(dataStr, "toolcall_name").Exists() {
				toolName = firstNonEmpty(
					gjson.Get(dataStr, "tool_name").String(),
					gjson.Get(dataStr, "toolcall_name").String(),
					gjson.Get(dataStr, "name").String(),
				)
				// Filter out field-name-as-value artifacts from V3 API
				if toolName == "tool_name" || toolName == "toolcall_name" {
					toolName = ""
				}
				toolPayload = firstNonEmpty(
					gjson.Get(dataStr, "arguments").String(),
					gjson.Get(dataStr, "toolcall_payload").String(),
				)
				nativeToolCallID = gjson.Get(dataStr, "toolcall_id").String()
			}
			if val := gjson.Get(dataStr, "finish_reason"); val.Exists() && val.String() != "" {
				finishReason = val.String()
			}

			if inQueue && (content != "" || reasoning != "" || toolName != "" || len(toolCalls) > 0) {
				inQueue = false
				closeQueueHeartbeat()
			}

			if content != "" || reasoning != "" || toolName != "" || len(toolCalls) > 0 {
				if strings.TrimSpace(content) != "" {
					hasContentDelta = true
				}
				if strings.TrimSpace(reasoning) != "" {
					hasReasoningDelta = true
					accumulatedReasoning.WriteString(reasoning)
				}
				delta := openaiDelta{
					Content:          content,
					ReasoningContent: reasoning,
					ToolCalls:        toolCalls,
				}

				toolName = normalizeToolName(toolName)
				toolPayload = normalizeTraeToolArguments(toolName, toolPayload)
				if toolName != "" && !shouldEmitToolCall(toolName, toolPayload) {
					toolName = ""
				}
				if toolName != "" {
					hasToolCall = true
					state := traeToolState{
						SessionID:      build.SessionID,
						ConversationID: build.ConversationID,
						TaskID:         taskID,
						AgentRunID:     agentRunID,
						NativeID:       nativeToolCallID,
						Name:           toolName,
					}
					encodedID, _ := encodeTraeToolID(state)
					delta.ToolCalls = append(delta.ToolCalls, openaiToolCall{
						Index: tcIndex,
						ID:    encodedID,
						Type:  "function",
						Function: openaiFunction{
							Name:      toolName,
							Arguments: toolPayload,
						},
					})
					tcIndex++
				}

				syntheticOpenAIChunk := openaiChunk{
					ID:      chatID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []openaiChoice{
						{
							Index: 0,
							Delta: delta,
						},
					},
				}

				chunkJSON, _ := json.Marshal(syntheticOpenAIChunk)
				chunkBytes := []byte("data: " + string(chunkJSON))

				translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, chunkBytes, &translateParam)
				for _, tc := range translatedChunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
					case <-ctx.Done():
						return
					}
				}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}

		emitTrailingDelta := func(content, reasoning string) bool {
			if content == "" && reasoning == "" {
				return true
			}
			if strings.TrimSpace(content) != "" {
				hasContentDelta = true
			}
			trailingOpenAIChunk := openaiChunk{
				ID:      chatID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []openaiChoice{
					{
						Index: 0,
						Delta: openaiDelta{
							Content:          content,
							ReasoningContent: reasoning,
						},
					},
				},
			}

			trailingJSON, _ := json.Marshal(trailingOpenAIChunk)
			trailingChunkBytes := []byte("data: " + string(trailingJSON))

			translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, trailingChunkBytes, &translateParam)
			for _, tc := range translatedChunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}

		if !emitTrailingDelta(thoughtToolParser.Flush(), "") {
			return
		}
		if !emitTrailingDelta(stripTrailingToolCallResidue(inlineContentToolParser.Flush(), hasToolCall), "") {
			return
		}
		if flushedReasoning := reasoningThinkStripper.Flush(); flushedReasoning != "" {
			parsed := inlineReasoningToolParser.Append(flushedReasoning)
			if len(parsed.ToolCalls) > 0 {
				toolCalls := buildToolCalls(parsed.ToolCalls, "inline", "inline reasoning", &inlineToolIndex)
				if len(toolCalls) > 0 {
					trailingToolChunk := openaiChunk{
						ID:      chatID,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   req.Model,
						Choices: []openaiChoice{
							{
								Index: 0,
								Delta: openaiDelta{ToolCalls: toolCalls},
							},
						},
					}
					trailingToolJSON, _ := json.Marshal(trailingToolChunk)
					trailingToolBytes := []byte("data: " + string(trailingToolJSON))
					translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, trailingToolBytes, &translateParam)
					for _, tc := range translatedChunks {
						select {
						case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
						case <-ctx.Done():
							return
						}
					}
				}
			}
			if parsed.Content != "" {
				if strings.TrimSpace(parsed.Content) != "" {
					hasReasoningDelta = true
					accumulatedReasoning.WriteString(parsed.Content)
				}
				if !emitTrailingDelta("", parsed.Content) {
					return
				}
			}
		}
		trailingReasoning := inlineReasoningToolParser.Flush()
		trailingReasoning = stripTrailingToolCallResidue(trailingReasoning, hasToolCall)
		if strings.TrimSpace(trailingReasoning) != "" {
			hasReasoningDelta = true
		}
		if !emitTrailingDelta("", trailingReasoning) {
			return
		}
		if !hasContentDelta && !hasReasoningDelta && !hasToolCall && pendingHistoryContent != "" {
			if !emitTrailingDelta(pendingHistoryContent, "") {
				return
			}
		}
		if build.IsToolCommit && !hasContentDelta {
			if !emitTrailingDelta("Tool result received.", "") {
				return
			}
		}
		// Fallback: if the model put the entire response in thinking blocks (no text block),
		// promote reasoning to a content delta so clients that only read content get the answer.
		if !hasContentDelta && hasReasoningDelta && !hasToolCall {
			if accumulated := accumulatedReasoning.String(); accumulated != "" {
				if !emitTrailingDelta(accumulated, "") {
					return
				}
			}
		}

		// Stream termination chunk
		if hasToolCall {
			finishReason = "tool_calls"
		} else {
			finishReason = mapUpstreamFinishReasonToOpenAI(finishReason)
		}

		terminalOpenAIChunk := openaiChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []openaiChoice{
				{
					Index:        0,
					Delta:        openaiDelta{},
					FinishReason: finishReason,
				},
			},
		}
		if hasUsage {
			terminalOpenAIChunk.Usage = &accumulatedUsage
		}

		terminalJSON, _ := json.Marshal(terminalOpenAIChunk)
		terminalChunkBytes := []byte("data: " + string(terminalJSON))

		translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, terminalChunkBytes, &translateParam)
		for _, tc := range translatedChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
			case <-ctx.Done():
				return
			}
		}

		// Standard [DONE] block
		doneChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, []byte("data: [DONE]"), &translateParam)
		for _, dc := range doneChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: dc}:
			case <-ctx.Done():
				return
			}
		}
		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func openAIUsageFromResult(result gjson.Result) openaiUsage {
	promptTokens := traeUsageInt(result, "prompt_tokens")
	if promptTokens == 0 {
		promptTokens = traeUsageInt(result, "input_tokens")
	}
	completionTokens := traeUsageInt(result, "completion_tokens")
	if completionTokens == 0 {
		completionTokens = traeUsageInt(result, "output_tokens")
	}
	totalTokens := traeUsageInt(result, "total_tokens")
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}

	cacheReadTokens := traeUsageInt(result, "cache_read_input_tokens")
	cacheCreationTokens := traeUsageInt(result, "cache_creation_input_tokens")
	reasoningTokens := traeUsageInt(result, "reasoning_tokens")

	usageData := openaiUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
	if cachedTokens := cacheReadTokens + cacheCreationTokens; cachedTokens > 0 {
		usageData.PromptTokensDetails = &openaiPromptDetails{CachedTokens: cachedTokens}
	}
	if reasoningTokens > 0 {
		usageData.CompletionTokensDetails = &openaiCompletionDetails{ReasoningTokens: reasoningTokens}
	}
	return usageData
}

func usageDetailFromOpenAIUsage(usageData openaiUsage) cliproxyusage.Detail {
	detail := cliproxyusage.Detail{
		InputTokens:  usageData.PromptTokens,
		OutputTokens: usageData.CompletionTokens,
		TotalTokens:  usageData.TotalTokens,
	}
	if usageData.PromptTokensDetails != nil {
		detail.CachedTokens = usageData.PromptTokensDetails.CachedTokens
	}
	if usageData.CompletionTokensDetails != nil {
		detail.ReasoningTokens = usageData.CompletionTokensDetails.ReasoningTokens
	}
	return detail
}

func traeUsageInt(result gjson.Result, key string) int64 {
	if val := result.Get(key); val.Exists() {
		return val.Int()
	}
	if val := result.Get(key + "_total"); val.Exists() {
		return val.Int()
	}
	return 0
}

func mapUpstreamFinishReasonToOpenAI(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}
