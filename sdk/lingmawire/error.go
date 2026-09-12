package lingmawire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// ErrorInfo contains structured information about an error returned by Lingma upstream.
type ErrorInfo struct {
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
	Type       string `json:"type"`
	Code       string `json:"code,omitempty"`
}

// ResolveErrorDetails inspects the details field of a Lingma provider error envelope.
// Details may be:
//  1. A JSON string encoding an inner error object: "{\"error\":{\"code\":...,\"message\":...,\"type\":...}}"
//  2. A raw JSON object: {"error":{...}} or {"code":...,"message":...}
//  3. A plain text string error message
//  4. Null or missing
//  5. Malformed JSON
//
// It returns (resolvedMessage, resolvedType, resolvedCode).
// Inner error values take precedence over outer fields. If inner fields are missing,
// it falls back to the outer fields without losing errors.
func ResolveErrorDetails(outerMessage, outerType, outerCode string, details json.RawMessage) (string, string, string) {
	trimmed := bytes.TrimSpace(details)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return outerMessage, outerType, outerCode
	}

	// 1. Check if details is a JSON string (e.g. "\"{\\\"error\\\":...}\"")
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var unquoted string
		if err := json.Unmarshal(trimmed, &unquoted); err == nil {
			unquoted = strings.TrimSpace(unquoted)
			if unquoted == "" {
				return outerMessage, outerType, outerCode
			}
			// If unquoted string is JSON, parse it
			if gjson.Valid(unquoted) {
				return parseInnerErrorPayload([]byte(unquoted), outerMessage, outerType, outerCode)
			}
			if strings.HasPrefix(unquoted, "{") || strings.HasPrefix(unquoted, "[") {
				// Malformed JSON inside string: fallback to outer error
				return outerMessage, outerType, outerCode
			}
			// Otherwise, treat as plain text error message
			return unquoted, outerType, outerCode
		}
	}

	// 2. Details is already a JSON object or array
	if strings.HasPrefix(string(trimmed), "{") || strings.HasPrefix(string(trimmed), "[") {
		if !gjson.ValidBytes(trimmed) {
			return outerMessage, outerType, outerCode
		}
		return parseInnerErrorPayload(trimmed, outerMessage, outerType, outerCode)
	}
	if gjson.ValidBytes(trimmed) {
		return parseInnerErrorPayload(trimmed, outerMessage, outerType, outerCode)
	}

	// 3. Fallback: details is not valid JSON
	plainText := strings.TrimSpace(string(trimmed))
	if plainText != "" {
		return plainText, outerType, outerCode
	}
	return outerMessage, outerType, outerCode
}

func parseInnerErrorPayload(data []byte, outerMessage, outerType, outerCode string) (string, string, string) {
	var detail struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(data, &detail); err != nil {
		root := gjson.ParseBytes(data)
		if errNode := root.Get("error"); errNode.Exists() {
			msg := errNode.Get("message").String()
			if msg == "" {
				msg = errNode.Get("msg").String()
			}
			if msg == "" && errNode.Type == gjson.String {
				msg = errNode.String()
			}
			t := errNode.Get("type").String()
			c := errNode.Get("code").String()
			return orDefault(msg, outerMessage), orDefault(t, outerType), orDefault(c, outerCode)
		}
		msg := root.Get("message").String()
		if msg == "" {
			msg = root.Get("msg").String()
		}
		t := root.Get("type").String()
		c := root.Get("code").String()
		return orDefault(msg, outerMessage), orDefault(t, outerType), orDefault(c, outerCode)
	}

	if detail.Error != nil {
		switch errVal := detail.Error.(type) {
		case map[string]any:
			innerMsg := stringValue(errVal["message"])
			if innerMsg == "" {
				innerMsg = stringValue(errVal["msg"])
			}
			innerType := stringValue(errVal["type"])
			innerCode := stringifyErrorCode(errVal["code"])
			return orDefault(innerMsg, outerMessage),
				orDefault(innerType, outerType),
				orDefault(innerCode, outerCode)
		case string:
			if strings.TrimSpace(errVal) != "" {
				return strings.TrimSpace(errVal), outerType, outerCode
			}
		}
	}

	msg := detail.Message
	if msg == "" {
		msg = detail.Msg
	}
	return orDefault(msg, outerMessage),
		orDefault(detail.Type, outerType),
		orDefault(stringifyErrorCode(detail.Code), outerCode)
}

// ParseError attempts to parse a Lingma SSE line or JSON response into an ErrorInfo.
// Returns (info, true) if an error was found, or (nil, false) if not an error.
func ParseError(raw []byte) (*ErrorInfo, bool) {
	data := StripSSEPrefix(raw)
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil, false
	}
	if !gjson.ValidBytes(data) {
		return nil, false
	}

	root := gjson.ParseBytes(data)

	// Extract outer envelope status code if present
	outerStatusCode := int(root.Get("statusCodeValue").Int())
	if outerStatusCode == 0 {
		outerStatusCode = parseStatusCodeString(root.Get("statusCode").String())
	}

	// Unroll double-JSON envelope: {"body": "..."}
	if bodyNode := root.Get("body"); bodyNode.Exists() {
		if bodyNode.Type == gjson.String {
			innerStr := bodyNode.String()
			if innerStr == "[DONE]" {
				return nil, false
			}
			if gjson.Valid(innerStr) {
				root = gjson.Parse(innerStr)
			}
		} else if bodyNode.IsObject() {
			root = bodyNode
		}
	}

	// 1. Check for nested error object: {"error": {...}} or {"error": "..."}
	if errNode := root.Get("error"); errNode.Exists() {
		return buildErrorInfoFromErrorNode(root, errNode, outerStatusCode)
	}

	// 2. Check for root-level code / message / type / details error envelope:
	// e.g. {"code":"provider_error","message":"Error in upstream response","type":"provider_error","details":"..."}
	codeNode := root.Get("code")
	msgNode := root.Get("message")
	if !msgNode.Exists() {
		msgNode = root.Get("msg")
	}
	typeNode := root.Get("type")
	detailsNode := root.Get("details")

	isError := false
	if codeNode.Exists() && msgNode.Exists() {
		c := strings.ToLower(codeNode.String())
		t := strings.ToLower(typeNode.String())
		if c == "provider_error" || strings.Contains(c, "error") ||
			t == "provider_error" || strings.Contains(t, "error") ||
			detailsNode.Exists() || outerStatusCode >= 400 {
			isError = true
		}
	} else if outerStatusCode >= 400 && msgNode.Exists() {
		isError = true
	} else if detailsNode.Exists() && (codeNode.Exists() || typeNode.Exists()) {
		isError = true
	}

	if !isError {
		return nil, false
	}

	outerMsg := msgNode.String()
	outerType := typeNode.String()
	outerCode := codeNode.String()

	var detailsRaw json.RawMessage
	if detailsNode.Exists() {
		detailsRaw = json.RawMessage(detailsNode.Raw)
	}

	resolvedMsg, resolvedType, resolvedCode := ResolveErrorDetails(outerMsg, outerType, outerCode, detailsRaw)
	if resolvedMsg == "" {
		resolvedMsg = "unknown error from lingma"
	}
	if resolvedType == "" {
		resolvedType = "provider_error"
	}

	statusCode := resolveStatusCode(outerStatusCode, resolvedCode, resolvedType, outerCode)

	return &ErrorInfo{
		StatusCode: statusCode,
		Message:    resolvedMsg,
		Type:       resolvedType,
		Code:       resolvedCode,
	}, true
}

func buildErrorInfoFromErrorNode(root, errNode gjson.Result, outerStatusCode int) (*ErrorInfo, bool) {
	if errNode.Type == gjson.String {
		msg := errNode.String()
		if msg == "" {
			msg = "unknown error from lingma"
		}
		status := outerStatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		return &ErrorInfo{
			StatusCode: status,
			Message:    msg,
			Type:       "api_error",
		}, true
	}

	outerMsg := errNode.Get("message").String()
	if outerMsg == "" {
		outerMsg = errNode.Get("msg").String()
	}
	outerType := errNode.Get("type").String()
	outerCode := errNode.Get("code").String()

	var detailsRaw json.RawMessage
	if d := errNode.Get("details"); d.Exists() {
		detailsRaw = json.RawMessage(d.Raw)
	} else if d := root.Get("details"); d.Exists() {
		detailsRaw = json.RawMessage(d.Raw)
	}

	resolvedMsg, resolvedType, resolvedCode := ResolveErrorDetails(outerMsg, outerType, outerCode, detailsRaw)
	if resolvedMsg == "" {
		resolvedMsg = "unknown error from lingma"
	}
	if resolvedType == "" {
		resolvedType = "api_error"
	}

	status := outerStatusCode
	if status == 0 {
		status = int(errNode.Get("status").Int())
	}
	if status == 0 {
		status = int(errNode.Get("code").Int())
	}
	statusCode := resolveStatusCode(status, resolvedCode, resolvedType, outerCode)

	return &ErrorInfo{
		StatusCode: statusCode,
		Message:    resolvedMsg,
		Type:       resolvedType,
		Code:       resolvedCode,
	}, true
}

func resolveStatusCode(outerStatus int, resolvedCode, resolvedType, outerCode string) int {
	if outerStatus >= 400 && outerStatus <= 599 {
		return outerStatus
	}

	if codeInt, err := strconv.Atoi(strings.TrimSpace(resolvedCode)); err == nil && codeInt >= 400 && codeInt <= 599 {
		return codeInt
	}
	if codeInt, err := strconv.Atoi(strings.TrimSpace(outerCode)); err == nil && codeInt >= 400 && codeInt <= 599 {
		return codeInt
	}

	lowerCode := strings.ToLower(strings.TrimSpace(resolvedCode))
	lowerType := strings.ToLower(strings.TrimSpace(resolvedType))

	if lowerCode == "invalid_request_error" || lowerType == "invalid_request_error" ||
		strings.Contains(lowerCode, "bad_request") || strings.Contains(lowerType, "bad_request") {
		return http.StatusBadRequest
	}
	if strings.Contains(lowerType, "rate_limit") || strings.Contains(lowerCode, "rate_limit") {
		return http.StatusTooManyRequests
	}
	if strings.Contains(lowerType, "unauthorized") || strings.Contains(lowerCode, "unauthorized") ||
		strings.Contains(lowerType, "authentication") || strings.Contains(lowerCode, "authentication") {
		return http.StatusUnauthorized
	}
	if strings.Contains(lowerType, "forbidden") || strings.Contains(lowerCode, "permission") {
		return http.StatusForbidden
	}
	if strings.Contains(lowerType, "not_found") {
		return http.StatusNotFound
	}
	if strings.Contains(lowerType, "timeout") {
		return http.StatusGatewayTimeout
	}
	for _, marker := range []string{"server", "internal", "overload", "rate", "timeout", "unavailable"} {
		if strings.Contains(lowerType, marker) {
			if marker == "rate" {
				return http.StatusTooManyRequests
			}
			if marker == "timeout" {
				return http.StatusGatewayTimeout
			}
			return http.StatusBadGateway
		}
	}
	return http.StatusBadGateway
}

func parseStatusCodeString(statusStr string) int {
	switch strings.ToUpper(strings.TrimSpace(statusStr)) {
	case "BAD_REQUEST":
		return http.StatusBadRequest
	case "UNAUTHORIZED":
		return http.StatusUnauthorized
	case "FORBIDDEN":
		return http.StatusForbidden
	case "NOT_FOUND":
		return http.StatusNotFound
	case "TOO_MANY_REQUESTS":
		return http.StatusTooManyRequests
	case "INTERNAL_SERVER_ERROR":
		return http.StatusInternalServerError
	case "BAD_GATEWAY":
		return http.StatusBadGateway
	case "SERVICE_UNAVAILABLE":
		return http.StatusServiceUnavailable
	case "GATEWAY_TIMEOUT":
		return http.StatusGatewayTimeout
	default:
		return 0
	}
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stringifyErrorCode(code any) string {
	if code == nil {
		return ""
	}
	return fmt.Sprint(code)
}

func orDefault(val, def string) string {
	if strings.TrimSpace(val) != "" {
		return val
	}
	return def
}
