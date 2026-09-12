package helpers

import (
	"encoding/json"

	"github.com/coolxll/lingma-protocol-go"
)

// LingmaErrorInfo aliases lingmawire.ErrorInfo.
type LingmaErrorInfo = lingmawire.ErrorInfo

// ParseLingmaError attempts to parse a Lingma SSE line or JSON response into an ErrorInfo.
func ParseLingmaError(raw []byte) (*lingmawire.ErrorInfo, bool) {
	return lingmawire.ParseError(raw)
}

// ResolveLingmaErrorDetails resolves details from a Lingma error envelope.
func ResolveLingmaErrorDetails(outerMessage, outerType, outerCode string, details json.RawMessage) (string, string, string) {
	return lingmawire.ResolveErrorDetails(outerMessage, outerType, outerCode, details)
}
