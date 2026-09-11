package helpers

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestResolveLingmaErrorDetails(t *testing.T) {
	tests := []struct {
		name        string
		details     string
		wantMessage string
		wantType    string
		wantCode    string
	}{
		{
			name:        "details is JSON string containing error object",
			details:     `"{\"error\":{\"message\":\"Messages with role 'tool' must be a response to a preceding message with 'tool_calls'\",\"type\":\"invalid_request_error\",\"param\":null,\"code\":\"invalid_request_error\"}}"`,
			wantMessage: "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'",
			wantType:    "invalid_request_error",
			wantCode:    "invalid_request_error",
		},
		{
			name:        "details is JSON object",
			details:     `{"error":{"message":"bad tool history","type":"invalid_request_error","code":40001}}`,
			wantMessage: "bad tool history",
			wantType:    "invalid_request_error",
			wantCode:    "40001",
		},
		{
			name:        "details is plain text JSON string",
			details:     `"provider unavailable"`,
			wantMessage: "provider unavailable",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
		{
			name:        "details missing / null",
			details:     `null`,
			wantMessage: "Error in upstream response",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
		{
			name:        "details empty raw bytes",
			details:     ``,
			wantMessage: "Error in upstream response",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
		{
			name:        "details is malformed JSON object",
			details:     `{not-json}`,
			wantMessage: "Error in upstream response",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
		{
			name:        "details is malformed JSON string",
			details:     `"{malformed-json"`,
			wantMessage: "Error in upstream response",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
		{
			name:        "details object with missing inner fields falls back to outer",
			details:     `{"error":{}}`,
			wantMessage: "Error in upstream response",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMsg, gotType, gotCode := ResolveLingmaErrorDetails(
				"Error in upstream response",
				"provider_error",
				"provider_error",
				json.RawMessage(tt.details),
			)
			if gotMsg != tt.wantMessage {
				t.Errorf("message = %q, want %q", gotMsg, tt.wantMessage)
			}
			if gotType != tt.wantType {
				t.Errorf("type = %q, want %q", gotType, tt.wantType)
			}
			if gotCode != tt.wantCode {
				t.Errorf("code = %q, want %q", gotCode, tt.wantCode)
			}
		})
	}
}

func TestParseLingmaError_RealUpstreamProviderErrorSample(t *testing.T) {
	rawSSE := []byte(`data: {
  "headers": {"Content-Type": ["application/json"]},
  "body": "{\"code\":\"provider_error\",\"message\":\"Error in upstream response\",\"request_id\":\"1869151502422008\",\"type\":\"provider_error\",\"details\":\"{\\\"error\\\":{\\\"message\\\":\\\"Messages with role 'tool' must be a response to a preceding message with 'tool_calls'\\\",\\\"type\\\":\\\"invalid_request_error\\\",\\\"param\\\":null,\\\"code\\\":\\\"invalid_request_error\\\"}}\"}",
  "statusCodeValue": 400,
  "statusCode": "BAD_REQUEST"
}`)

	info, isErr := ParseLingmaError(rawSSE)
	if !isErr || info == nil {
		t.Fatalf("ParseLingmaError returned isErr=%v, info=%v", isErr, info)
	}

	if info.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", info.StatusCode, http.StatusBadRequest)
	}
	wantMsg := "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"
	if info.Message != wantMsg {
		t.Errorf("Message = %q, want %q", info.Message, wantMsg)
	}
	if info.Type != "invalid_request_error" {
		t.Errorf("Type = %q, want %q", info.Type, "invalid_request_error")
	}
	if info.Code != "invalid_request_error" {
		t.Errorf("Code = %q, want %q", info.Code, "invalid_request_error")
	}
	if info.Message == "Error in upstream response" {
		t.Error("Message must NOT be the generic outer 'Error in upstream response'")
	}
}

func TestParseLingmaError_DetailsVariants(t *testing.T) {
	tests := []struct {
		name        string
		bodyJSON    string
		statusVal   int
		statusStr   string
		wantStatus  int
		wantMessage string
		wantType    string
		wantCode    string
	}{
		{
			name:        "details is direct object",
			bodyJSON:    `{"code":"provider_error","message":"Error in upstream response","type":"provider_error","details":{"error":{"message":"bad tool history","type":"invalid_request_error","code":40001}}}`,
			statusVal:   400,
			statusStr:   "BAD_REQUEST",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "bad tool history",
			wantType:    "invalid_request_error",
			wantCode:    "40001",
		},
		{
			name:        "details is plain text",
			bodyJSON:    `{"code":"provider_error","message":"Error in upstream response","type":"provider_error","details":"upstream timeout"}`,
			statusVal:   504,
			statusStr:   "GATEWAY_TIMEOUT",
			wantStatus:  http.StatusGatewayTimeout,
			wantMessage: "upstream timeout",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
		{
			name:        "details is null",
			bodyJSON:    `{"code":"provider_error","message":"Error in upstream response","type":"provider_error","details":null}`,
			statusVal:   500,
			statusStr:   "INTERNAL_SERVER_ERROR",
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "Error in upstream response",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
		{
			name:        "details is malformed string",
			bodyJSON:    `{"code":"provider_error","message":"Error in upstream response","type":"provider_error","details":"{broken-json"}`,
			statusVal:   502,
			statusStr:   "BAD_GATEWAY",
			wantStatus:  http.StatusBadGateway,
			wantMessage: "Error in upstream response",
			wantType:    "provider_error",
			wantCode:    "provider_error",
		},
		{
			name:        "nested error object old style",
			bodyJSON:    `{"error":{"message":"rate limit exceeded","type":"rate_limit_error","code":429}}`,
			statusVal:   429,
			statusStr:   "TOO_MANY_REQUESTS",
			wantStatus:  http.StatusTooManyRequests,
			wantMessage: "rate limit exceeded",
			wantType:    "rate_limit_error",
			wantCode:    "429",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := map[string]any{
				"headers":         map[string]any{"Content-Type": []string{"application/json"}},
				"body":            tt.bodyJSON,
				"statusCodeValue": tt.statusVal,
				"statusCode":      tt.statusStr,
			}
			rawEnvelope, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			sseLine := append([]byte("data: "), rawEnvelope...)

			info, isErr := ParseLingmaError(sseLine)
			if !isErr || info == nil {
				t.Fatalf("ParseLingmaError returned isErr=%v, info=%v", isErr, info)
			}
			if info.StatusCode != tt.wantStatus {
				t.Errorf("StatusCode = %d, want %d", info.StatusCode, tt.wantStatus)
			}
			if info.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", info.Message, tt.wantMessage)
			}
			if info.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", info.Type, tt.wantType)
			}
			if info.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", info.Code, tt.wantCode)
			}
		})
	}
}

func TestParseLingmaError_IgnoresNormalChunks(t *testing.T) {
	normalChunks := [][]byte{
		[]byte(`data: [DONE]`),
		[]byte(`data: {"body":"[DONE]"}`),
		[]byte(`data: {"choices":[{"delta":{"content":"hello"}}],"id":"chatcmpl-1"}`),
		[]byte(`data: {"usage":{"input_tokens":10,"output_tokens":5}}`),
		[]byte(`data: {"totalDuration":100,"serverDuration":50}`),
		[]byte(`: keep-alive`),
		[]byte(``),
	}

	for _, chunk := range normalChunks {
		if info, isErr := ParseLingmaError(chunk); isErr {
			t.Errorf("expected normal chunk not to be an error: %s, got %+v", chunk, info)
		}
	}
}
