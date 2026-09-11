package helpers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// LingmaErrorInfo represents a parsed error from Lingma.
type LingmaErrorInfo struct {
	StatusCode int
	Message    string
	Type       string
	Code       string
}

// Error implements the error interface.
func (e LingmaErrorInfo) Error() string {
	return e.Message
}

// StatusCode returns the HTTP status code.
func (e LingmaErrorInfo) GetStatusCode() int {
	return e.StatusCode
}

// ResolveLingmaErrorDetails unwraps the provider error nested in Lingma's
// details field. Lingma may encode details as an object, a JSON string
// containing an object, plain text, or null/malformed.
func ResolveLingmaErrorDetails(outerMessage, outerType, outerCode string, details json.RawMessage) (string, string, string) {
	trimmed := bytes.TrimSpace(details)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return outerMessage, outerType, outerCode
	}

	// 1. Details may be encoded as a JSON string.
	var encoded string
	if err := json.Unmarshal(trimmed, &encoded); err == nil {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" {
			return outerMessage, outerType, outerCode
		}
		// If the string starts with JSON object or array notation, attempt to unpack it.
		if strings.HasPrefix(encoded, "{") || strings.HasPrefix(encoded, "[") {
			if json.Valid([]byte(encoded)) {
				return ResolveLingmaErrorDetails(outerMessage, outerType, outerCode, json.RawMessage(encoded))
			}
			// Malformed JSON inside string: fallback to outer error
			return outerMessage, outerType, outerCode
		}
		// Plain text string: use as actionable message, keep outer type and code
		return encoded, outerType, outerCode
	}

	// 2. Details may be a direct JSON object.
	var detail struct {
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Error   any    `json:"error"`
	}
	if err := json.Unmarshal(trimmed, &detail); err != nil {
		// Malformed JSON object: fallback to outer error
		return outerMessage, outerType, outerCode
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

// ParseLingmaError attempts to parse a Lingma SSE line or JSON response into a LingmaErrorInfo.
// Returns (info, true) if an error was found, or (nil, false) if not an error.
func ParseLingmaError(raw []byte) (*LingmaErrorInfo, bool) {
	data := bytes.TrimSpace(raw)
	if bytes.HasPrefix(data, []byte("event:")) {
		return nil, false
	}
	if bytes.HasPrefix(data, []byte("data:")) {
		data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
	}
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

	resolvedMsg, resolvedType, resolvedCode := ResolveLingmaErrorDetails(outerMsg, outerType, outerCode, detailsRaw)
	if resolvedMsg == "" {
		resolvedMsg = "unknown error from lingma"
	}
	if resolvedType == "" {
		resolvedType = "provider_error"
	}

	statusCode := resolveStatusCode(outerStatusCode, resolvedCode, resolvedType, outerCode)

	return &LingmaErrorInfo{
		StatusCode: statusCode,
		Message:    resolvedMsg,
		Type:       resolvedType,
		Code:       resolvedCode,
	}, true
}

func buildErrorInfoFromErrorNode(root, errNode gjson.Result, outerStatusCode int) (*LingmaErrorInfo, bool) {
	if errNode.Type == gjson.String {
		msg := errNode.String()
		if msg == "" {
			msg = "unknown error from lingma"
		}
		status := outerStatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		return &LingmaErrorInfo{
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

	resolvedMsg, resolvedType, resolvedCode := ResolveLingmaErrorDetails(outerMsg, outerType, outerCode, detailsRaw)
	if resolvedMsg == "" {
		resolvedMsg = "unknown error from lingma"
	}
	if resolvedType == "" {
		resolvedType = "server_error"
	}

	nodeStatus := int(errNode.Get("status").Int())
	if nodeStatus == 0 {
		nodeStatus = outerStatusCode
	}

	status := resolveStatusCode(nodeStatus, resolvedCode, resolvedType, outerCode)

	return &LingmaErrorInfo{
		StatusCode: status,
		Message:    resolvedMsg,
		Type:       resolvedType,
		Code:       resolvedCode,
	}, true
}

func resolveStatusCode(explicitStatus int, resolvedCode, resolvedType, outerCode string) int {
	if explicitStatus >= 400 && explicitStatus < 600 {
		return explicitStatus
	}
	for _, codeStr := range []string{resolvedCode, outerCode} {
		if codeStr != "" {
			if num, err := strconv.Atoi(codeStr); err == nil && num >= 400 && num < 600 {
				return num
			}
		}
	}
	lowerType := strings.ToLower(resolvedType)
	lowerCode := strings.ToLower(resolvedCode)
	if strings.Contains(lowerType, "invalid_request") || strings.Contains(lowerCode, "invalid_request") {
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
