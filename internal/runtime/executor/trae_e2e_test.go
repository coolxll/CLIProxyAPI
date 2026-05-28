//go:build e2e

package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
