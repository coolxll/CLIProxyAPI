package trae

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type traeStreamEmitter func(payload []byte, usage *pluginapi.UsageDetail) error

type traeStreamProcessor struct {
	ctx              context.Context
	host             hostRPC
	req              executorRPCRequest
	build            *traeRequestBuildResult
	targetFormat     sdktranslator.Format
	translateParam   any
	emit             traeStreamEmitter
	chatID           string
	currentEvent     string
	taskID           string
	agentRunID       string
	tcIndex          int
	thoughtToolIndex int
	inlineToolIndex  int
	hasToolCall      bool
	hasContentDelta  bool
	hasReasoning     bool
	finishReason     string
	accumulatedUsage openaiUsage
	hasUsage         bool
	reasoning        strings.Builder
	historyContent   string
	normalizeTool    func(string) string
	toolSignatures   map[string]struct{}
	thoughtParser    traeThoughtToolParser
	contentParser    traeInlineToolCallParser
	reasoningParser  traeInlineToolCallParser
	thinkStripper    deepSeekThinkTagStripper
	queueDone        chan struct{}
	queueDoneOnce    sync.Once
	heartbeatStarted atomic.Bool
	enableHeartbeat  bool
	inQueue          bool
	finished         bool
}

func newTraeStreamProcessor(
	ctx context.Context,
	host hostRPC,
	req executorRPCRequest,
	build *traeRequestBuildResult,
	openaiReq []byte,
	targetFormat sdktranslator.Format,
	enableHeartbeat bool,
	emit traeStreamEmitter,
) *traeStreamProcessor {
	if ctx == nil {
		ctx = context.Background()
	}
	if targetFormat == "" {
		targetFormat = sdktranslator.FormatOpenAI
	}
	return &traeStreamProcessor{
		ctx:             ctx,
		host:            host,
		req:             req,
		build:           build,
		targetFormat:    targetFormat,
		emit:            emit,
		chatID:          fmt.Sprintf("chatcmpl-%s", strings.ReplaceAll(uuid.NewString(), "-", "")),
		taskID:          "unknown",
		agentRunID:      "unknown",
		finishReason:    "stop",
		normalizeTool:   buildTraeToolNameNormalizer(openaiReq, req.OriginalRequest),
		toolSignatures:  make(map[string]struct{}),
		queueDone:       make(chan struct{}),
		enableHeartbeat: enableHeartbeat,
	}
}

func (p *traeStreamProcessor) start() error {
	return p.emitChunk(openaiChunk{
		ID:      p.chatID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   p.req.Model,
		Choices: []openaiChoice{{Index: 0, Delta: openaiDelta{Role: "assistant"}}},
	})
}

func (p *traeStreamProcessor) processLine(line []byte) error {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		p.currentEvent = strings.TrimSpace(string(bytes.TrimPrefix(trimmed, []byte("event:"))))
		return nil
	}
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil
	}

	data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || !gjson.ValidBytes(data) {
		return nil
	}

	event := p.currentEvent
	p.currentEvent = ""
	dataStr := string(data)
	if event == "error" {
		message := firstNonEmpty(
			gjson.Get(dataStr, "message").String(),
			gjson.Get(dataStr, "error").String(),
			dataStr,
		)
		if code := gjson.Get(dataStr, "code").String(); code != "" {
			message = fmt.Sprintf("trae error event %s: %s", code, message)
		} else {
			message = fmt.Sprintf("trae error event: %s", message)
		}
		return traeStatusErr{code: http.StatusBadGateway, msg: message}
	}

	if event == "task_created" {
		if taskID := gjson.Get(dataStr, "task_id").String(); taskID != "" {
			p.taskID = taskID
		}
		if agentRunID := gjson.Get(dataStr, "agent_run_id").String(); agentRunID != "" {
			p.agentRunID = agentRunID
		}
	}
	if event == "request_wait_in_queue" {
		p.inQueue = true
		p.startQueueHeartbeat()
		return nil
	}
	if event == "history" {
		p.captureHistory(data)
		return nil
	}

	content := firstStringField(dataStr,
		"choices.0.delta.content",
		"response",
		"content",
		"text",
	)
	reasoning := firstStringField(dataStr,
		"choices.0.delta.reasoning_content",
		"reasoning_content",
	)

	var toolCalls []openaiToolCall
	if event == "thought" {
		thought := gjson.Get(dataStr, "thought")
		reasoningField := gjson.Get(dataStr, "reasoning_content")
		if thought.Exists() && thought.Type == gjson.String && thought.String() != "" {
			parsed := p.thoughtParser.Append(thought.String())
			content += parsed.Content
			toolCalls = append(toolCalls, p.buildToolCalls(parsed.ToolCalls, "thought", &p.thoughtToolIndex)...)
		} else if reasoningField.Exists() && reasoningField.Type == gjson.String && reasoningField.String() != "" {
			parsed := p.thoughtParser.Append(reasoningField.String())
			toolCalls = append(toolCalls, p.buildToolCalls(parsed.ToolCalls, "thought", &p.thoughtToolIndex)...)
		}
	}

	hasUpstreamUsage := false
	var usageData openaiUsage
	if event == "token_usage" {
		usageData = openAIUsageFromResult(gjson.ParseBytes(data))
		hasUpstreamUsage = true
	} else if usageValue := gjson.Get(dataStr, "usage"); usageValue.Exists() {
		usageData = openAIUsageFromResult(usageValue)
		hasUpstreamUsage = true
	}
	if hasUpstreamUsage {
		p.addUsage(usageData)
		if err := p.emit(nil, usageDetailFromOpenAIUsage(usageData)); err != nil {
			return err
		}
		if event == "token_usage" {
			return nil
		}
	}

	if event == "agent_event" || gjson.Get(dataStr, "event").String() == "agent_event" {
		if payload := gjson.Get(dataStr, "payload"); payload.Exists() {
			payloadString := payload.String()
			if gjson.Valid(payloadString) {
				if message := gjson.Get(payloadString, "message"); message.Exists() && message.Type == gjson.String {
					content += message.String()
				}
			}
		}
	}

	if content != "" {
		parsed := p.contentParser.Append(content)
		content = stripTrailingToolCallResidue(parsed.Content, len(parsed.ToolCalls) > 0)
		toolCalls = append(toolCalls, p.buildToolCalls(parsed.ToolCalls, "inline", &p.inlineToolIndex)...)
	}
	if reasoning != "" {
		reasoning = p.thinkStripper.Append(reasoning)
		parsed := p.reasoningParser.Append(reasoning)
		reasoning = stripTrailingToolCallResidue(parsed.Content, len(parsed.ToolCalls) > 0)
		if strings.TrimSpace(reasoning) == "" {
			reasoning = ""
		}
		toolCalls = append(toolCalls, p.buildToolCalls(parsed.ToolCalls, "inline", &p.inlineToolIndex)...)
	}

	if value := gjson.Get(dataStr, "choices.0.delta.tool_calls"); value.Exists() && value.IsArray() {
		for _, toolCall := range value.Array() {
			name := p.normalizeTool(toolCall.Get("function.name").String())
			arguments := normalizeTraeToolArguments(name, toolCall.Get("function.arguments").String())
			p.hasToolCall = true
			toolCalls = append(toolCalls, openaiToolCall{
				Index: int(toolCall.Get("index").Int()),
				ID:    toolCall.Get("id").String(),
				Type:  toolCall.Get("type").String(),
				Function: openaiFunction{
					Name:      name,
					Arguments: arguments,
				},
			})
		}
	}

	toolName := ""
	toolArguments := ""
	nativeToolCallID := ""
	if event == "tool_call" || gjson.Get(dataStr, "tool_name").Exists() || gjson.Get(dataStr, "toolcall_name").Exists() {
		toolName = firstNonEmpty(
			gjson.Get(dataStr, "tool_name").String(),
			gjson.Get(dataStr, "toolcall_name").String(),
			gjson.Get(dataStr, "name").String(),
		)
		if toolName == "tool_name" || toolName == "toolcall_name" {
			toolName = ""
		}
		toolArguments = firstNonEmpty(
			gjson.Get(dataStr, "arguments").String(),
			gjson.Get(dataStr, "toolcall_payload").String(),
		)
		nativeToolCallID = gjson.Get(dataStr, "toolcall_id").String()
	}
	if value := gjson.Get(dataStr, "finish_reason"); value.Exists() && value.String() != "" {
		p.finishReason = value.String()
	}

	if p.inQueue && (content != "" || reasoning != "" || toolName != "" || len(toolCalls) > 0) {
		p.inQueue = false
		p.closeQueueHeartbeat()
	}
	if content == "" && reasoning == "" && toolName == "" && len(toolCalls) == 0 {
		return nil
	}

	if strings.TrimSpace(content) != "" {
		p.hasContentDelta = true
	}
	if strings.TrimSpace(reasoning) != "" {
		p.hasReasoning = true
		p.reasoning.WriteString(reasoning)
	}
	delta := openaiDelta{
		Content:          content,
		ReasoningContent: reasoning,
		ToolCalls:        toolCalls,
	}

	toolName = p.normalizeTool(toolName)
	toolArguments = normalizeTraeToolArguments(toolName, toolArguments)
	if toolName != "" && !p.shouldEmitToolCall(toolName, toolArguments) {
		toolName = ""
	}
	if toolName != "" {
		p.hasToolCall = true
		state := traeToolState{
			SessionID:      p.build.SessionID,
			ConversationID: p.build.ConversationID,
			TaskID:         p.taskID,
			AgentRunID:     p.agentRunID,
			NativeID:       nativeToolCallID,
			Name:           toolName,
		}
		encodedID, err := encodeTraeToolID(state)
		if err != nil {
			p.host.log("error", "encode Trae tool ID", map[string]any{"error": err.Error()})
		} else {
			delta.ToolCalls = append(delta.ToolCalls, openaiToolCall{
				Index: p.tcIndex,
				ID:    encodedID,
				Type:  "function",
				Function: openaiFunction{
					Name:      toolName,
					Arguments: toolArguments,
				},
			})
			p.tcIndex++
		}
	}
	return p.emitDelta(delta)
}

func (p *traeStreamProcessor) finish() error {
	if p.finished {
		return nil
	}
	p.finished = true
	p.closeQueueHeartbeat()

	if err := p.emitTrailingDelta(p.thoughtParser.Flush(), ""); err != nil {
		return err
	}
	if err := p.emitTrailingDelta(stripTrailingToolCallResidue(p.contentParser.Flush(), p.hasToolCall), ""); err != nil {
		return err
	}
	if flushed := p.thinkStripper.Flush(); flushed != "" {
		parsed := p.reasoningParser.Append(flushed)
		toolCalls := p.buildToolCalls(parsed.ToolCalls, "inline", &p.inlineToolIndex)
		if len(toolCalls) > 0 {
			if err := p.emitDelta(openaiDelta{ToolCalls: toolCalls}); err != nil {
				return err
			}
		}
		if parsed.Content != "" {
			if strings.TrimSpace(parsed.Content) != "" {
				p.hasReasoning = true
				p.reasoning.WriteString(parsed.Content)
			}
			if err := p.emitTrailingDelta("", parsed.Content); err != nil {
				return err
			}
		}
	}
	trailingReasoning := stripTrailingToolCallResidue(p.reasoningParser.Flush(), p.hasToolCall)
	if strings.TrimSpace(trailingReasoning) != "" {
		p.hasReasoning = true
		p.reasoning.WriteString(trailingReasoning)
	}
	if err := p.emitTrailingDelta("", trailingReasoning); err != nil {
		return err
	}
	if !p.hasContentDelta && !p.hasReasoning && !p.hasToolCall && p.historyContent != "" {
		if err := p.emitTrailingDelta(p.historyContent, ""); err != nil {
			return err
		}
	}
	if p.build.IsToolCommit && !p.hasContentDelta {
		if err := p.emitTrailingDelta("Tool result received.", ""); err != nil {
			return err
		}
	}
	if !p.hasContentDelta && p.hasReasoning && !p.hasToolCall {
		if accumulated := p.reasoning.String(); accumulated != "" {
			if err := p.emitTrailingDelta(accumulated, ""); err != nil {
				return err
			}
		}
	}

	finishReason := mapUpstreamFinishReasonToOpenAI(p.finishReason)
	if p.hasToolCall {
		finishReason = "tool_calls"
	}
	terminal := openaiChunk{
		ID:      p.chatID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   p.req.Model,
		Choices: []openaiChoice{{
			Index:        0,
			Delta:        openaiDelta{},
			FinishReason: finishReason,
		}},
	}
	if p.hasUsage {
		terminal.Usage = &p.accumulatedUsage
	}
	if err := p.emitChunk(terminal); err != nil {
		return err
	}
	return p.emitTranslated([]byte("data: [DONE]"))
}

func (p *traeStreamProcessor) emitDelta(delta openaiDelta) error {
	return p.emitChunk(openaiChunk{
		ID:      p.chatID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   p.req.Model,
		Choices: []openaiChoice{{Index: 0, Delta: delta}},
	})
}

func (p *traeStreamProcessor) emitTrailingDelta(content, reasoning string) error {
	if content == "" && reasoning == "" {
		return nil
	}
	if strings.TrimSpace(content) != "" {
		p.hasContentDelta = true
	}
	return p.emitDelta(openaiDelta{Content: content, ReasoningContent: reasoning})
}

func (p *traeStreamProcessor) emitChunk(chunk openaiChunk) error {
	raw, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("encode Trae stream chunk: %w", err)
	}
	return p.emitTranslated(append([]byte("data: "), raw...))
}

func (p *traeStreamProcessor) emitTranslated(chunk []byte) error {
	translated := sdktranslator.TranslateStream(
		p.ctx,
		sdktranslator.FormatOpenAI,
		p.targetFormat,
		p.req.Model,
		p.req.OriginalRequest,
		nil,
		chunk,
		&p.translateParam,
	)
	for _, output := range translated {
		if err := p.emit(output, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *traeStreamProcessor) buildToolCalls(parsed []traeThoughtToolCall, nativePrefix string, nextNativeIndex *int) []openaiToolCall {
	toolCalls := make([]openaiToolCall, 0, len(parsed))
	for _, parsedCall := range parsed {
		name := p.normalizeTool(parsedCall.Name)
		arguments := normalizeTraeToolArguments(name, parsedCall.Arguments)
		if !p.shouldEmitToolCall(name, arguments) {
			continue
		}
		nativeID := fmt.Sprintf("%s-%d", nativePrefix, *nextNativeIndex)
		(*nextNativeIndex)++
		state := traeToolState{
			SessionID:      p.build.SessionID,
			ConversationID: p.build.ConversationID,
			TaskID:         p.taskID,
			AgentRunID:     p.agentRunID,
			NativeID:       nativeID,
			Name:           name,
		}
		encodedID, err := encodeTraeToolID(state)
		if err != nil {
			p.host.log("error", "encode Trae parsed tool ID", map[string]any{"error": err.Error()})
			continue
		}
		p.hasToolCall = true
		toolCalls = append(toolCalls, openaiToolCall{
			Index: p.tcIndex,
			ID:    encodedID,
			Type:  "function",
			Function: openaiFunction{
				Name:      name,
				Arguments: arguments,
			},
		})
		p.tcIndex++
	}
	return toolCalls
}

func (p *traeStreamProcessor) shouldEmitToolCall(name, arguments string) bool {
	signature := traeToolSignature(name, arguments)
	if signature == "" {
		return true
	}
	if _, exists := p.toolSignatures[signature]; exists {
		return false
	}
	p.toolSignatures[signature] = struct{}{}
	return true
}

func (p *traeStreamProcessor) addUsage(usage openaiUsage) {
	p.accumulatedUsage.PromptTokens += usage.PromptTokens
	p.accumulatedUsage.CompletionTokens += usage.CompletionTokens
	p.accumulatedUsage.TotalTokens += usage.TotalTokens
	if usage.PromptTokensDetails != nil {
		if p.accumulatedUsage.PromptTokensDetails == nil {
			p.accumulatedUsage.PromptTokensDetails = &openaiPromptDetails{}
		}
		p.accumulatedUsage.PromptTokensDetails.CachedTokens += usage.PromptTokensDetails.CachedTokens
	}
	if usage.CompletionTokensDetails != nil {
		if p.accumulatedUsage.CompletionTokensDetails == nil {
			p.accumulatedUsage.CompletionTokensDetails = &openaiCompletionDetails{}
		}
		p.accumulatedUsage.CompletionTokensDetails.ReasoningTokens += usage.CompletionTokensDetails.ReasoningTokens
	}
	p.hasUsage = true
}

func (p *traeStreamProcessor) captureHistory(data []byte) {
	p.historyContent = ""
	historyMessages := gjson.GetBytes(data, "history_data.messages")
	if !historyMessages.Exists() || historyMessages.Type != gjson.String {
		return
	}
	var messages struct {
		RawMessages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"raw_messages"`
	}
	if err := json.Unmarshal([]byte(historyMessages.String()), &messages); err != nil {
		p.host.log("debug", "decode Trae history event", map[string]any{"error": err.Error()})
		return
	}
	for index := len(messages.RawMessages) - 1; index >= 0; index-- {
		message := messages.RawMessages[index]
		if message.Role != "assistant" {
			continue
		}
		var content strings.Builder
		for _, part := range message.Content {
			if part.Type == "text" && part.Text != "" {
				content.WriteString(part.Text)
			}
		}
		if content.Len() > 0 {
			p.historyContent = content.String()
		}
		return
	}
}

func (p *traeStreamProcessor) startQueueHeartbeat() {
	if !p.enableHeartbeat || !p.heartbeatStarted.CompareAndSwap(false, true) {
		return
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		var translateParam any
		for {
			select {
			case <-ticker.C:
				chunk := openaiChunk{
					ID:      p.chatID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   p.req.Model,
					Choices: []openaiChoice{{Index: 0, Delta: openaiDelta{}}},
				}
				raw, err := json.Marshal(chunk)
				if err != nil {
					return
				}
				translated := sdktranslator.TranslateStream(
					p.ctx,
					sdktranslator.FormatOpenAI,
					p.targetFormat,
					p.req.Model,
					p.req.OriginalRequest,
					nil,
					append([]byte("data: "), raw...),
					&translateParam,
				)
				for _, output := range translated {
					if errEmit := p.emit(output, nil); errEmit != nil {
						return
					}
				}
			case <-p.queueDone:
				return
			case <-p.ctx.Done():
				return
			}
		}
	}()
}

func (p *traeStreamProcessor) closeQueueHeartbeat() {
	p.queueDoneOnce.Do(func() {
		close(p.queueDone)
	})
}

func firstStringField(data string, paths ...string) string {
	for _, path := range paths {
		value := gjson.Get(data, path)
		if value.Exists() && value.Type == gjson.String {
			return value.String()
		}
	}
	return ""
}

func usageDetailFromOpenAIUsage(usage openaiUsage) *pluginapi.UsageDetail {
	detail := &pluginapi.UsageDetail{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if usage.PromptTokensDetails != nil {
		detail.CachedTokens = usage.PromptTokensDetails.CachedTokens
	}
	if usage.CompletionTokensDetails != nil {
		detail.ReasoningTokens = usage.CompletionTokensDetails.ReasoningTokens
	}
	return detail
}
