package opencode

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func (p *Plugin) countTokens(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode opencode token count request: %w", err)
	}
	if req.SourceFormat == "" {
		return nil, fmt.Errorf("source format is required")
	}

	from := sdktranslator.Format(req.SourceFormat)
	to := sdktranslator.FormatOpenAI
	translated := sdktranslator.TranslateRequest(from, to, req.Model, req.Payload, false)

	tokenCount := int64((len(translated) + 3) / 4)
	usageJSON := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":0,"total_tokens":%d}`, tokenCount, tokenCount)
	translatedUsage := sdktranslator.TranslateTokenCount(context.Background(), to, from, tokenCount, []byte(usageJSON))

	resp := pluginapi.ExecutorResponse{
		Payload: translatedUsage,
	}
	return pluginruntime.OK(resp)
}

func (p *Plugin) httpRequest(raw []byte) ([]byte, error) {
	return pluginruntime.Failure("not_implemented", "raw HTTP request is not supported for opencode"), nil
}
