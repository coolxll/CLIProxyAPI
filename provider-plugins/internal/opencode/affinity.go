package opencode

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/tidwall/gjson"
)

type requestIDs struct {
	Session       string
	Request       string
	Project       string
	ParentSession string
}

func deriveRequestIDs(headers http.Header, body []byte) requestIDs {
	signal := firstHeaderValue(headers,
		"x-opencode-session",
		"x-session-affinity",
		"X-Session-Id",
		"x-session-id",
		"conversation-id",
		"conversation_id",
	)
	if signal == "" && len(body) > 0 {
		signal = firstGjsonValue(body,
			"conversation_id",
			"metadata.session_id",
			"metadata.conversation_id",
		)
	}
	if signal == "" && len(body) > 0 {
		signal = conversationSeed(body)
	}
	if signal == "" && len(body) > 0 {
		signal = gjson.GetBytes(body, "previous_response_id").String()
	}
	if signal == "" || signal == "{}" {
		signal = randomHex(16)
	}
	session := stableID("ses", signal)

	projectSignal := firstHeaderValue(headers, "x-opencode-project")
	if projectSignal == "" && len(body) > 0 {
		projectSignal = gjson.GetBytes(body, "metadata.project_id").String()
	}
	if projectSignal == "" {
		projectSignal = "opencode2api:cpa-project"
	}

	parentSession := firstHeaderValue(headers, "x-parent-session-id")
	if parentSession == "" && len(body) > 0 {
		parentSession = gjson.GetBytes(body, "metadata.parent_session_id").String()
	}

	return requestIDs{
		Session:       session,
		Request:       "req_" + randomHex(16),
		Project:       stableID("prj", projectSignal),
		ParentSession: parentSession,
	}
}

func conversationSeed(body []byte) string {
	messages := gjson.GetBytes(body, "messages").Array()
	for _, m := range messages {
		if strings.ToLower(m.Get("role").String()) == "user" {
			content := m.Get("content")
			if content.Exists() && content.String() != "" && content.String() != "null" {
				return content.Raw
			}
		}
	}
	if input := gjson.GetBytes(body, "input"); input.Exists() {
		return input.Raw
	}
	return ""
}

func stableID(prefix, value string) string {
	sum := sha256.Sum256([]byte(prefix + "\x00" + value))
	return prefix + "_" + hex.EncodeToString(sum[:12])
}

func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", size)))
		return hex.EncodeToString(sum[:size])
	}
	return hex.EncodeToString(buf)
}

func firstHeaderValue(headers http.Header, keys ...string) string {
	if headers == nil {
		return ""
	}
	for _, k := range keys {
		if v := strings.TrimSpace(headers.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func firstGjsonValue(data []byte, paths ...string) string {
	for _, p := range paths {
		if val := gjson.GetBytes(data, p); val.Exists() {
			if s := strings.TrimSpace(val.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func opencodeUserAgent() string {
	return fmt.Sprintf("opencode/1.18.21 (%s %s; %s)", runtime.GOOS, runtime.GOARCH, runtime.Version())
}

func buildCloudZenHeaders(creds credentials, ids requestIDs, key string, isClaude bool) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json, text/event-stream")
	headers.Set("User-Agent", opencodeUserAgent())
	headers.Set("x-opencode-client", "cli")
	headers.Set("x-opencode-session", ids.Session)
	headers.Set("x-session-affinity", ids.Session)
	headers.Set("X-Session-Id", ids.Session)
	headers.Set("x-opencode-request", ids.Request)
	headers.Set("x-opencode-project", ids.Project)
	if ids.ParentSession != "" {
		headers.Set("x-parent-session-id", ids.ParentSession)
	}

	if isClaude {
		headers.Set("x-api-key", key)
		headers.Set("anthropic-version", "2023-06-01")
		headers.Set("anthropic-beta", "interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
	} else {
		headers.Set("Authorization", "Bearer "+key)
	}
	return headers
}
