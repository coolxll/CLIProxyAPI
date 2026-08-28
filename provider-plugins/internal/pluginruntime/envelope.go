package pluginruntime

import (
	"encoding/json"
	"fmt"
)

// Envelope is the schema returned by a dynamic plugin RPC method.
type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a provider-plugin RPC error.
type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// OK wraps a successful method result in the plugin RPC envelope.
func OK(value any) ([]byte, error) {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal plugin result: %w", errMarshal)
	}
	encoded, errEnvelope := json.Marshal(Envelope{OK: true, Result: result})
	if errEnvelope != nil {
		return nil, fmt.Errorf("marshal plugin envelope: %w", errEnvelope)
	}
	return encoded, nil
}

// Failure returns an encoded error envelope. It intentionally never includes
// request or credential payloads.
func Failure(code, message string) []byte {
	encoded, _ := json.Marshal(Envelope{
		OK: false,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	})
	return encoded
}

// FailureFromError preserves optional HTTP status and retryability information
// without exposing request or credential payloads.
func FailureFromError(err error) []byte {
	if err == nil {
		return Failure("plugin_error", "plugin call failed")
	}
	status := 0
	if typed, ok := err.(interface{ StatusCode() int }); ok {
		status = typed.StatusCode()
	}
	retryable := false
	if typed, ok := err.(interface{ Retryable() bool }); ok {
		retryable = typed.Retryable()
	}
	encoded, _ := json.Marshal(Envelope{
		OK: false,
		Error: &Error{
			Code:       "plugin_error",
			Message:    err.Error(),
			Retryable:  retryable,
			HTTPStatus: status,
		},
	})
	return encoded
}
