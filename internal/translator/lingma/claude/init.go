package claude

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		Lingma,
		ConvertClaudeRequestToLingma,
		interfaces.TranslateResponse{
			Stream:     ConvertLingmaResponseToClaude,
			NonStream:  ConvertLingmaResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
}
