package helpers

import (
	"encoding/json"

	"github.com/coolxll/lingma-protocol-go"
)

// LingmaErrorInfo aliases lingmawire.ErrorInfo.
type LingmaErrorInfo = lingmawire.ErrorInfo

// ParseLingmaError attempts to parse a Lingma SSE line or JSON response into a LingmaErrorInfo.
func ParseLingmaError(raw []byte) (*lingmawire.ErrorInfo, bool) {
	return lingmawire.ParseError(raw)
}

// ResolveLingmaErrorDetails unwraps the provider error nested in Lingma's details field.
func ResolveLingmaErrorDetails(outerMessage, outerType, outerCode string, details json.RawMessage) (string, string, string) {
	return lingmawire.ResolveErrorDetails(outerMessage, outerType, outerCode, details)
}
