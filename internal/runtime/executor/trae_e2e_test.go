//go:build e2e

package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// loadTraeTestAuth loads Trae credentials from the local JSON auth file.
func loadTraeTestAuth(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..", "..")

	authFile := strings.TrimSpace(os.Getenv("TRAE_E2E_AUTH_FILE"))
	if authFile != "" && !filepath.IsAbs(authFile) {
		authFile = filepath.Join(repoRoot, authFile)
	}
	if authFile == "" {
		for _, dir := range []string{filepath.Join(repoRoot, "auths"), repoRoot} {
			entries, errRead := os.ReadDir(dir)
			if errRead != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
					continue
				}
				path := filepath.Join(dir, e.Name())
				if isTraeE2EAuthFile(t, path) {
					authFile = path
					break
				}
			}
			if authFile != "" {
				break
			}
		}
	}
	if authFile == "" {
		t.Skip("no Trae auth JSON found; set TRAE_E2E_AUTH_FILE or place a Trae auth JSON in auths/")
	}

	raw, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}

	var authData struct {
		DeviceID  string `json:"device_id"`
		JWTToken  string `json:"jwt_token"`
		MachineID string `json:"machine_id"`
		UserID    string `json:"user_id"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		Workspace string `json:"workspace_path"`
	}
	if err := json.Unmarshal(raw, &authData); err != nil {
		t.Fatalf("parse auth json: %v", err)
	}
	if authData.JWTToken == "" {
		t.Fatal("auth file has empty jwt_token")
	}

	return &cliproxyauth.Auth{
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token":      authData.JWTToken,
			"machine_id":     authData.MachineID,
			"device_id":      authData.DeviceID,
			"workspace_path": authData.Workspace,
		},
	}
}

func isTraeE2EAuthFile(t *testing.T, path string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var authData struct {
		Type     string `json:"type"`
		JWTToken string `json:"jwt_token"`
	}
	if err := json.Unmarshal(raw, &authData); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(authData.Type), "trae") || strings.TrimSpace(authData.JWTToken) != ""
}

// collectStreamContent drains a stream result and returns aggregated content and reasoning.
func collectStreamContent(t *testing.T, result *cliproxyexecutor.StreamResult, sourceFmt sdktranslator.Format) (content, reasoning string, usage *gjson.Result) {
	t.Helper()
	e := NewTraeExecutor(nil)
	req := cliproxyexecutor.Request{Model: "test"}
	opts := cliproxyexecutor.Options{SourceFormat: sourceFmt}

	// Use the same aggregation logic as Execute()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var aggContent, aggReasoning strings.Builder
	var usageResult *gjson.Result
	openaiFormat := sdktranslator.FromString("openai")
	var parseParam any

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		requireOpenAIStreamPayload(t, chunk.Payload)
		_ = e // suppress unused
		chunks := sdktranslator.TranslateStream(ctx, sourceFmt, openaiFormat, req.Model, opts.OriginalRequest, nil, chunk.Payload, &parseParam)
		for _, oc := range chunks {
			dataStr := string(oc)
			if strings.HasPrefix(dataStr, "data:") {
				dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
			}
			if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
				continue
			}
			if c := gjson.Get(dataStr, "choices.0.delta.content"); c.Exists() && c.String() != "" {
				aggContent.WriteString(c.String())
			}
			if r := gjson.Get(dataStr, "choices.0.delta.reasoning_content"); r.Exists() && r.String() != "" {
				aggReasoning.WriteString(r.String())
			}
			if u := gjson.Get(dataStr, "usage"); u.Exists() && u.Get("total_tokens").Int() > 0 {
				result := gjson.Parse(u.Raw)
				usageResult = &result
			}
		}
	}
	return aggContent.String(), aggReasoning.String(), usageResult
}

func TestTraeE2E_V1_RawChat_DeepSeekR1(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	rawRequest := []byte(`{
		"model": "deepseek-R1",
		"messages": [
			{"role": "user", "content": "Reply with exactly: E2E_V1_OK"}
		],
		"stream": true
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek-R1",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	// Directly consume raw stream chunks to see what the executor produces
	var rawChunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		requireOpenAIStreamPayload(t, chunk.Payload)
		rawChunks = append(rawChunks, string(chunk.Payload))
	}

	t.Logf("V1 deepseek-R1 got %d raw stream chunks", len(rawChunks))
	for i, c := range rawChunks {
		if i < 15 {
			t.Logf("  raw[%d]: %s", i, truncate(c, 400))
		}
	}

	// Parse content directly from raw chunks (TranslateStream strips "data:" prefix)
	var aggContent strings.Builder
	for _, c := range rawChunks {
		dataStr := c
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		if val := gjson.Get(dataStr, "choices.0.delta.content"); val.Exists() && val.String() != "" {
			aggContent.WriteString(val.String())
		}
	}

	t.Logf("V1 deepseek-R1 aggregated content from raw chunks: %q", aggContent.String())
	if strings.TrimSpace(aggContent.String()) == "" {
		t.Fatal("V1 deepseek-R1 returned empty content")
	}
}

// TestTraeE2E_V1_DebugRawResponse makes a direct HTTP call to capture the raw SSE format.
func TestTraeE2E_V1_DebugRawResponse(t *testing.T) {
	auth := loadTraeTestAuth(t)
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	// Build a V1 request using the executor's internal builder
	rawRequest := []byte(`{
		"model": "deepseek-R1",
		"messages": [
			{"role": "user", "content": "Reply with exactly: DEBUG_V1_OK"}
		],
		"stream": true
	}`)

	build, err := buildTraeRawChatRequest(traeProtocolV1, "deepseek-R1", rawRequest, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, build.TargetURL, bytes.NewReader(build.RequestBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	setTraeCommonHeaders(httpReq.Header, creds)
	httpReq.Header.Set("X-Ide-Session-Id", build.SessionID)
	httpReq.Header.Set("X-Request-Pin", build.RequestPin)
	httpReq.Header.Set("X-Requested-At", strconv.FormatInt(build.RequestAt, 10))
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	httpReq.Header.Set("get-svc", "1")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	defer httpResp.Body.Close()

	t.Logf("V1 debug response status: %d", httpResp.StatusCode)
	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		t.Fatalf("V1 debug error %d: %s", httpResp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(httpResp.Body)
	lineCount := 0
	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			t.Logf("  EVENT: %s", currentEvent)
			continue
		}

		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "" {
				continue
			}
			lineCount++
			if lineCount <= 30 {
				t.Logf("  DATA[%d] (event=%s): %s", lineCount, currentEvent, truncate(data, 500))
			}
			currentEvent = ""
		}
	}
	t.Logf("V1 debug total data lines: %d", lineCount)
	if lineCount == 0 {
		t.Fatal("V1 debug received 0 data lines")
	}
}

func TestTraeE2E_V2_RawChat_NoThinkingModel(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	rawRequest := []byte(`{
		"model": "no_thinking_model",
		"messages": [
			{"role": "user", "content": "Reply with exactly: E2E_V2_OK"}
		],
		"stream": true
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "no_thinking_model",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	content, _, usage := collectStreamContent(t, result, sdktranslator.FromString("openai"))
	t.Logf("V2 no_thinking_model content: %q", content)
	if usage != nil {
		t.Logf("V2 no_thinking_model usage: total_tokens=%d", usage.Get("total_tokens").Int())
	}

	if strings.TrimSpace(content) == "" {
		t.Fatal("V2 no_thinking_model returned empty content")
	}
}

func TestTraeE2E_V3_NonStreaming(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	rawRequest := []byte(`{
		"model": "glm-5",
		"messages": [
			{"role": "user", "content": "Reply with exactly: E2E_V3_OK"}
		]
	}`)

	resp, err := e.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          false,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	payload := string(resp.Payload)
	t.Logf("V3 glm-5 non-stream payload: %s", truncate(payload, 500))

	content := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	reasoning := gjson.GetBytes(resp.Payload, "choices.0.message.reasoning_content").String()
	t.Logf("V3 glm-5 content: %q", content)
	t.Logf("V3 glm-5 reasoning length: %d", len(reasoning))

	if strings.TrimSpace(content) == "" {
		t.Fatal("V3 glm-5 non-stream returned empty content")
	}
}

func TestTraeE2E_V3_Streaming(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	rawRequest := []byte(`{
		"model": "glm-5",
		"messages": [
			{"role": "user", "content": "Reply with exactly: E2E_V3_STREAM"}
		],
		"stream": true
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var rawChunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		requireOpenAIStreamPayload(t, chunk.Payload)
		rawChunks = append(rawChunks, string(chunk.Payload))
	}

	t.Logf("V3 glm-5 streaming got %d raw chunks", len(rawChunks))
	for i, c := range rawChunks {
		t.Logf("  raw[%d]: %s", i, truncate(c, 300))
	}

	var aggContent, aggReasoning strings.Builder
	for _, c := range rawChunks {
		dataStr := c
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		if val := gjson.Get(dataStr, "choices.0.delta.content"); val.Exists() && val.String() != "" {
			aggContent.WriteString(val.String())
		}
		if val := gjson.Get(dataStr, "choices.0.delta.reasoning_content"); val.Exists() && val.String() != "" {
			aggReasoning.WriteString(val.String())
		}
	}

	t.Logf("V3 glm-5 streaming content: %q", aggContent.String())
	t.Logf("V3 glm-5 streaming reasoning length: %d", len(aggReasoning.String()))

	if strings.TrimSpace(aggContent.String()) == "" {
		t.Fatal("V3 glm-5 streaming returned empty content")
	}
}

func TestTraeE2E_V1_NonStreaming_DeepSeekV3(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	rawRequest := []byte(`{
		"model": "deepseek-V3",
		"messages": [
			{"role": "user", "content": "Reply with exactly: E2E_V1_V3_OK"}
		]
	}`)

	// First test streaming to confirm the executor produces content
	streamResult, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek-V3",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamChunks []string
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		requireOpenAIStreamPayload(t, chunk.Payload)
		streamChunks = append(streamChunks, string(chunk.Payload))
	}
	t.Logf("V1 deepseek-V3 streaming got %d chunks", len(streamChunks))
	for i, c := range streamChunks {
		t.Logf("  chunk[%d]: %s", i, truncate(c, 300))
	}

	// Now test non-streaming
	resp, err := e.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "deepseek-V3",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          false,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	payload := string(resp.Payload)
	t.Logf("V1 deepseek-V3 non-stream payload: %s", truncate(payload, 500))

	content := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	t.Logf("V1 deepseek-V3 content: %q", content)

	if strings.TrimSpace(content) == "" {
		t.Fatal("V1 deepseek-V3 non-stream returned empty content")
	}
}

func TestTraeE2E_V3_DebugRawResponse(t *testing.T) {
	auth := loadTraeTestAuth(t)
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	e := NewTraeExecutor(nil)
	rawRequest := []byte(`{
		"model": "glm-5",
		"messages": [
			{"role": "user", "content": "Reply with exactly: DEBUG_V3_OK"}
		]
	}`)

	build, err := e.buildTraeV3CreateTaskRequest(auth, creds, "glm-5",
		gjson.GetBytes(rawRequest, "messages").Array(),
		cliproxyexecutor.Options{OriginalRequest: rawRequest},
	)
	if err != nil {
		t.Fatalf("build v3 request: %v", err)
	}

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, build.TargetURL, bytes.NewReader(build.RequestBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	setTraeCommonHeaders(httpReq.Header, creds)
	httpReq.Header.Set("X-Ide-Session-Id", build.SessionID)
	httpReq.Header.Set("X-Request-Pin", build.RequestPin)
	httpReq.Header.Set("X-Requested-At", strconv.FormatInt(build.RequestAt, 10))
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	httpClient := &http.Client{Timeout: 60 * time.Second}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	defer httpResp.Body.Close()

	t.Logf("V3 debug response status: %d", httpResp.StatusCode)
	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		t.Fatalf("V3 debug error %d: %s", httpResp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(httpResp.Body)
	lineCount := 0
	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			t.Logf("  EVENT: %s", currentEvent)
			continue
		}

		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "" {
				continue
			}
			lineCount++
			if lineCount <= 30 {
				t.Logf("  DATA[%d] (event=%s): %s", lineCount, currentEvent, truncate(data, 500))
			}
			currentEvent = ""
		}
	}
	t.Logf("V3 debug total data lines: %d", lineCount)
	if lineCount == 0 {
		t.Fatal("V3 debug received 0 data lines")
	}
}

// TestTraeE2E_V3_DebugRawToolCall captures the raw SSE format when a v3 model
// calls a tool. This helps us understand the exact event/field structure for
// v3 tool calls (tool_name vs toolcall_name, toolcall_payload vs arguments, etc.)
func TestTraeE2E_V3_DebugRawToolCall(t *testing.T) {
	auth := loadTraeTestAuth(t)
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	e := NewTraeExecutor(nil)
	// Use a prompt that will trigger a tool call (e.g., asking to search code)
	rawRequest := []byte(`{
		"model": "glm-5",
		"messages": [
			{"role": "user", "content": "Search for the file main.go in the current workspace and tell me what it contains. Use the search_codebase tool."}
		]
	}`)

	build, err := e.buildTraeV3CreateTaskRequest(auth, creds, "glm-5",
		gjson.GetBytes(rawRequest, "messages").Array(),
		cliproxyexecutor.Options{OriginalRequest: rawRequest},
	)
	if err != nil {
		t.Fatalf("build v3 request: %v", err)
	}

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, build.TargetURL, bytes.NewReader(build.RequestBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	setTraeCommonHeaders(httpReq.Header, creds)
	httpReq.Header.Set("X-Ide-Session-Id", build.SessionID)
	httpReq.Header.Set("X-Request-Pin", build.RequestPin)
	httpReq.Header.Set("X-Requested-At", strconv.FormatInt(build.RequestAt, 10))
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	httpClient := &http.Client{Timeout: 120 * time.Second}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	defer httpResp.Body.Close()

	t.Logf("V3 tool call debug response status: %d", httpResp.StatusCode)
	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		t.Fatalf("V3 tool call debug error %d: %s", httpResp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(httpResp.Body)
	lineCount := 0
	var currentEvent string
	var events []string
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			events = append(events, currentEvent)
			t.Logf("  EVENT: %s", currentEvent)
			continue
		}

		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "" {
				continue
			}
			lineCount++
			dataLines = append(dataLines, data)
			// Log all lines for tool call analysis (no truncation limit for key events)
			if currentEvent == "tool_call" || strings.Contains(data, "tool_name") || strings.Contains(data, "toolcall") {
				t.Logf("  DATA[%d] (event=%s) [TOOL]: %s", lineCount, currentEvent, data)
			} else if currentEvent == "thought" || currentEvent == "turn_completion" || currentEvent == "required_context" || currentEvent == "history" || currentEvent == "token_usage" {
				t.Logf("  DATA[%d] (event=%s): %s", lineCount, currentEvent, truncate(data, 1000))
			} else if lineCount <= 30 {
				t.Logf("  DATA[%d] (event=%s): %s", lineCount, currentEvent, truncate(data, 500))
			}
			currentEvent = ""
		}
	}
	t.Logf("V3 tool call debug total data lines: %d", lineCount)
	t.Logf("V3 tool call debug events seen: %v", events)

	// Check if we saw a tool_call event
	hasToolCall := false
	for _, d := range dataLines {
		if gjson.Valid(d) {
			if gjson.Get(d, "tool_name").Exists() || gjson.Get(d, "toolcall_name").Exists() {
				hasToolCall = true
				t.Logf("  TOOL CALL DETECTED: tool_name=%s toolcall_name=%s toolcall_payload=%s toolcall_id=%s arguments=%s",
					gjson.Get(d, "tool_name").String(),
					gjson.Get(d, "toolcall_name").String(),
					truncate(gjson.Get(d, "toolcall_payload").String(), 200),
					gjson.Get(d, "toolcall_id").String(),
					truncate(gjson.Get(d, "arguments").String(), 200),
				)
			}
		}
	}
	t.Logf("V3 tool call detected: %v", hasToolCall)
	if lineCount == 0 {
		t.Fatal("V3 tool call debug received 0 data lines")
	}
}

// TestTraeE2E_V3_ToolCallViaExecutor tests the full v3 tool call flow through
// the executor's ExecuteStream, verifying that tool calls are properly translated
// into OpenAI tool_calls format in the SSE stream.
func TestTraeE2E_V3_ToolCallViaExecutor(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	// Use a prompt that will trigger a tool call
	rawRequest := []byte(`{
		"model": "glm-5",
		"messages": [
			{"role": "user", "content": "Search for the file main.go in the current workspace and tell me what it contains. Use the search_codebase tool."}
		],
		"stream": true
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var rawChunks []string
	var toolCalls []string
	var finishReason string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		requireOpenAIStreamPayload(t, chunk.Payload)
		rawChunks = append(rawChunks, string(chunk.Payload))

		dataStr := string(chunk.Payload)
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		if tc := gjson.Get(dataStr, "choices.0.delta.tool_calls"); tc.Exists() && tc.IsArray() {
			for _, item := range tc.Array() {
				toolCalls = append(toolCalls, item.String())
			}
		}
		if fr := gjson.Get(dataStr, "choices.0.finish_reason"); fr.Exists() && fr.String() != "" {
			finishReason = fr.String()
		}
	}

	t.Logf("V3 tool call via executor got %d raw chunks", len(rawChunks))
	for i, c := range rawChunks {
		t.Logf("  raw[%d]: %s", i, truncate(c, 300))
	}
	t.Logf("V3 tool calls found: %d", len(toolCalls))
	for i, tc := range toolCalls {
		t.Logf("  tool_call[%d]: %s", i, tc)
	}
	t.Logf("V3 finish_reason: %s", finishReason)

	if len(toolCalls) > 0 {
		t.Logf("V3 tool call flow works! Model called a tool and executor translated it to OpenAI format.")
		// Verify the tool call ID is a trae_ base64-encoded state
		firstTC := toolCalls[0]
		tcID := gjson.Get(firstTC, "id").String()
		if !strings.HasPrefix(tcID, "trae_") {
			t.Fatalf("tool call ID should start with 'trae_', got: %q", tcID)
		}
		// Verify we can decode the tool state
		state, err := decodeTraeToolID(tcID)
		if err != nil {
			t.Fatalf("decode tool call ID: %v", err)
		}
		t.Logf("  decoded tool state: session_id=%s conversation_id=%s task_id=%s agent_run_id=%s native_id=%s name=%s",
			state.SessionID, state.ConversationID, state.TaskID, state.AgentRunID, state.NativeID, state.Name)
	} else {
		t.Logf("V3 model did not call a tool in this run (may have answered directly). This is acceptable.")
	}
}

// TestTraeE2E_V3_ToolCommitFlow tests the full v3 tool call → commit → continue flow.
// Step 1: Send a request that triggers a tool call
// Step 2: If a tool call is received, commit a mock result
// Step 3: Verify the model continues after the tool result
func TestTraeE2E_V3_ToolCommitFlow(t *testing.T) {
	auth := loadTraeTestAuth(t)
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	// Step 1: Send request that should trigger a tool call
	rawRequest := []byte(`{
		"model": "glm-5",
		"messages": [
			{"role": "user", "content": "List the files in the current directory. Use the list_dir tool."}
		],
		"stream": true
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Step 1 ExecuteStream error: %v", err)
	}

	var toolCallIDs []string
	var toolCallNames []string
	var toolCallArguments []string
	var finishReason string
	var content strings.Builder

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("Step 1 stream error: %v", chunk.Err)
		}
		dataStr := string(chunk.Payload)
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		if tc := gjson.Get(dataStr, "choices.0.delta.tool_calls"); tc.Exists() && tc.IsArray() {
			for _, item := range tc.Array() {
				toolCallIDs = append(toolCallIDs, item.Get("id").String())
				toolCallNames = append(toolCallNames, item.Get("function.name").String())
				toolCallArguments = append(toolCallArguments, item.Get("function.arguments").String())
			}
		}
		if c := gjson.Get(dataStr, "choices.0.delta.content"); c.Exists() && c.String() != "" {
			content.WriteString(c.String())
		}
		if fr := gjson.Get(dataStr, "choices.0.finish_reason"); fr.Exists() && fr.String() != "" {
			finishReason = fr.String()
		}
	}

	t.Logf("Step 1: tool_calls=%d names=%v finish_reason=%s content_len=%d",
		len(toolCallIDs), toolCallNames, finishReason, content.Len())

	if len(toolCallIDs) == 0 {
		t.Skip("V3 model did not call a tool; cannot test commit flow. Model may have answered directly.")
	}

	// Step 2: Commit a mock tool result
	firstToolID := toolCallIDs[0]
	state, err := decodeTraeToolID(firstToolID)
	if err != nil {
		t.Fatalf("decode tool call ID: %v", err)
	}
	t.Logf("Step 2: decoded state: session=%s conv=%s task=%s agent_run=%s native_id=%s name=%s",
		state.SessionID, state.ConversationID, state.TaskID, state.AgentRunID, state.NativeID, state.Name)

	// Build the commit request using the executor's internal builder
	toolMessages := []gjson.Result{
		gjson.Parse(fmt.Sprintf(`{"role":"tool","tool_call_id":"%s","name":"%s","content":"mock result: found files [main.go, config.yaml, README.md]"}`,
			firstToolID, toolCallNames[0])),
	}

	commitBuild, err := buildTraeToolCommitRequest(creds, toolMessages)
	if err != nil {
		t.Fatalf("build commit request: %v", err)
	}

	t.Logf("Step 2: commit target URL: %s", commitBuild.TargetURL)
	t.Logf("Step 2: commit log body: %s", truncate(string(commitBuild.LogBody), 500))

	commitReq, err := http.NewRequestWithContext(ctx, http.MethodPost, commitBuild.TargetURL, bytes.NewReader(commitBuild.RequestBody))
	if err != nil {
		t.Fatalf("new commit request: %v", err)
	}
	commitReq.Header.Set("Content-Type", "application/json")
	setTraeCommonHeaders(commitReq.Header, creds)
	commitReq.Header.Set("X-Ide-Session-Id", commitBuild.SessionID)
	commitReq.Header.Set("X-Request-Pin", commitBuild.RequestPin)
	commitReq.Header.Set("X-Requested-At", strconv.FormatInt(commitBuild.RequestAt, 10))
	commitReq.Header.Set("Accept", "text/event-stream")
	commitReq.Header.Set("Cache-Control", "no-cache")

	commitClient := &http.Client{Timeout: 120 * time.Second}
	commitResp, err := commitClient.Do(commitReq)
	if err != nil {
		t.Fatalf("commit http do: %v", err)
	}
	defer commitResp.Body.Close()

	t.Logf("Step 2: commit response status: %d", commitResp.StatusCode)
	if commitResp.StatusCode != 200 {
		body, _ := io.ReadAll(commitResp.Body)
		t.Fatalf("Step 2 commit error %d: %s", commitResp.StatusCode, string(body))
	}

	// Step 3: Read the continuation SSE stream
	scanner := bufio.NewScanner(commitResp.Body)
	lineCount := 0
	var currentEvent string
	var step3Content strings.Builder
	var step3Events []string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			step3Events = append(step3Events, currentEvent)
			continue
		}

		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "" {
				continue
			}
			lineCount++
			if lineCount <= 50 {
				t.Logf("  Step3 DATA[%d] (event=%s): %s", lineCount, currentEvent, truncate(data, 500))
			}
			if gjson.Valid(data) {
				// Extract content from various possible SSE formats
				if c := gjson.Get(data, "choices.0.delta.content"); c.Exists() && c.String() != "" {
					step3Content.WriteString(c.String())
				} else if c := gjson.Get(data, "content"); c.Exists() && c.Type == gjson.String {
					step3Content.WriteString(c.String())
				} else if c := gjson.Get(data, "response"); c.Exists() && c.Type == gjson.String {
					step3Content.WriteString(c.String())
				}
				// Also check for agent_event payload
				if payload := gjson.Get(data, "payload"); payload.Exists() {
					payloadStr := payload.String()
					if gjson.Valid(payloadStr) {
						if msg := gjson.Get(payloadStr, "message"); msg.Exists() && msg.Type == gjson.String {
							step3Content.WriteString(msg.String())
						}
					}
				}
			}
			currentEvent = ""
		}
	}

	t.Logf("Step 3: total data lines=%d events=%v content_len=%d",
		lineCount, step3Events, step3Content.Len())
	t.Logf("Step 3: content: %q", truncate(step3Content.String(), 500))

	if lineCount == 0 {
		t.Fatal("Step 3: commit response had 0 data lines — model did not continue after tool result")
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func requireOpenAIStreamPayload(t *testing.T, payload []byte) {
	t.Helper()
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "[DONE]" {
			continue
		}
		if !gjson.Valid(line) {
			t.Fatalf("stream payload is not valid OpenAI JSON data: %q", truncate(line, 300))
		}
	}
}
