package opencode

import (
	"net/http"
	"strings"
	"testing"
)

func TestDeriveRequestIDsFromHeaders(t *testing.T) {
	headers := make(http.Header)
	headers.Set("x-session-affinity", "custom-session-123")
	headers.Set("x-opencode-project", "my-project")
	headers.Set("x-parent-session-id", "parent-ses-999")

	ids := deriveRequestIDs(headers, nil)
	if !strings.HasPrefix(ids.Session, "ses_") {
		t.Errorf("expected session prefix 'ses_', got %q", ids.Session)
	}
	if !strings.HasPrefix(ids.Request, "req_") {
		t.Errorf("expected request prefix 'req_', got %q", ids.Request)
	}
	if !strings.HasPrefix(ids.Project, "prj_") {
		t.Errorf("expected project prefix 'prj_', got %q", ids.Project)
	}
	if ids.ParentSession != "parent-ses-999" {
		t.Errorf("expected parent session 'parent-ses-999', got %q", ids.ParentSession)
	}
}

func TestDeriveRequestIDsStableMultiTurn(t *testing.T) {
	// Turn 1
	bodyTurn1 := []byte(`{
		"model": "big-pickle",
		"messages": [
			{"role": "user", "content": "Hello, how are you?"}
		]
	}`)

	// Turn 2: same conversation, longer history
	bodyTurn2 := []byte(`{
		"model": "big-pickle",
		"messages": [
			{"role": "user", "content": "Hello, how are you?"},
			{"role": "assistant", "content": "I am doing well!"},
			{"role": "user", "content": "What is 2+2?"}
		]
	}`)

	// Turn 3: different conversation, different first message
	bodyDifferent := []byte(`{
		"model": "big-pickle",
		"messages": [
			{"role": "user", "content": "Write a quicksort in Go."}
		]
	}`)

	ids1 := deriveRequestIDs(nil, bodyTurn1)
	ids2 := deriveRequestIDs(nil, bodyTurn2)
	idsDiff := deriveRequestIDs(nil, bodyDifferent)

	if ids1.Session == "" {
		t.Fatalf("session ID 1 should not be empty")
	}
	if ids1.Session != ids2.Session {
		t.Errorf("multi-turn conversation should retain same session ID: turn1=%s, turn2=%s", ids1.Session, ids2.Session)
	}
	if ids1.Session == idsDiff.Session {
		t.Errorf("different conversation should have different session ID: got %s for both", ids1.Session)
	}
}

func TestBuildCloudZenHeaders(t *testing.T) {
	ids := requestIDs{
		Session: "ses_abc123",
		Request: "req_xyz789",
		Project: "prj_test456",
	}

	t.Run("chat completions headers", func(t *testing.T) {
		headers := buildCloudZenHeaders(credentials{}, ids, "public", false)
		if got := headers.Get("Authorization"); got != "Bearer public" {
			t.Errorf("expected 'Bearer public', got %q", got)
		}
		if got := headers.Get("x-opencode-client"); got != "cli" {
			t.Errorf("expected 'cli', got %q", got)
		}
		if got := headers.Get("x-opencode-session"); got != "ses_abc123" {
			t.Errorf("expected 'ses_abc123', got %q", got)
		}
		if got := headers.Get("x-session-affinity"); got != "ses_abc123" {
			t.Errorf("expected 'ses_abc123', got %q", got)
		}
		if got := headers.Get("X-Session-Id"); got != "ses_abc123" {
			t.Errorf("expected 'ses_abc123', got %q", got)
		}
		if got := headers.Get("x-opencode-request"); got != "req_xyz789" {
			t.Errorf("expected 'req_xyz789', got %q", got)
		}
		if !strings.HasPrefix(headers.Get("User-Agent"), "opencode/") {
			t.Errorf("expected User-Agent to start with 'opencode/', got %q", headers.Get("User-Agent"))
		}
	})

	t.Run("claude headers", func(t *testing.T) {
		headers := buildCloudZenHeaders(credentials{}, ids, "sk-custom-zen-key", true)
		if got := headers.Get("x-api-key"); got != "sk-custom-zen-key" {
			t.Errorf("expected 'sk-custom-zen-key', got %q", got)
		}
		if got := headers.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("expected '2023-06-01', got %q", got)
		}
		if !strings.Contains(headers.Get("anthropic-beta"), "interleaved-thinking") {
			t.Errorf("expected anthropic-beta header, got %q", headers.Get("anthropic-beta"))
		}
	})
}
