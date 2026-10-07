package chat_completions

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/lingma/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Lingma,
		ConvertOpenAIRequestToLingma,
		interfaces.TranslateResponse{
			Stream:    responses.ConvertLingmaResponseToOpenAI,
			NonStream: responses.ConvertLingmaResponseToOpenAINonStream,
		},
	)
}
