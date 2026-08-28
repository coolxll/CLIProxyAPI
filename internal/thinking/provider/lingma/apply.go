package lingma

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for Lingma models.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Lingma thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("lingma", NewApplier())
}

// Apply applies thinking configuration to Lingma request body.
//
// Lingma-specific behavior:
//   - Target field: model_config.is_reasoning (boolean)
//   - Also respects reasoning_effort if present in the input (passed through to OpenAI-compatible upstreams if applicable,
//     but here we primarily control the native is_reasoning flag).
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	isReasoning := true // Default to true as per user requirement

	switch config.Mode {
	case thinking.ModeNone:
		isReasoning = false
	case thinking.ModeLevel:
		if config.Level == thinking.LevelNone {
			isReasoning = false
		} else {
			isReasoning = true
		}
	case thinking.ModeBudget:
		if config.Budget == 0 {
			isReasoning = false
		} else {
			isReasoning = true
		}
	case thinking.ModeAuto:
		isReasoning = true
	}

	result, _ := sjson.SetBytes(body, "model_config.is_reasoning", isReasoning)
	return result, nil
}
