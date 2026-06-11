//go:build e2e

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type executorE2EExecutor interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

type executorE2ETarget struct {
	Name                       string
	Executor                   executorE2EExecutor
	Auth                       *cliproxyauth.Auth
	Model                      string
	SourceFormat               sdktranslator.Format
	Metadata                   map[string]any
	SupportsClaudeTools        bool
	SupportsToolResultFollowUp bool
	RequiresTraeToolID         bool
}

type traeE2EClaudeEvent struct {
	typ  string
	data string
}

type executorE2EClaudeStream struct {
	RawChunks           []string
	Text                string
	TextBeforeFirstTool string
	ToolUseNames        []string
	ToolUseIDs          []string
	ToolInput           string
	StopReason          string
	ChunkCount          int
}

func TestExecutorE2E_CompatibilityMatrix(t *testing.T) {
	targets := append([]executorE2ETarget{}, traeE2ETargets(t)...)
	if target, ok := lingmaE2ETarget(t); ok {
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		t.Skip("no executor e2e targets available")
	}

	for _, target := range targets {
		target := target
		t.Run(target.Name, func(t *testing.T) {
			t.Run("OpenAI_streaming_basic", func(t *testing.T) {
				runExecutorE2EOpenAIStreamingBasic(t, target)
			})
			t.Run("OpenAI_non_streaming_basic", func(t *testing.T) {
				runExecutorE2EOpenAINonStreamingBasic(t, target)
			})
			t.Run("Claude_Read_tool_use", func(t *testing.T) {
				if !target.SupportsClaudeTools {
					t.Skip("target does not support Claude tool use")
				}
				runExecutorE2EClaudeReadToolUse(t, target)
			})
			t.Run("Claude_workspace_inspection_tool_use", func(t *testing.T) {
				if !target.SupportsClaudeTools {
					t.Skip("target does not support Claude tool use")
				}
				runExecutorE2EClaudeWorkspaceInspectionToolUse(t, target)
			})
			t.Run("Claude_tool_result_follow_up", func(t *testing.T) {
				if !target.SupportsToolResultFollowUp {
					t.Skip("target does not support Claude tool_result follow-up")
				}
				runExecutorE2EClaudeToolResultFollowUp(t, target)
			})
		})
	}
}

func runExecutorE2EOpenAIStreamingBasic(t *testing.T, target executorE2ETarget) {
	t.Helper()
	rawRequest := mustMarshalExecutorE2EJSON(t, map[string]any{
		"model":  target.Model,
		"stream": true,
		"messages": []map[string]any{{
			"role":    "user",
			"content": "Reply with one short sentence containing the word E2E_STREAM_OK.",
		}},
	})

	result, err := target.Executor.ExecuteStream(context.Background(), target.Auth, cliproxyexecutor.Request{
		Model:    target.Model,
		Payload:  rawRequest,
		Metadata: target.Metadata,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
		Metadata:        target.Metadata,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	content, reasoning, usage := collectStreamContent(t, result, sdktranslator.FromString("openai"))
	t.Logf("%s OpenAI streaming content=%q reasoning_len=%d", target.Name, content, len(reasoning))
	if usage != nil {
		t.Logf("%s OpenAI streaming usage total_tokens=%d", target.Name, usage.Get("total_tokens").Int())
	}
	if strings.TrimSpace(content) == "" {
		t.Fatalf("%s returned empty streaming content", target.Name)
	}
}

func runExecutorE2EOpenAINonStreamingBasic(t *testing.T, target executorE2ETarget) {
	t.Helper()
	rawRequest := mustMarshalExecutorE2EJSON(t, map[string]any{
		"model":  target.Model,
		"stream": false,
		"messages": []map[string]any{{
			"role":    "user",
			"content": "Reply with one short sentence containing the word E2E_NON_STREAM_OK.",
		}},
	})

	resp, err := target.Executor.Execute(context.Background(), target.Auth, cliproxyexecutor.Request{
		Model:    target.Model,
		Payload:  rawRequest,
		Metadata: target.Metadata,
	}, cliproxyexecutor.Options{
		Stream:          false,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
		Metadata:        target.Metadata,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	content := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	t.Logf("%s OpenAI non-streaming payload=%s", target.Name, truncate(string(resp.Payload), 1000))
	if strings.TrimSpace(content) == "" {
		t.Fatalf("%s returned empty non-streaming assistant content", target.Name)
	}
}

func runExecutorE2EClaudeReadToolUse(t *testing.T, target executorE2ETarget) {
	t.Helper()
	rawRequest := mustMarshalExecutorE2EJSON(t, map[string]any{
		"model":      target.Model,
		"max_tokens": 1024,
		"stream":     true,
		"tools":      []map[string]any{executorE2EClaudeReadTool()},
		"messages": []map[string]any{{
			"role":    "user",
			"content": "Use the Read tool to read exactly /tmp/test.txt. Do not answer from memory; call the tool first.",
		}},
	})

	parsed := executeAndCollectClaudeE2EStream(t, target, rawRequest)
	t.Logf("%s Claude Read tool_use count=%d names=%v stop_reason=%q input=%s text=%q",
		target.Name, len(parsed.ToolUseNames), parsed.ToolUseNames, parsed.StopReason,
		truncate(parsed.ToolInput, 500), truncate(parsed.Text, 500))
	if len(parsed.ToolUseNames) == 0 {
		logExecutorE2ERawChunks(t, parsed.RawChunks)
		t.Fatalf("expected Claude tool_use for Read request; stop_reason=%q text=%q",
			parsed.StopReason, truncate(parsed.Text, 500))
	}
	if got := parsed.ToolUseNames[0]; got != "Read" {
		t.Fatalf("tool_use name = %q, want Read", got)
	}
	if parsed.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", parsed.StopReason)
	}
	validateExecutorE2EToolIDs(t, target, parsed.ToolUseIDs)
	if !gjson.Valid(parsed.ToolInput) {
		t.Fatalf("tool input is not valid JSON: %q", parsed.ToolInput)
	}
	filePath := gjson.Get(parsed.ToolInput, "file_path").String()
	if filePath == "" {
		t.Fatalf("Read input missing file_path: %s", parsed.ToolInput)
	}
	if !strings.Contains(filePath, "/tmp/test.txt") {
		t.Fatalf("Read file_path = %q, want /tmp/test.txt; input=%s", filePath, parsed.ToolInput)
	}
	if gjson.Get(parsed.ToolInput, "file_name").Exists() {
		t.Fatalf("Read input leaked file_name alias; input=%s", parsed.ToolInput)
	}
}

func runExecutorE2EClaudeWorkspaceInspectionToolUse(t *testing.T, target executorE2ETarget) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	rawRequest := mustMarshalExecutorE2EJSON(t, map[string]any{
		"model":      target.Model,
		"max_tokens": 1024,
		"stream":     true,
		"tools": []map[string]any{
			executorE2EClaudeReadTool(),
			executorE2EClaudeBashTool(),
			executorE2EClaudeGlobTool(),
		},
		"messages": []map[string]any{{
			"role":    "user",
			"content": fmt.Sprintf("Current working directory is %s. Inspect the README file using one of the available tools before answering.", repoRoot),
		}},
	})

	parsed := executeAndCollectClaudeE2EStream(t, target, rawRequest)
	t.Logf("%s Claude workspace tool_use count=%d names=%v stop_reason=%q input=%s text=%q",
		target.Name, len(parsed.ToolUseNames), parsed.ToolUseNames, parsed.StopReason,
		truncate(parsed.ToolInput, 500), truncate(parsed.Text, 500))
	if len(parsed.ToolUseNames) == 0 {
		logExecutorE2ERawChunks(t, parsed.RawChunks)
		t.Fatalf("expected Claude tool_use for workspace inspection; stop_reason=%q text=%q",
			parsed.StopReason, truncate(parsed.Text, 500))
	}
	if parsed.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", parsed.StopReason)
	}
	allowedTools := map[string]bool{"Read": true, "Bash": true, "Glob": true}
	if !allowedTools[parsed.ToolUseNames[0]] {
		t.Fatalf("tool_use name = %q, want one of Read, Bash, Glob; input=%s", parsed.ToolUseNames[0], parsed.ToolInput)
	}
	if !gjson.Valid(parsed.ToolInput) {
		t.Fatalf("tool input is not valid JSON: %q", parsed.ToolInput)
	}
	if len(gjson.Parse(parsed.ToolInput).Map()) == 0 {
		t.Fatalf("tool input must not be empty JSON object: %s", parsed.ToolInput)
	}
	validateExecutorE2EToolIDs(t, target, parsed.ToolUseIDs)
}

func runExecutorE2EClaudeToolResultFollowUp(t *testing.T, target executorE2ETarget) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	readmePath := filepath.Join(repoRoot, "README.md")

	firstPrompt := fmt.Sprintf("Use the Read tool to read the file %s before answering. Do not reply from memory; call the tool first.", readmePath)
	rawRequest1 := mustMarshalExecutorE2EJSON(t, map[string]any{
		"model":      target.Model,
		"max_tokens": 1024,
		"stream":     true,
		"tools":      []map[string]any{executorE2EClaudeReadTool()},
		"messages": []map[string]any{{
			"role":    "user",
			"content": firstPrompt,
		}},
	})

	parsed1 := executeAndCollectClaudeE2EStream(t, target, rawRequest1)
	t.Logf("%s Claude tool_result follow-up First Turn: tool_use count=%d names=%v stop_reason=%q input=%s text=%q",
		target.Name, len(parsed1.ToolUseNames), parsed1.ToolUseNames, parsed1.StopReason,
		truncate(parsed1.ToolInput, 500), truncate(parsed1.Text, 500))

	if len(parsed1.ToolUseNames) == 0 {
		logExecutorE2ERawChunks(t, parsed1.RawChunks)
		t.Fatalf("expected Claude tool_use for first turn of follow-up; stop_reason=%q text=%q",
			parsed1.StopReason, truncate(parsed1.Text, 500))
	}
	toolName := parsed1.ToolUseNames[0]
	toolID := parsed1.ToolUseIDs[0]
	if toolName != "Read" {
		t.Fatalf("expected tool_use name = %q, got = %q", "Read", toolName)
	}
	if toolID == "" {
		t.Fatalf("expected non-empty tool_use ID on first turn")
	}
	validateExecutorE2EToolIDs(t, target, parsed1.ToolUseIDs)

	if !gjson.Valid(parsed1.ToolInput) {
		t.Fatalf("tool input is not valid JSON: %q", parsed1.ToolInput)
	}

	var toolResult string
	localBytes, errRead := os.ReadFile(readmePath)
	if errRead == nil {
		if len(localBytes) > 200 {
			toolResult = string(localBytes[:200]) + "\n... truncated ..."
		} else {
			toolResult = string(localBytes)
		}
	} else {
		toolResult = "Mock README content: CLIProxyAPI proxy server providing OpenAI/Gemini/Claude compatible APIs."
	}

	var toolInputJSON any
	if err := json.Unmarshal([]byte(parsed1.ToolInput), &toolInputJSON); err != nil {
		t.Fatalf("failed to unmarshal parsed tool input: %v", err)
	}

	rawRequest2 := mustMarshalExecutorE2EJSON(t, map[string]any{
		"model":      target.Model,
		"max_tokens": 1024,
		"stream":     true,
		"tools":      []map[string]any{executorE2EClaudeReadTool()},
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{{
					"type": "text",
					"text": firstPrompt,
				}},
			},
			{
				"role": "assistant",
				"content": []map[string]any{
					{
						"type":  "tool_use",
						"id":    toolID,
						"name":  "Read",
						"input": toolInputJSON,
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": toolID,
					"content":     toolResult,
					"is_error":    false,
				}},
			},
		},
	})

	result, err := target.Executor.ExecuteStream(context.Background(), target.Auth, cliproxyexecutor.Request{
		Model:    target.Model,
		Payload:  rawRequest2,
		Metadata: target.Metadata,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest2,
		SourceFormat:    sdktranslator.FromString("claude"),
		Metadata:        target.Metadata,
	})
	if err != nil {
		requireNoExecutorE2EFollowUpStatus(t, err)
		t.Fatalf("ExecuteStream setup error after tool_result follow-up: %v", err)
	}

	parsed2 := collectClaudeE2EStream(t, target, result)
	t.Logf("%s Claude tool_result follow-up Second Turn: chunks=%d stop_reason=%q text=%q tools=%v",
		target.Name, parsed2.ChunkCount, parsed2.StopReason, truncate(parsed2.Text, 500), parsed2.ToolUseNames)

	if parsed2.ChunkCount == 0 {
		t.Fatal("expected non-empty stream after tool_result follow-up")
	}

	if strings.TrimSpace(parsed2.Text) == "" && len(parsed2.ToolUseNames) == 0 {
		t.Fatalf("expected text or follow-up tool_use after tool_result; stop_reason=%q chunks=%v",
			parsed2.StopReason, parsed2.RawChunks)
	}
}

func executeAndCollectClaudeE2EStream(t *testing.T, target executorE2ETarget, rawRequest []byte) executorE2EClaudeStream {
	t.Helper()
	result, err := target.Executor.ExecuteStream(context.Background(), target.Auth, cliproxyexecutor.Request{
		Model:    target.Model,
		Payload:  rawRequest,
		Metadata: target.Metadata,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("claude"),
		Metadata:        target.Metadata,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	return collectClaudeE2EStream(t, target, result)
}

func collectClaudeE2EStream(t *testing.T, target executorE2ETarget, result *cliproxyexecutor.StreamResult) executorE2EClaudeStream {
	t.Helper()
	var parsed executorE2EClaudeStream
	var text strings.Builder
	var textBeforeFirstTool strings.Builder
	var toolInput strings.Builder
	seenTool := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			requireNoExecutorE2EFollowUpStatus(t, chunk.Err)
			t.Fatalf("stream error: %v", chunk.Err)
		}
		parsed.ChunkCount++
		raw := string(chunk.Payload)
		parsed.RawChunks = append(parsed.RawChunks, raw)
		requireClaudeE2EStreamHygiene(t, target, raw)
		for _, event := range parseClaudeSSEEvents(raw) {
			if event.data != "[DONE]" && !gjson.Valid(event.data) {
				t.Fatalf("Claude protocol stream emitted invalid JSON event %q: %s", event.typ, event.data)
			}
			if event.data == "[DONE]" {
				continue
			}
			payload := gjson.Parse(event.data)
			switch event.typ {
			case "content_block_start":
				if payload.Get("content_block.type").String() == "tool_use" {
					seenTool = true
					parsed.ToolUseNames = append(parsed.ToolUseNames, payload.Get("content_block.name").String())
					parsed.ToolUseIDs = append(parsed.ToolUseIDs, payload.Get("content_block.id").String())
				}
			case "content_block_delta":
				switch payload.Get("delta.type").String() {
				case "text_delta":
					delta := payload.Get("delta.text").String()
					text.WriteString(delta)
					if !seenTool {
						textBeforeFirstTool.WriteString(delta)
					}
				case "input_json_delta":
					toolInput.WriteString(payload.Get("delta.partial_json").String())
				}
			case "message_delta":
				if sr := payload.Get("delta.stop_reason").String(); sr != "" {
					parsed.StopReason = sr
				}
			}
		}
	}
	parsed.Text = text.String()
	parsed.TextBeforeFirstTool = textBeforeFirstTool.String()
	parsed.ToolInput = toolInput.String()
	if strings.TrimSpace(parsed.TextBeforeFirstTool) == "}" {
		t.Fatalf("Claude stream emitted isolated residue text before tool_use: %q", parsed.TextBeforeFirstTool)
	}
	return parsed
}

// collectStreamContent drains a stream result and returns aggregated content and reasoning.
func collectStreamContent(t *testing.T, result *cliproxyexecutor.StreamResult, sourceFmt sdktranslator.Format) (content, reasoning string, usage *gjson.Result) {
	t.Helper()
	req := cliproxyexecutor.Request{Model: "test"}
	opts := cliproxyexecutor.Options{SourceFormat: sourceFmt}

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

func executorE2EToolUseID(t *testing.T, target executorE2ETarget, toolName string) string {
	t.Helper()
	if !target.RequiresTraeToolID {
		return "toolu_e2e_follow_up"
	}
	id, err := encodeTraeToolID(traeToolState{
		SessionID:      "session-e2e",
		ConversationID: "conversation-e2e",
		TaskID:         "unknown",
		AgentRunID:     "unknown",
		NativeID:       "inline-0",
		Name:           toolName,
	})
	if err != nil {
		t.Fatalf("encode Trae tool id: %v", err)
	}
	return id
}

func validateExecutorE2EToolIDs(t *testing.T, target executorE2ETarget, ids []string) {
	t.Helper()
	if len(ids) == 0 {
		t.Fatalf("expected at least one tool_use id")
	}
	if !target.RequiresTraeToolID {
		return
	}
	for i, id := range ids {
		if !strings.HasPrefix(id, "trae_") {
			t.Fatalf("tool_use id[%d] = %q, want trae_ encoded id", i, id)
		}
		if _, err := decodeTraeToolID(id); err != nil {
			t.Fatalf("decode tool_use id[%d]: %v", i, err)
		}
	}
}

func requireClaudeE2EStreamHygiene(t *testing.T, target executorE2ETarget, raw string) {
	t.Helper()
	if strings.Contains(raw, `"object":"chat.completion.chunk"`) || strings.Contains(raw, `"choices":[`) {
		t.Fatalf("%s Claude stream leaked raw OpenAI chunk: %s", target.Name, truncate(raw, 1000))
	}
	if strings.Contains(raw, "tool_calls=") {
		t.Fatalf("%s Claude stream leaked tool_calls residue: %s", target.Name, truncate(raw, 1000))
	}
	if strings.Contains(raw, `"usage":`) && strings.Contains(raw, `"choices"`) {
		t.Fatalf("%s Claude stream leaked raw OpenAI usage chunk: %s", target.Name, truncate(raw, 1000))
	}
}

func requireNoExecutorE2EFollowUpStatus(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	errStr := err.Error()
	if strings.Contains(errStr, "redis: nil") {
		t.Fatalf("disallowed redis: nil error: %v", err)
	}
	var statusErr cliproxyexecutor.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode() {
		case 4001, http.StatusServiceUnavailable:
			t.Fatalf("tool_result follow-up returned disallowed status %d: %v", statusErr.StatusCode(), err)
		}
	}
}

func executorE2EClaudeReadTool() map[string]any {
	return map[string]any{
		"name":        "Read",
		"description": "Reads a file from the local filesystem.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "The absolute path to the file to read",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

func executorE2EClaudeBashTool() map[string]any {
	return map[string]any{
		"name":        "Bash",
		"description": "Run a shell command in the current working directory.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The command to execute",
				},
			},
			"required": []string{"command"},
		},
	}
}

func executorE2EClaudeGlobTool() map[string]any {
	return map[string]any{
		"name":        "Glob",
		"description": "Find files by glob pattern.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "The glob pattern to match files against",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "The directory to search from",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func mustMarshalExecutorE2EJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal e2e request: %v", err)
	}
	return raw
}

func logExecutorE2ERawChunks(t *testing.T, rawChunks []string) {
	t.Helper()
	for i, raw := range rawChunks {
		t.Logf("  raw[%d]=%s", i, truncate(raw, 1000))
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

func parseClaudeSSEEvents(payload string) []traeE2EClaudeEvent {
	var events []traeE2EClaudeEvent
	currentEvent := ""
	for _, line := range strings.Split(payload, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data != "" {
				events = append(events, traeE2EClaudeEvent{typ: currentEvent, data: data})
			}
		}
	}
	return events
}
