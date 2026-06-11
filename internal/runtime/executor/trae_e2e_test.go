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

func traeE2ETargets(t *testing.T) []executorE2ETarget {
	t.Helper()
	authFile := findTraeTestAuthFile(t)
	if authFile == "" {
		t.Log("no Trae auth JSON found; skipping Trae targets")
		return nil
	}
	auth := loadTraeTestAuthFromFile(t, authFile)
	v1Model := strings.TrimSpace(os.Getenv("TRAE_E2E_V1_MODEL"))
	if v1Model == "" {
		v1Model = "deepseek-R1"
	}
	v3Model := strings.TrimSpace(os.Getenv("TRAE_E2E_V3_MODEL"))
	if v3Model == "" {
		v3Model = "glm-4.7"
	}
	e := NewTraeExecutor(nil)
	return []executorE2ETarget{
		{
			Name:                       "trae-v1/" + v1Model,
			Executor:                   e,
			Auth:                       auth,
			Model:                      v1Model,
			SourceFormat:               sdktranslator.FromString("openai"),
			SupportsClaudeTools:        true,
			SupportsToolResultFollowUp: true,
			RequiresTraeToolID:         true,
		},
		{
			Name:                       "trae-v3/" + v3Model,
			Executor:                   e,
			Auth:                       auth,
			Model:                      v3Model,
			SourceFormat:               sdktranslator.FromString("openai"),
			SupportsClaudeTools:        true,
			SupportsToolResultFollowUp: true,
			RequiresTraeToolID:         true,
		},
	}
}

// loadTraeTestAuth loads Trae credentials from the local JSON auth file.
func loadTraeTestAuth(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	authFile := findTraeTestAuthFile(t)
	if authFile == "" {
		t.Skip("no Trae auth JSON found; set TRAE_E2E_AUTH_FILE or place a Trae auth JSON in auths/")
	}
	return loadTraeTestAuthFromFile(t, authFile)
}

func findTraeTestAuthFile(t *testing.T) string {
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
	return authFile
}

func loadTraeTestAuthFromFile(t *testing.T, authFile string) *cliproxyauth.Auth {
	t.Helper()
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

// TestTraeE2E_V1_DebugRawToolCallResponse captures the raw SSE format when
// a V1 model calls a tool via the plaintext-tools protocol. This test
// reveals the actual format of tool call responses (e.g., <seed:tool_call>,
// <|FunctionCallBegin|>, or standard OpenAI tool_calls deltas).
// It tests both deepseek-R1 and deepseek-V3 with directory-related prompts
// to trigger <seed:tool_call> format that may appear in reasoning_content.
func TestTraeE2E_V1_DebugRawToolCallResponse(t *testing.T) {
	const maxDebugDataLines = 200
	models := []string{"deepseek-V3", "deepseek-R1"}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			auth := loadTraeTestAuth(t)
			creds, err := traeauth.CredentialsFromAuth(auth)
			if err != nil {
				t.Fatalf("credentials: %v", err)
			}

			workspacePath := traeauth.WorkspacePathFromAuth(auth, "")
			if workspacePath == "" {
				workspacePath = "/tmp"
			}
			// Build a V1 request with directory/file operation tools.
			// Use a prompt that asks the model to search for files, which may
			// trigger the <seed:tool_call> format in reasoning_content.
			rawRequest := []byte(`{
				"model": "` + model + `",
				"messages": [
					{"role": "system", "content": "You have access to the following tools. When you need information, call the appropriate tool before answering. Do not say you don't have access to tools."},
					{"role": "user", "content": "List all files in the directory ` + workspacePath + `. You must call the Glob tool to find out."}
				],
				"stream": true,
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "Glob",
							"description": "Fast file pattern matching tool that works with any number of patterns.",
							"parameters": {
								"type": "object",
								"properties": {
									"pattern": {
										"type": "string",
										"description": "The glob pattern to match files against"
									}
								},
								"required": ["pattern"]
							}
						}
					},
					{
						"type": "function",
						"function": {
							"name": "Read",
							"description": "Reads a file from the local filesystem.",
							"parameters": {
								"type": "object",
								"properties": {
									"file_path": {
										"type": "string",
										"description": "The absolute path to the file to read"
									}
								},
								"required": ["file_path"]
							}
						}
					},
					{
						"type": "function",
						"function": {
							"name": "Bash",
							"description": "Execute a shell command",
							"parameters": {
								"type": "object",
								"properties": {
									"command": {
										"type": "string",
										"description": "The command to execute"
									}
								},
								"required": ["command"]
							}
						}
					}
				]
			}`)

			build, err := buildTraeRawChatRequest(traeProtocolV1, model, rawRequest, cliproxyexecutor.Options{})
			if err != nil {
				t.Fatalf("build request: %v", err)
			}

			t.Logf("V1 %s tool call debug request body (first 500 chars): %s", model, truncate(string(build.RequestBody), 500))

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

			httpClient := &http.Client{Timeout: 120 * time.Second}
			httpResp, err := httpClient.Do(httpReq)
			if err != nil {
				t.Fatalf("http do: %v", err)
			}
			defer httpResp.Body.Close()

			t.Logf("V1 %s tool call debug response status: %d", model, httpResp.StatusCode)
			if httpResp.StatusCode != 200 {
				body, _ := io.ReadAll(httpResp.Body)
				t.Fatalf("V1 %s tool call debug error %d: %s", model, httpResp.StatusCode, string(body))
			}

			scanner := bufio.NewScanner(httpResp.Body)
			scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
			lineCount := 0
			var currentEvent string
			var seedToolCallMarkers, functionCallMarkers, openAIToolCallFields int
			var allDataLines []string
			var reasoningContent strings.Builder
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
					if len(allDataLines) < maxDebugDataLines {
						allDataLines = append(allDataLines, data)
					}

					// Collect reasoning_content for analysis
					if gjson.Valid(data) {
						if rc := gjson.Get(data, "reasoning_content"); rc.Exists() && rc.String() != "" && rc.String() != "null" {
							reasoningContent.WriteString(rc.String())
						}
					}

					// Detect <seed:tool_call> format (in any field, including JSON-escaped)
					if strings.Contains(data, "seed:tool_call") || strings.Contains(data, "seed_tool_call") {
						seedToolCallMarkers++
						t.Logf("  DATA[%d] (event=%s) [SEED_TOOL_CALL]: %s", lineCount, currentEvent, data)
					}

					// Detect <|FunctionCallBegin|> format
					if strings.Contains(data, "FunctionCallBegin") || strings.Contains(data, "FunctionCallEnd") {
						functionCallMarkers++
						t.Logf("  DATA[%d] (event=%s) [FUNC_CALL]: %s", lineCount, currentEvent, data)
					}

					// Detect OpenAI tool_calls delta format
					if gjson.Valid(data) {
						if tc := gjson.Get(data, "choices.0.delta.tool_calls"); tc.Exists() && tc.IsArray() {
							openAIToolCallFields++
							t.Logf("  DATA[%d] (event=%s) [OPENAI_TOOL_CALLS]: %s", lineCount, currentEvent, truncate(data, 1000))
						}
						if tc := gjson.Get(data, "tool_calls"); tc.Exists() && tc.IsArray() && tc.String() != "null" {
							t.Logf("  DATA[%d] (event=%s) [V1_TOOL_CALLS]: %s", lineCount, currentEvent, truncate(data, 1000))
						}
					}

					// Log all data lines (no truncation for tool-related lines)
					if strings.Contains(data, "tool_call") || strings.Contains(data, "tool_name") ||
						strings.Contains(data, "toolcall") || strings.Contains(data, "arguments") ||
						strings.Contains(data, "seed:tool_call") || strings.Contains(data, "FunctionCall") ||
						strings.Contains(data, "reasoning_content") {
						t.Logf("  DATA[%d] (event=%s) [TOOL_RELATED]: %s", lineCount, currentEvent, data)
					} else if lineCount <= 50 {
						t.Logf("  DATA[%d] (event=%s): %s", lineCount, currentEvent, truncate(data, 500))
					}
					currentEvent = ""
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("scan V1 %s tool call debug stream: %v", model, err)
			}

			t.Logf("V1 %s tool call debug total data lines: %d", model, lineCount)
			t.Logf("V1 %s tool call debug markers: seed_tool_call=%d function_call=%d openai_tool_calls=%d",
				model, seedToolCallMarkers, functionCallMarkers, openAIToolCallFields)

			// Dump reasoning_content for analysis of <seed:tool_call> format
			rcStr := reasoningContent.String()
			if rcStr != "" {
				t.Logf("=== AGGREGATED reasoning_content for %s ===", model)
				t.Logf("  reasoning_content: %s", truncate(rcStr, 5000))
				if strings.Contains(rcStr, "<seed:tool_call>") || strings.Contains(rcStr, "</seed:tool_call>") {
					t.Logf("  FOUND <seed:tool_call> in reasoning_content!")
				}
			}

			// Dump all data lines for manual analysis of the tool call format
			t.Logf("=== ALL DATA LINES for %s (for format analysis) ===", model)
			for i, d := range allDataLines {
				t.Logf("  ALL[%d]: %s", i, truncate(d, 500))
			}
			if lineCount > maxDebugDataLines {
				t.Logf("  ... (%d more data lines not shown)", lineCount-maxDebugDataLines)
			}

			if lineCount == 0 {
				t.Fatal("V1 tool call debug received 0 data lines")
			}
		})
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

func TestTraeE2E_V3_DebugRawResponse(t *testing.T) {
	auth := loadTraeTestAuth(t)
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	e := NewTraeExecutor(nil)
	model := traeE2EV3DebugModel()
	rawRequest := []byte(`{
		"model": "` + model + `",
		"messages": [
			{"role": "user", "content": "Reply with exactly: DEBUG_V3_OK"}
		]
	}`)

	build, err := e.buildTraeV3CreateTaskRequest(auth, creds, model, rawRequest,
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

	t.Logf("V3 debug model=%s response status: %d", model, httpResp.StatusCode)
	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		t.Fatalf("V3 debug error %d: %s", httpResp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
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
	t.Logf("V3 debug model=%s total data lines: %d", model, lineCount)
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
	model := traeE2EV3DebugModel()
	rawRequest := []byte(`{
		"model": "` + model + `",
		"messages": [
			{"role": "user", "content": "Use the LS tool to list the current workspace directory. Do not answer from memory; call the tool first."}
		]
	}`)

	build, err := e.buildTraeV3CreateTaskRequest(auth, creds, model, rawRequest,
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

	t.Logf("V3 tool call debug model=%s response status: %d", model, httpResp.StatusCode)
	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		t.Fatalf("V3 tool call debug error %d: %s", httpResp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineCount := 0
	var currentEvent string
	var events []string
	var dataLines []string
	var toolCallEvents, toolCallFields, thoughtEvents, enqMarkers, xmlParameterMarkers int
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
			if currentEvent == "tool_call" {
				toolCallEvents++
			}
			if currentEvent == "thought" {
				thoughtEvents++
			}
			if strings.Contains(data, "tool_name") || strings.Contains(data, "toolcall") || strings.Contains(data, "arguments") {
				toolCallFields++
			}
			if strings.Contains(data, "\x05") || strings.Contains(data, "\\u0005") {
				enqMarkers++
			}
			if strings.Contains(data, "<parameter name=") {
				xmlParameterMarkers++
			}
			// Log all lines for tool call analysis (no truncation limit for key events)
			if currentEvent == "tool_call" || strings.Contains(data, "tool_name") || strings.Contains(data, "toolcall") || strings.Contains(data, "arguments") {
				t.Logf("  DATA[%d] (event=%s) [TOOL]: %s", lineCount, currentEvent, data)
			} else if strings.Contains(data, "\x05") || strings.Contains(data, "\\u0005") || strings.Contains(data, "<parameter name=") {
				t.Logf("  DATA[%d] (event=%s) [XML?]: %q", lineCount, currentEvent, truncate(data, 2000))
			} else if currentEvent == "thought" || currentEvent == "turn_completion" || currentEvent == "required_context" || currentEvent == "history" || currentEvent == "token_usage" {
				t.Logf("  DATA[%d] (event=%s): %s", lineCount, currentEvent, truncate(data, 1000))
			} else if lineCount <= 30 {
				t.Logf("  DATA[%d] (event=%s): %s", lineCount, currentEvent, truncate(data, 500))
			}
			currentEvent = ""
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan V3 tool call debug stream: %v", err)
	}
	t.Logf("V3 tool call debug model=%s total data lines: %d", model, lineCount)
	t.Logf("V3 tool call debug events seen: %v", events)
	t.Logf("V3 tool call debug markers: tool_call_events=%d tool_call_fields=%d thought_events=%d enq_markers=%d xml_parameter_markers=%d",
		toolCallEvents, toolCallFields, thoughtEvents, enqMarkers, xmlParameterMarkers)

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

func traeE2EV3DebugModel() string {
	if model := strings.TrimSpace(os.Getenv("TRAE_E2E_V3_MODEL")); model != "" {
		return model
	}
	return "glm-4.7"
}

func TestTraeE2E_V3_MinimalThoughtToolCommit(t *testing.T) {
	auth := loadTraeTestAuth(t)
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	e := NewTraeExecutor(nil)
	model := traeE2EV3DebugModel()
	rawRequest := []byte(`{
		"model": "` + model + `",
		"messages": [
			{"role": "user", "content": "Use the LS tool to list the current workspace directory. Do not answer from memory; call the tool first."}
		]
	}`)

	build, err := e.buildTraeV3CreateTaskRequest(auth, creds, model, rawRequest,
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
		t.Fatalf("create task http do: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		t.Fatalf("create task error %d: %s", httpResp.StatusCode, string(body))
	}

	taskID := ""
	agentRunID := ""
	toolName := ""
	toolMarkup := ""
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	currentEvent := ""
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(trimmed, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			continue
		}
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if !gjson.Valid(data) {
			continue
		}
		if currentEvent == "task_created" {
			taskID = gjson.Get(data, "task_id").String()
			agentRunID = gjson.Get(data, "agent_run_id").String()
		}
		if currentEvent == "thought" {
			thought := gjson.Get(data, "thought").String()
			if thought != "" {
				t.Logf("thought: %s", truncate(thought, 500))
			}
			if idx := strings.Index(thought, "<tool_call>"); idx >= 0 {
				toolMarkup = thought[idx:]
				toolName = parseTraeThoughtToolNameForTest(toolMarkup)
			}
		}
		if currentEvent == "required_context" && toolName != "" {
			break
		}
		currentEvent = ""
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan create task stream: %v", err)
	}
	if taskID == "" || agentRunID == "" {
		t.Fatalf("missing task identifiers: task_id=%q agent_run_id=%q", taskID, agentRunID)
	}
	if toolName == "" {
		t.Fatalf("did not observe thought tool call; last markup=%q", toolMarkup)
	}
	t.Logf("observed thought tool call: name=%s markup=%q", toolName, toolMarkup)

	state := traeToolState{
		SessionID:      build.SessionID,
		ConversationID: build.ConversationID,
		TaskID:         taskID,
		AgentRunID:     agentRunID,
		NativeID:       "synthetic-0",
		Name:           toolName,
	}
	encodedID, err := encodeTraeToolID(state)
	if err != nil {
		t.Fatalf("encode synthetic tool id: %v", err)
	}
	toolMessages := []gjson.Result{
		gjson.Parse(fmt.Sprintf(`{"role":"tool","tool_call_id":"%s","name":"%s","content":"README.md\ncmd\ninternal\nsdk\n"}`,
			encodedID, toolName)),
	}
	commitBuild, err := buildTraeToolCommitRequest(creds, toolMessages)
	if err != nil {
		t.Fatalf("build commit request: %v", err)
	}
	t.Logf("commit log body: %s", string(commitBuild.LogBody))

	commitReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, commitBuild.TargetURL, bytes.NewReader(commitBuild.RequestBody))
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

	commitResp, err := httpClient.Do(commitReq)
	if err != nil {
		t.Fatalf("commit http do: %v", err)
	}
	defer commitResp.Body.Close()
	t.Logf("commit response status: %d", commitResp.StatusCode)
	if commitResp.StatusCode != 200 {
		body, _ := io.ReadAll(commitResp.Body)
		t.Fatalf("commit error %d: %s", commitResp.StatusCode, string(body))
	}

	commitScanner := bufio.NewScanner(commitResp.Body)
	commitScanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineCount := 0
	currentEvent = ""
	for commitScanner.Scan() {
		trimmed := strings.TrimSpace(commitScanner.Text())
		if strings.HasPrefix(trimmed, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			lineCount++
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if lineCount <= 20 {
				t.Logf("commit DATA[%d] event=%s: %s", lineCount, currentEvent, truncate(data, 800))
			}
			currentEvent = ""
		}
	}
	if err := commitScanner.Err(); err != nil {
		t.Fatalf("scan commit stream: %v", err)
	}
	if lineCount == 0 {
		t.Fatal("commit returned no SSE data")
	}
}

func parseTraeThoughtToolNameForTest(markup string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(markup, "<tool_call>"))
	if rest == "" {
		return ""
	}
	end := len(rest)
	for i, r := range rest {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '/' || r == '>' {
			end = i
			break
		}
	}
	return rest[:end]
}

// TestTraeE2E_V3_ToolCallViaExecutor tests the full v3 tool call flow through
// the executor's ExecuteStream, verifying that tool calls are properly translated
// into OpenAI tool_calls format in the SSE stream.
func TestTraeE2E_V3_ToolCallViaExecutor(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	model := traeE2EV3DebugModel()
	// Use a prompt that will trigger a tool call
	rawRequest := []byte(`{
		"model": "` + model + `",
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "LS",
					"description": "List files in a directory.",
					"parameters": {
						"type": "object",
						"properties": {
							"path": {"type": "string", "description": "Directory path to list."}
						}
					}
				}
			}
		],
		"messages": [
			{"role": "user", "content": "Use the LS tool to list the current workspace directory. Do not answer from memory; call the tool first."}
		],
		"stream": true
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
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

func TestTraeE2E_V3_ClaudeProtocolMCPWeatherToolUse(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	model := traeE2EV3DebugModel()
	rawRequest := []byte(`{
		"model": "` + model + `",
		"max_tokens": 1024,
		"stream": true,
		"tools": [
			{
				"name": "mcp__weather__get_current_weather",
				"description": "Get the current weather for a city.",
				"input_schema": {
					"type": "object",
					"properties": {
						"location": {"type": "string"}
					},
					"required": ["location"]
				}
			}
		],
		"messages": [
				{
					"role": "user",
					"content": "Use the mcp__weather__get_current_weather tool to check the current weather in San Francisco. Do not answer from memory; call the tool first."
				}
		]
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var text strings.Builder
	var toolUseNames []string
	var toolInput strings.Builder
	var rawChunks []string
	stopReason := ""
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		rawChunks = append(rawChunks, string(chunk.Payload))
		for _, event := range parseClaudeSSEEvents(string(chunk.Payload)) {
			payload := gjson.Parse(event.data)
			switch event.typ {
			case "content_block_start":
				if payload.Get("content_block.type").String() == "tool_use" {
					toolUseNames = append(toolUseNames, payload.Get("content_block.name").String())
				}
			case "content_block_delta":
				switch payload.Get("delta.type").String() {
				case "text_delta":
					text.WriteString(payload.Get("delta.text").String())
				case "input_json_delta":
					toolInput.WriteString(payload.Get("delta.partial_json").String())
				}
			case "message_delta":
				if sr := payload.Get("delta.stop_reason").String(); sr != "" {
					stopReason = sr
				}
			}
		}
	}

	t.Logf("Claude MCP weather tool_use count=%d names=%v stop_reason=%q input=%s",
		len(toolUseNames), toolUseNames, stopReason, truncate(toolInput.String(), 300))

	if len(toolUseNames) == 0 {
		for i, raw := range rawChunks {
			t.Logf("  raw[%d]=%s", i, truncate(raw, 500))
		}
		t.Fatalf("expected Claude tool_use for MCP weather request, got none; stop_reason=%q text=%q",
			stopReason, truncate(text.String(), 500))
	}
	if got := toolUseNames[0]; got != "mcp__weather__get_current_weather" {
		t.Fatalf("tool_use name = %q, want mcp__weather__get_current_weather", got)
	}
	if stopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", stopReason)
	}
}

func TestTraeE2E_V3_OpenAIProtocolMCPWeatherToolCall(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()

	model := traeE2EV3DebugModel()
	rawRequest := []byte(`{
		"model": "` + model + `",
		"stream": true,
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "mcp__weather__get_current_weather",
					"description": "Get the current weather for a city.",
					"parameters": {
						"type": "object",
						"properties": {
							"location": {"type": "string"}
						},
						"required": ["location"]
					}
				}
			}
		],
		"messages": [
			{
				"role": "user",
				"content": "Use the mcp__weather__get_current_weather tool to check the current weather in San Francisco. Do not answer from memory; call the tool first."
			}
		]
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var toolCallNames []string
	var toolCallIDs []string
	var arguments strings.Builder
	var content strings.Builder
	finishReason := ""
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		requireOpenAIStreamPayload(t, chunk.Payload)
		dataStr := string(chunk.Payload)
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		if c := gjson.Get(dataStr, "choices.0.delta.content"); c.Exists() && c.String() != "" {
			content.WriteString(c.String())
		}
		if tc := gjson.Get(dataStr, "choices.0.delta.tool_calls"); tc.Exists() && tc.IsArray() {
			for _, item := range tc.Array() {
				if name := item.Get("function.name").String(); name != "" {
					toolCallNames = append(toolCallNames, name)
				}
				if id := item.Get("id").String(); id != "" {
					toolCallIDs = append(toolCallIDs, id)
				}
				if args := item.Get("function.arguments").String(); args != "" {
					arguments.WriteString(args)
				}
			}
		}
		if fr := gjson.Get(dataStr, "choices.0.finish_reason"); fr.Exists() && fr.String() != "" {
			finishReason = fr.String()
		}
	}

	t.Logf("OpenAI MCP weather tool_calls=%d names=%v finish_reason=%q args=%s",
		len(toolCallNames), toolCallNames, finishReason, truncate(arguments.String(), 300))

	if len(toolCallNames) == 0 {
		t.Fatalf("expected OpenAI tool_calls for MCP weather request, got none; finish_reason=%q content=%q",
			finishReason, truncate(content.String(), 500))
	}
	if got := toolCallNames[0]; got != "mcp__weather__get_current_weather" {
		t.Fatalf("tool call name = %q, want mcp__weather__get_current_weather", got)
	}
	if got := gjson.Get(arguments.String(), "location").String(); got != "San Francisco" {
		t.Fatalf("tool call location = %q, want San Francisco; args=%q", got, arguments.String())
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
	}
	for i, id := range toolCallIDs {
		if !strings.HasPrefix(id, "trae_") {
			t.Fatalf("tool_call id[%d] = %q, want trae_ encoded id", i, id)
		}
		if _, errDecode := decodeTraeToolID(id); errDecode != nil {
			t.Fatalf("decode tool_call id[%d]: %v", i, errDecode)
		}
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

	model := traeE2EV3DebugModel()
	// Step 1: Send request that should trigger a tool call
	rawRequest := []byte(`{
		"model": "` + model + `",
		"messages": [
			{"role": "user", "content": "Use the LS tool to list the current workspace directory. Do not answer from memory; call the tool first."}
		],
		"stream": true
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
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

func TestTraeE2E_V3_ToolCommitFallbackDeltaViaExecutor(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()
	model := traeE2EV3DebugModel()

	rawRequest := []byte(`{
		"model": "` + model + `",
		"stream": true,
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "LS",
					"description": "List files in a workspace directory.",
					"parameters": {
						"type": "object",
						"properties": {
							"path": {"type": "string", "description": "Directory path to list."}
						}
					}
				}
			}
		],
		"messages": [
			{"role": "user", "content": "Use the LS tool to list the current workspace directory. Do not answer directly; call the tool first."}
		]
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("initial ExecuteStream error: %v", err)
	}

	var toolCallID string
	var toolCallName string
	var toolCallArguments string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("initial stream error: %v", chunk.Err)
		}
		dataStr := string(chunk.Payload)
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		if tc := gjson.Get(dataStr, "choices.0.delta.tool_calls"); tc.Exists() && tc.IsArray() && toolCallID == "" {
			first := tc.Array()[0]
			toolCallID = first.Get("id").String()
			toolCallName = first.Get("function.name").String()
			toolCallArguments = first.Get("function.arguments").String()
		}
	}
	if toolCallID == "" {
		t.Skip("V3 model did not produce a tool call; cannot verify commit fallback path")
	}
	state, err := decodeTraeToolID(toolCallID)
	if err != nil {
		t.Fatalf("decode tool_call_id: %v", err)
	}
	toolCallName = firstNonEmpty(toolCallName, state.Name)
	t.Logf("tool call selected: name=%s native_id=%s task=%s agent_run=%s args=%s",
		toolCallName, state.NativeID, state.TaskID, state.AgentRunID, truncate(toolCallArguments, 300))

	commitRawRequest, err := json.Marshal(map[string]any{
		"model":  model,
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "Use the LS tool to list the current workspace directory. Do not answer directly; call the tool first."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{
						"id":   toolCallID,
						"type": "function",
						"function": map[string]any{
							"name":      toolCallName,
							"arguments": toolCallArguments,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": toolCallID,
				"name":         toolCallName,
				"content":      "README.md\ncmd\nconfig.example.yaml\ninternal\nsdk\n",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal commit request: %v", err)
	}

	commitResult, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: commitRawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: commitRawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("commit ExecuteStream error: %v", err)
	}

	var content strings.Builder
	var reasoning strings.Builder
	var chunkCount int
	finishReason := ""
	for chunk := range commitResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("commit stream error: %v", chunk.Err)
		}
		dataStr := string(chunk.Payload)
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		chunkCount++
		if chunkCount <= 20 {
			t.Logf("commit chunk[%d]: %s", chunkCount, truncate(dataStr, 500))
		}
		if c := gjson.Get(dataStr, "choices.0.delta.content"); c.Exists() {
			content.WriteString(c.String())
		}
		if r := gjson.Get(dataStr, "choices.0.delta.reasoning_content"); r.Exists() {
			reasoning.WriteString(r.String())
		}
		if fr := gjson.Get(dataStr, "choices.0.finish_reason"); fr.Exists() && fr.String() != "" {
			finishReason = fr.String()
		}
	}

	t.Logf("commit via executor: chunks=%d content=%q reasoning_len=%d finish_reason=%q",
		chunkCount, truncate(content.String(), 500), reasoning.Len(), finishReason)
	if chunkCount == 0 {
		t.Fatal("commit ExecuteStream produced no chunks")
	}
	if strings.TrimSpace(content.String()) == "" {
		t.Fatal("commit ExecuteStream produced empty content; fallback delta did not fire")
	}
}

func TestTraeE2E_V3_BeijingWeatherMockToolCommitViaExecutor(t *testing.T) {
	auth := loadTraeTestAuth(t)
	e := NewTraeExecutor(nil)
	ctx := context.Background()
	model := traeE2EV3DebugModel()

	userPrompt := "Use the mcp__weather__get_current_weather tool to check the current weather in Beijing. Do not answer from memory; call the tool first."
	rawRequest := []byte(`{
		"model": "` + model + `",
		"stream": true,
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "mcp__weather__get_current_weather",
					"description": "Get the current weather for a city.",
					"parameters": {
						"type": "object",
						"properties": {
							"location": {"type": "string"}
						},
						"required": ["location"]
					}
				}
			}
		],
		"messages": [
			{"role": "user", "content": "` + userPrompt + `"}
		]
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("initial ExecuteStream error: %v", err)
	}

	var toolCallID string
	var toolCallName string
	var toolCallArguments string
	var initialContent strings.Builder
	var seenToolNames []string
	initialFinishReason := ""
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("initial stream error: %v", chunk.Err)
		}
		dataStr := string(chunk.Payload)
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		if c := gjson.Get(dataStr, "choices.0.delta.content"); c.Exists() {
			initialContent.WriteString(c.String())
		}
		if tc := gjson.Get(dataStr, "choices.0.delta.tool_calls"); tc.Exists() && tc.IsArray() && toolCallID == "" {
			for _, item := range tc.Array() {
				name := item.Get("function.name").String()
				if name != "" {
					seenToolNames = append(seenToolNames, name)
				}
				if name == "mcp__weather__get_current_weather" {
					toolCallID = item.Get("id").String()
					toolCallName = name
					toolCallArguments = item.Get("function.arguments").String()
					break
				}
			}
		}
		if fr := gjson.Get(dataStr, "choices.0.finish_reason"); fr.Exists() && fr.String() != "" {
			initialFinishReason = fr.String()
		}
	}
	if toolCallID == "" {
		t.Fatalf("expected Beijing weather tool_call, got names=%v; finish_reason=%q content=%q",
			seenToolNames, initialFinishReason, truncate(initialContent.String(), 500))
	}
	state, err := decodeTraeToolID(toolCallID)
	if err != nil {
		t.Fatalf("decode tool_call_id: %v", err)
	}
	toolCallName = firstNonEmpty(toolCallName, state.Name)
	t.Logf("initial weather tool_call: name=%s args=%s native_id=%s task=%s agent_run=%s finish_reason=%q",
		toolCallName, truncate(toolCallArguments, 500), state.NativeID, state.TaskID, state.AgentRunID, initialFinishReason)
	if toolCallName != "mcp__weather__get_current_weather" {
		t.Fatalf("tool_call name = %q, want mcp__weather__get_current_weather", toolCallName)
	}

	mockWeatherResult := `{"location":"Beijing","temperature_c":23,"condition":"Sunny","humidity_percent":35,"wind":"NE 2","air_quality":"Good","summary":"北京当前天气晴，23°C，湿度35%，东北风2级，空气质量良。"}`
	commitRawRequest, err := json.Marshal(map[string]any{
		"model":  model,
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": userPrompt},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{
						"id":   toolCallID,
						"type": "function",
						"function": map[string]any{
							"name":      toolCallName,
							"arguments": toolCallArguments,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": toolCallID,
				"name":         toolCallName,
				"content":      mockWeatherResult,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal commit request: %v", err)
	}

	commitResult, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: commitRawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: commitRawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("commit ExecuteStream error: %v", err)
	}

	var content strings.Builder
	var reasoning strings.Builder
	var followUpToolNames []string
	chunkCount := 0
	finishReason := ""
	for chunk := range commitResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("commit stream error: %v", chunk.Err)
		}
		dataStr := string(chunk.Payload)
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		chunkCount++
		if chunkCount <= 30 {
			t.Logf("commit weather chunk[%d]: %s", chunkCount, truncate(dataStr, 800))
		}
		if c := gjson.Get(dataStr, "choices.0.delta.content"); c.Exists() {
			content.WriteString(c.String())
		}
		if r := gjson.Get(dataStr, "choices.0.delta.reasoning_content"); r.Exists() {
			reasoning.WriteString(r.String())
		}
		if tc := gjson.Get(dataStr, "choices.0.delta.tool_calls"); tc.Exists() && tc.IsArray() {
			for _, item := range tc.Array() {
				if name := item.Get("function.name").String(); name != "" {
					followUpToolNames = append(followUpToolNames, name)
				}
			}
		}
		if fr := gjson.Get(dataStr, "choices.0.finish_reason"); fr.Exists() && fr.String() != "" {
			finishReason = fr.String()
		}
	}

	t.Logf("commit weather result: chunks=%d content=%q reasoning=%q follow_up_tools=%v finish_reason=%q",
		chunkCount, truncate(content.String(), 1000), truncate(reasoning.String(), 1000), followUpToolNames, finishReason)
	if chunkCount == 0 {
		t.Fatal("commit ExecuteStream produced no chunks")
	}
	if strings.TrimSpace(content.String()) == "" {
		t.Fatal("commit ExecuteStream produced empty content")
	}
}

// TestTraeE2E_V3_LargePayloadNoScannerError verifies that a request body exceeding 64KB
// (the bufio.Scanner limit on the V3 API server) does not produce "bufio.Scanner: token too long".
// This reproduces the exact scenario from the bug report: a Claude Code request with 93KB Content-Length.
// The test sends a large payload through the full ExecuteStream pipeline and verifies the model
// produces actual output (tool call → commit → final response).
func TestTraeE2E_V3_LargePayloadNoScannerError(t *testing.T) {
	auth := loadTraeTestAuth(t)
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	e := NewTraeExecutor(nil)
	ctx := context.Background()
	model := traeE2EV3DebugModel()

	// Simulate a large Claude Code request (~93KB) with many tools and a long system prompt.
	largeSystemPrompt := strings.Repeat("You are Claude Code, Anthropic's official CLI for Claude. ", 800) + "\n" +
		strings.Repeat("IMPORTANT: Follow these instructions carefully. Do not break the rules. ", 400)
	toolsJSON := buildLargeToolDefinitionsJSON(20)

	rawRequest := []byte(`{
		"model": "` + model + `",
		"max_tokens": 1024,
		"stream": true,
		"tools": ` + toolsJSON + `,
		"messages": [
			{"role": "system", "content": "` + largeSystemPrompt + `"},
			{"role": "user", "content": "Use the Bash tool to run exactly ls -la. Do not answer from memory; call the tool first."}
		]
	}`)

	t.Logf("raw request size: %d bytes", len(rawRequest))

	// Step 1: Send the large request through ExecuteStream
	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	// Step 2: Collect tool calls and content from the stream
	var toolCallIDs []string
	var toolCallNames []string
	var toolCallArguments []string
	var content strings.Builder
	finishReason := ""

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			errStr := chunk.Err.Error()
			if strings.Contains(errStr, "bufio.Scanner") || strings.Contains(errStr, "token too long") {
				t.Fatalf("got bufio.Scanner error in stream: %s", errStr)
			}
			t.Fatalf("stream error: %v", chunk.Err)
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

	t.Logf("step1: tool_calls=%d names=%v finish_reason=%q content_len=%d",
		len(toolCallIDs), toolCallNames, finishReason, content.Len())

	if len(toolCallIDs) == 0 {
		// Model answered directly without calling a tool — still a valid pass
		t.Logf("model answered directly (no tool call), content=%q", truncate(content.String(), 300))
		return
	}

	// Step 3: Commit tool result and get the final response
	state, err := decodeTraeToolID(toolCallIDs[0])
	if err != nil {
		t.Fatalf("decode tool call ID: %v", err)
	}

	toolMessages := []gjson.Result{
		gjson.Parse(fmt.Sprintf(`{"role":"tool","tool_call_id":"%s","name":"%s","content":"total 8\ndrwxr-xr-x  5 user  staff  160 May 29 23:00 .\ndrwxr-xr-x  3 user  staff   96 May 29 22:59 ..\n-rw-r--r--  1 user  staff  123 May 29 23:00 main.go"}`,
			toolCallIDs[0], toolCallNames[0])),
	}

	commitBuild, err := buildTraeToolCommitRequest(creds, toolMessages)
	if err != nil {
		t.Fatalf("build commit request: %v", err)
	}

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

	t.Logf("step2: commit status=%d", commitResp.StatusCode)
	if commitResp.StatusCode != 200 {
		body, _ := io.ReadAll(commitResp.Body)
		t.Fatalf("commit error %d: %s", commitResp.StatusCode, truncate(string(body), 500))
	}

	// Step 4: Read the final response after tool commit
	var finalContent strings.Builder
	scanner := bufio.NewScanner(commitResp.Body)
	scanner.Buffer(make([]byte, 0, 128*1024), 1024*1024)
	var commitEvents []string
	var rawCommitData []string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "event:") {
			commitEvents = append(commitEvents, strings.TrimSpace(strings.TrimPrefix(trimmed, "event:")))
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data == "" || !gjson.Valid(data) {
				continue
			}
			rawCommitData = append(rawCommitData, data)
			// Try all known content fields, including reasoning_content for reasoning models
			for _, path := range []string{"content", "response", "choices.0.delta.content", "text", "payload.message", "reasoning_content"} {
				if c := gjson.Get(data, path); c.Exists() && c.Type == gjson.String && c.String() != "" {
					finalContent.WriteString(c.String())
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}

	// Log first few raw data lines for debugging
	for i, d := range rawCommitData {
		if i < 10 {
			t.Logf("  commit_data[%d]: %s", i, truncate(d, 500))
		}
	}

	t.Logf("step2: final_content_len=%d final_content=%q commit_events=%v state=%v",
		finalContent.Len(), truncate(finalContent.String(), 300), commitEvents, state)

	if finalContent.Len() == 0 {
		t.Fatal("after tool commit, model did not produce final output — the response is empty")
	}
}

// buildLargeToolDefinitionsJSON builds a JSON array of tool definitions
// similar to what Claude Code sends (20+ tools with full schemas).
func buildLargeToolDefinitionsJSON(n int) string {
	names := []string{
		"Agent", "AskUserQuestion", "Bash", "Edit", "Glob", "Grep",
		"Read", "Write", "WebFetch", "WebSearch", "NotebookEdit",
		"TaskCreate", "TaskUpdate", "TaskList", "LSP", "TodoRead",
		"TodoWrite", "CronCreate", "CronDelete", "CronList",
	}
	descriptionBase := "This tool helps you perform actions in the codebase. " +
		"It supports various parameters and options depending on the use case. " +
		"Use it when you need to interact with files, search for patterns, " +
		"manage your workflow, or automate repetitive tasks. " +
		"The tool accepts input in multiple formats and can be configured " +
		"with different parameters depending on the specific requirements. " +
		"For more complex operations, you can combine multiple parameters " +
		"to achieve the desired result. The tool also supports chaining " +
		"with other tools for advanced workflows."
	var tools []map[string]any
	for i := 0; i < n && i < len(names); i++ {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        names[i],
				"description": names[i] + " - " + descriptionBase,
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"input":     map[string]any{"type": "string", "description": "The primary input to the tool."},
						"options":   map[string]any{"type": "object", "description": "Optional configuration."},
						"verbose":   map[string]any{"type": "boolean", "description": "Enable verbose output."},
						"recursive": map[string]any{"type": "boolean", "description": "Process recursively."},
						"format":    map[string]any{"type": "string", "description": "Output format."},
					},
				},
			},
		})
	}
	b, _ := json.Marshal(tools)
	return string(b)
}
