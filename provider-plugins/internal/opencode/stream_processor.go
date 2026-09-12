package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type opencodeStreamEmitter func(payload []byte, usage *pluginapi.UsageDetail) error

type opencodeStreamProcessor struct {
	ctx               context.Context
	host              hostRPC
	req               executorRPCRequest
	targetFormat      sdktranslator.Format
	translateParam    any
	emit              opencodeStreamEmitter
	sessionID         string
	chatID            string
	partTypeByID      map[string]string
	hasContentDelta   bool
	hasReasoningDelta bool
	hasToolCall       bool
	streamedContent   strings.Builder
	streamedReasoning strings.Builder
	streamedToolCalls []openaiToolCall
	finishReason      string
	tokens            OpenCodeTokens
	hasTokens         bool
	isFinished        bool
}

func newOpencodeStreamProcessor(
	ctx context.Context,
	host hostRPC,
	req executorRPCRequest,
	sessionID string,
	targetFormat sdktranslator.Format,
	emit opencodeStreamEmitter,
) *opencodeStreamProcessor {
	if ctx == nil {
		ctx = context.Background()
	}
	if targetFormat == "" {
		targetFormat = sdktranslator.FormatOpenAI
	}
	return &opencodeStreamProcessor{
		ctx:          ctx,
		host:         host,
		req:          req,
		targetFormat: targetFormat,
		emit:         emit,
		sessionID:    sessionID,
		chatID:       fmt.Sprintf("chatcmpl-%s", strings.ReplaceAll(uuid.NewString()[:12], "-", "")),
		partTypeByID: make(map[string]string),
		finishReason: "stop",
	}
}

func (p *opencodeStreamProcessor) start() error {
	return p.emitChunk(openaiChunk{
		ID:      p.chatID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   p.req.Model,
		Choices: []openaiChoice{{Index: 0, Delta: openaiDelta{Role: "assistant"}, FinishReason: nil}},
	})
}

func (p *opencodeStreamProcessor) processEvent(event OpenCodeEvent) (bool, error) {
	if p.isFinished {
		return true, nil
	}

	switch event.Type {
	case "message.part.updated":
		var props EventMessagePartUpdatedProps
		if err := json.Unmarshal(event.Properties, &props); err != nil {
			return false, nil
		}
		if props.Part.SessionID != "" && props.Part.SessionID != p.sessionID {
			return false, nil
		}
		if props.Part.ID != "" && props.Part.Type != "" {
			p.partTypeByID[props.Part.ID] = props.Part.Type
		}
		if props.Delta != "" {
			if err := p.applyDelta(props.Part.Type, props.Delta); err != nil {
				return false, err
			}
		}

	case "message.part.delta":
		var props EventMessagePartDeltaProps
		if err := json.Unmarshal(event.Properties, &props); err != nil {
			return false, nil
		}
		if props.SessionID != "" && props.SessionID != p.sessionID {
			return false, nil
		}
		partType := p.partTypeByID[props.PartID]
		if partType == "" {
			partType = "text"
		}
		if props.Delta != "" && (props.Field == "text" || props.Field == "") {
			if err := p.applyDelta(partType, props.Delta); err != nil {
				return false, err
			}
		}

	case "message.updated":
		var props EventMessageUpdatedProps
		if err := json.Unmarshal(event.Properties, &props); err != nil {
			return false, nil
		}
		if props.Info.SessionID != "" && props.Info.SessionID != p.sessionID {
			return false, nil
		}
		if props.Info.Role != "" && props.Info.Role != "assistant" {
			return false, nil
		}
		p.tokens = props.Info.Tokens
		p.hasTokens = true

		if props.Info.Error != nil {
			errMsg := props.Info.Error.Message
			if errMsg == "" {
				errMsg = props.Info.Error.Name
			}
			return false, fmt.Errorf("OpenCode error: %s", errMsg)
		}

		if props.Info.Finish != "" || props.Info.Time.Completed != nil {
			p.finishReason = props.Info.Finish
			if p.finishReason == "" {
				p.finishReason = "stop"
			}
			if err := p.finish(); err != nil {
				return false, err
			}
			return true, nil
		}
	}

	return false, nil
}

func (p *opencodeStreamProcessor) applyDelta(partType, delta string) error {
	if delta == "" {
		return nil
	}

	if partType == "reasoning" {
		p.streamedReasoning.WriteString(delta)
		p.hasReasoningDelta = true
		return p.emitDelta(openaiDelta{ReasoningContent: delta})
	}

	p.streamedContent.WriteString(delta)
	p.hasContentDelta = true
	return p.emitDelta(openaiDelta{Content: delta})
}

func (p *opencodeStreamProcessor) finish() error {
	if p.isFinished {
		return nil
	}
	p.isFinished = true

	// Check if full content contains embedded function call markup
	fullContent := p.streamedContent.String()
	toolCalls, _ := parseExternalToolCalls(fullContent)
	if len(toolCalls) > 0 {
		p.hasToolCall = true
		p.streamedToolCalls = toolCalls
		if err := p.emitDelta(openaiDelta{ToolCalls: toolCalls}); err != nil {
			return err
		}
	}

	finishReason := "stop"
	if p.hasToolCall || p.finishReason == "tool" {
		finishReason = "tool_calls"
	} else if p.finishReason != "" {
		finishReason = mapFinishReason(p.finishReason)
	}

	var usage openaiUsage
	if p.hasTokens {
		usage = openaiUsage{
			PromptTokens:     p.tokens.Input,
			CompletionTokens: p.tokens.Output,
			TotalTokens:      p.tokens.Input + p.tokens.Output,
		}
		if p.tokens.Reasoning > 0 {
			usage.CompletionTokensDetails = &openaiReasoningUsage{
				ReasoningTokens: p.tokens.Reasoning,
			}
		}
	} else {
		promptTokens := (len(p.req.Payload) + 3) / 4
		completionTokens := (p.streamedContent.Len() + 3) / 4
		reasoningTokens := (p.streamedReasoning.Len() + 3) / 4
		usage = openaiUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens + reasoningTokens,
			TotalTokens:      promptTokens + completionTokens + reasoningTokens,
		}
		if reasoningTokens > 0 {
			usage.CompletionTokensDetails = &openaiReasoningUsage{
				ReasoningTokens: reasoningTokens,
			}
		}
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
		Usage: &usage,
	}

	if err := p.emitChunk(terminal); err != nil {
		return err
	}
	return p.emitTranslated([]byte("data: [DONE]"))
}

func (p *opencodeStreamProcessor) emitDelta(delta openaiDelta) error {
	return p.emitChunk(openaiChunk{
		ID:      p.chatID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   p.req.Model,
		Choices: []openaiChoice{{Index: 0, Delta: delta, FinishReason: nil}},
	})
}

func (p *opencodeStreamProcessor) emitChunk(chunk openaiChunk) error {
	raw, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("encode OpenCode stream chunk: %w", err)
	}
	return p.emitTranslated(append([]byte("data: "), raw...))
}

func (p *opencodeStreamProcessor) emitTranslated(chunk []byte) error {
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
	for _, tChunk := range translated {
		if len(tChunk) > 0 {
			if err := p.emit(tChunk, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func mapFinishReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "stop", "end_turn":
		return "stop"
	case "length", "max_tokens":
		return "length"
	case "tool", "tool_calls", "tool_use":
		return "tool_calls"
	case "content_filter":
		return "content_filter"
	default:
		if reason == "" {
			return "stop"
		}
		return reason
	}
}
