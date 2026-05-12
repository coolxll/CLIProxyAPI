package chat_completions

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		Lingma,
		OpenAI,
		ConvertOpenAIRequestToLingma,
		interfaces.TranslateResponse{
			Stream:    responses.ConvertLingmaResponseToOpenAI,
			NonStream: responses.ConvertLingmaResponseToOpenAINonStream,
		},
	)
}
