package handlers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestTranslateUpstreamStreamError_Nil(t *testing.T) {
	if got := translateUpstreamStreamError(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestTranslateUpstreamStreamError_PeerResetString(t *testing.T) {
	// This is the actual error string Go's stdlib HTTP/2 client surfaces when
	// the upstream sends a mid-stream RST_STREAM(INTERNAL_ERROR):
	// "stream error: stream ID N; <code>; received from peer".
	raw := errors.New("stream error: stream ID 143; INTERNAL_ERROR; received from peer")
	got := translateUpstreamStreamError(raw)

	u, ok := got.(upstreamStreamError)
	if !ok {
		t.Fatalf("expected upstreamStreamError, got %T (%v)", got, got)
	}
	if !strings.Contains(u.msg, "upstream stream interrupted") {
		t.Errorf("expected message to mention interruption, got %q", u.msg)
	}
	if u.StatusCode() != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", u.StatusCode())
	}
}

func TestTranslateUpstreamStreamError_PlainErrorUnchanged(t *testing.T) {
	plain := errors.New("some other upstream error")
	got := translateUpstreamStreamError(plain)
	if got != plain {
		t.Errorf("expected plain error unchanged, got %v", got)
	}
}

func TestTranslateUpstreamStreamError_NonPeerStreamErrorUnchanged(t *testing.T) {
	// A stream error string that is not "received from peer" should not be
	// translated (e.g. a client-side cancel has no peer marker).
	raw := errors.New("stream error: stream ID 5; CANCEL")
	got := translateUpstreamStreamError(raw)
	if got != raw {
		t.Errorf("expected non-peer stream error unchanged, got %v", got)
	}
}

func TestTranslateUpstreamStreamError_OnlyOneSubstringUnchanged(t *testing.T) {
	// Only one of the two required substrings present -> not a peer reset, leave alone.
	raw := errors.New("stream error: stream ID 9; something else")
	got := translateUpstreamStreamError(raw)
	if got != raw {
		t.Errorf("expected error with only one substring unchanged, got %v", got)
	}
}
