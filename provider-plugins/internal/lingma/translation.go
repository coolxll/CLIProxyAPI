package lingma

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	claudetranslator "github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/claude"
	chattranslator "github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/openai/request"
	openaitranslator "github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/openai/response"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	formatOpenAI         = "openai"
	formatClaude         = "claude"
	formatOpenAIResponse = "openai-response"
)

type lingmaThinkingApplier struct{}

func init() {
	thinking.RegisterProvider("lingma", lingmaThinkingApplier{})
}

func (lingmaThinkingApplier) Apply(body []byte, config thinking.ThinkingConfig, _ *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}
	enabled := true
	switch config.Mode {
	case thinking.ModeNone:
		enabled = false
	case thinking.ModeLevel:
		enabled = config.Level != thinking.LevelNone
	case thinking.ModeBudget:
		enabled = config.Budget != 0
	}
	result, errSet := sjson.SetBytes(body, "model_config.is_reasoning", enabled)
	if errSet != nil {
		return body, errSet
	}
	if !enabled {
		result = disableThinking(result)
	}
	return result, nil
}

func normalizeFormat(req pluginapi.ExecutorRequest) string {
	format := strings.ToLower(strings.TrimSpace(req.SourceFormat))
	if format == "" {
		format = strings.ToLower(strings.TrimSpace(req.Format))
	}
	switch format {
	case "chat-completions", "chat_completions", "openai-chat-completions", "openai_chat_completions":
		return formatOpenAI
	case "anthropic":
		return formatClaude
	case "responses", "openai_responses":
		return formatOpenAIResponse
	default:
		return format
	}
}

func translateRequestToLingma(format, model string, body []byte, stream bool) ([]byte, error) {
	switch format {
	case formatOpenAI:
		return chattranslator.ConvertOpenAIRequestToLingma(model, body, stream), nil
	case formatClaude:
		return claudetranslator.ConvertClaudeRequestToLingma(model, body, stream), nil
	case formatOpenAIResponse:
		return nil, newStatusError(501, "Lingma openai-response format is not supported")
	default:
		return nil, newStatusError(400, fmt.Sprintf("Lingma request format %q is not supported", format))
	}
}

func translateNonStreamFromLingma(format, model string, originalRequest, translatedRequest, raw []byte) ([]byte, error) {
	var state any
	switch format {
	case formatOpenAI:
		return openaitranslator.ConvertLingmaResponseToOpenAINonStream(context.Background(), model, originalRequest, translatedRequest, raw, &state), nil
	case formatClaude:
		return claudetranslator.ConvertLingmaResponseToClaudeNonStream(context.Background(), model, originalRequest, translatedRequest, raw, &state), nil
	default:
		return nil, newStatusError(400, fmt.Sprintf("Lingma response format %q is not supported", format))
	}
}

func translateStreamFromLingma(format, model string, originalRequest, translatedRequest, raw []byte, state *any) ([][]byte, error) {
	switch format {
	case formatOpenAI:
		return openaitranslator.ConvertLingmaResponseToOpenAI(context.Background(), model, originalRequest, translatedRequest, raw, state), nil
	case formatClaude:
		return claudetranslator.ConvertLingmaResponseToClaude(context.Background(), model, originalRequest, translatedRequest, raw, state), nil
	default:
		return nil, newStatusError(400, fmt.Sprintf("Lingma response format %q is not supported", format))
	}
}

func applyRequestThinking(body []byte, req pluginapi.ExecutorRequest, format string) []byte {
	result, errApply := thinking.ApplyThinking(body, req.Model, format, "lingma", "lingma")
	if errApply == nil {
		body = result
	}
	return preserveClaudeCodeThinking(body, req.Payload, format)
}

func preserveClaudeCodeThinking(body, source []byte, sourceFormat string) []byte {
	if !strings.EqualFold(strings.TrimSpace(sourceFormat), formatClaude) ||
		len(body) == 0 || !gjson.ValidBytes(body) || len(source) == 0 || !gjson.ValidBytes(source) {
		return body
	}
	enabled, ok := claudeCodeThinkingEnabled(source)
	if !ok {
		return body
	}
	result, errSet := sjson.SetBytes(body, "model_config.is_reasoning", enabled)
	if errSet != nil {
		return body
	}
	return result
}

func claudeCodeThinkingEnabled(source []byte) (bool, bool) {
	if effort := gjson.GetBytes(source, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(effort.String())) {
		case "none", "off", "disabled":
			return false, true
		case "":
		default:
			return true, true
		}
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(source, "thinking.type").String())) {
	case "disabled":
		return false, true
	case "enabled":
		if budget := gjson.GetBytes(source, "thinking.budget_tokens"); budget.Exists() && budget.Int() == 0 {
			return false, true
		}
		return true, true
	case "adaptive", "auto":
		return true, true
	default:
		return false, false
	}
}
