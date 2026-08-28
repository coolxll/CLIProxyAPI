//go:build manual

package responses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	lingmaauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lingma"
	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	"github.com/tidwall/gjson"
)

const authFile = "../../../../../auths/lingma-local-cache-206119456452928225.json"

func loadTestCreds(t *testing.T) *lingmaauth.Credentials {
	t.Helper()
	data, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	root := gjson.ParseBytes(data)
	return &lingmaauth.Credentials{
		MachineID:          root.Get("machine_id").String(),
		UID:                root.Get("uid").String(),
		OrganizationID:     root.Get("organization_id").String(),
		CosyKey:            root.Get("key").String(),
		EncryptUserInfo:    root.Get("encrypt_user_info").String(),
		UserType:           root.Get("user_type").String(),
		SecurityOAuthToken: root.Get("security_oauth_token").String(),
	}
}

const chatURL = "https://lingma-api.tongyi.aliyun.com/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"

func lingmaChatRequest(t *testing.T, creds *lingmaauth.Credentials, innerBody map[string]any) *http.Response {
	t.Helper()
	innerJSON, _ := json.Marshal(innerBody)
	encoded := lingmaencoding.Encode(innerJSON)

	headers, err := lingmaauth.BuildHeaders(creds, encoded, chatURL)
	if err != nil {
		t.Fatalf("BuildHeaders: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatURL, bytes.NewReader([]byte(encoded)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// TestLingmaAPIUsageFields sends a request to Lingma and prints raw SSE lines
// to check what usage fields are returned.
// Run with: go test -tags manual -run TestLingmaAPIUsageFields -v
func TestLingmaAPIUsageFields(t *testing.T) {
	creds := loadTestCreds(t)

	resp := lingmaChatRequest(t, creds, map[string]any{
		"request_id": "test-usage-001", "request_set_id": "", "chat_record_id": "test-usage-001",
		"stream": true, "image_urls": nil, "is_reply": false, "is_retry": false,
		"session_id": "test-session-001", "code_language": "", "source": 0, "version": "3",
		"chat_prompt": "", "aliyun_user_type": "enterprise_standard",
		"agent_id": "agent_common", "task_id": "question_refine",
		"model_config": map[string]any{
			"key": "org_auto", "display_name": "", "model": "", "format": "",
			"is_vl": false, "is_reasoning": false, "api_key": "", "url": "", "source": "",
			"max_input_tokens": 0, "enable": false, "price_factor": 0,
			"original_price_factor": 0, "is_default": false, "is_new": false,
			"exclude_tags": nil, "tags": nil, "icon": nil, "strategies": nil,
		},
		"messages": []any{map[string]any{"role": "user", "content": "Say hello in one word"}},
		"business": map[string]any{
			"product": "ide", "version": "0.11.0", "type": "chat",
			"id": "test-biz-001", "begin_at": 0, "stage": "start", "name": "api-bridge", "relation": map[string]any{},
		},
		"parameters": map[string]any{"temperature": 0.1},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, body)
	}

	fmt.Println("=== Raw Lingma SSE Lines (Usage Test) ===")
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(nil, 5*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		fmt.Printf("Line %d: %s\n", lineNum, line)

		data := bytes.TrimSpace(line)
		if bytes.HasPrefix(data, []byte("data:")) {
			data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
		}
		if bytes.HasPrefix(data, []byte("event:")) {
			continue
		}

		parsed := gjson.ParseBytes(data)
		if body := parsed.Get("body"); body.Exists() && body.Type == gjson.String {
			inner := body.String()
			if inner != "" && inner != "[DONE]" {
				innerParsed := gjson.Parse(inner)
				if innerParsed.Get("usage").Exists() {
					fmt.Printf("  >>> USAGE FOUND: %s\n", innerParsed.Get("usage").Raw)
				}
				if innerParsed.Get("error").Exists() {
					fmt.Printf("  >>> ERROR FOUND: %s\n", innerParsed.Get("error").Raw)
				}
			}
		}
		if parsed.Get("usage").Exists() {
			fmt.Printf("  >>> USAGE FOUND: %s\n", parsed.Get("usage").Raw)
		}
		if parsed.Get("error").Exists() {
			fmt.Printf("  >>> ERROR FOUND: %s\n", parsed.Get("error").Raw)
		}
	}
	fmt.Printf("=== Total lines: %d ===\n", lineNum)
}

// TestLingmaAPIToolChoice tests if Lingma supports tool_choice.
// Run with: go test -tags manual -run TestLingmaAPIToolChoice -v
func TestLingmaAPIToolChoice(t *testing.T) {
	creds := loadTestCreds(t)

	resp := lingmaChatRequest(t, creds, map[string]any{
		"request_id": "test-tc-001", "request_set_id": "", "chat_record_id": "test-tc-001",
		"stream": true, "image_urls": nil, "is_reply": false, "is_retry": false,
		"session_id": "test-session-002", "code_language": "", "source": 0, "version": "3",
		"chat_prompt": "", "aliyun_user_type": "enterprise_standard",
		"agent_id": "agent_common", "task_id": "question_refine",
		"model_config": map[string]any{
			"key": "org_auto", "display_name": "", "model": "", "format": "",
			"is_vl": false, "is_reasoning": false, "api_key": "", "url": "", "source": "",
			"max_input_tokens": 0, "enable": false, "price_factor": 0,
			"original_price_factor": 0, "is_default": false, "is_new": false,
			"exclude_tags": nil, "tags": nil, "icon": nil, "strategies": nil,
		},
		"messages": []any{map[string]any{"role": "user", "content": "What's the weather in Tokyo?"}},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "get_weather", "description": "Get the weather for a location",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"location": map[string]any{"type": "string"}},
					"required":   []string{"location"},
				},
			},
		}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
		"business": map[string]any{
			"product": "ide", "version": "0.11.0", "type": "chat",
			"id": "test-biz-002", "begin_at": 0, "stage": "start", "name": "api-bridge", "relation": map[string]any{},
		},
		"parameters": map[string]any{"temperature": 0.1},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, body)
	}

	fmt.Println("=== Tool Choice Test - Raw SSE ===")
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(nil, 5*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		fmt.Printf("%s\n", line)

		data := bytes.TrimSpace(line)
		if bytes.HasPrefix(data, []byte("data:")) {
			data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
		}
		parsed := gjson.ParseBytes(data)
		if body := parsed.Get("body"); body.Exists() && body.Type == gjson.String {
			inner := body.String()
			if inner != "" && inner != "[DONE]" {
				innerParsed := gjson.Parse(inner)
				if innerParsed.Get("choices.0.delta.tool_calls").Exists() {
					fmt.Printf("  >>> TOOL_CALL FOUND\n")
				}
			}
		}
	}
}

// TestLingmaAPIVision tests if Lingma supports image input.
// Run with: go test -tags manual -run TestLingmaAPIVision -v
func TestLingmaAPIVision(t *testing.T) {
	creds := loadTestCreds(t)

	// Use a tiny 1x1 red pixel PNG as test image
	testImage := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="

	resp := lingmaChatRequest(t, creds, map[string]any{
		"request_id": "test-vision-001", "request_set_id": "", "chat_record_id": "test-vision-001",
		"stream": true, "image_urls": nil, "is_reply": false, "is_retry": false,
		"session_id": "test-session-003", "code_language": "", "source": 0, "version": "3",
		"chat_prompt": "", "aliyun_user_type": "enterprise_standard",
		"agent_id": "agent_common", "task_id": "question_refine",
		"model_config": map[string]any{
			"key": "org_auto", "display_name": "", "model": "", "format": "",
			"is_vl": true, "is_reasoning": false, "api_key": "", "url": "", "source": "",
			"max_input_tokens": 0, "enable": false, "price_factor": 0,
			"original_price_factor": 0, "is_default": false, "is_new": false,
			"exclude_tags": nil, "tags": nil, "icon": nil, "strategies": nil,
		},
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "What color is this image?"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": testImage}},
			},
		}},
		"business": map[string]any{
			"product": "ide", "version": "0.11.0", "type": "chat",
			"id": "test-biz-003", "begin_at": 0, "stage": "start", "name": "api-bridge", "relation": map[string]any{},
		},
		"parameters": map[string]any{"temperature": 0.1},
	})
	defer resp.Body.Close()

	fmt.Printf("=== Vision Test - Status: %d ===\n", resp.StatusCode)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(nil, 5*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		fmt.Printf("%s\n", line)
	}
}
