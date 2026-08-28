package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTranslateTransportError_Nil(t *testing.T) {
	if got := translateTransportError(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestTranslateTransportError_PeerResetString(t *testing.T) {
	// This is the actual error string Go's stdlib HTTP/2 client surfaces when
	// the upstream sends a mid-stream RST_STREAM(INTERNAL_ERROR):
	// "stream error: stream ID N; <code>; received from peer".
	raw := errors.New("stream error: stream ID 143; INTERNAL_ERROR; received from peer")
	got := translateTransportError(raw)

	u, ok := got.(transportStatusError)
	if !ok {
		t.Fatalf("expected transportStatusError, got %T (%v)", got, got)
	}
	if !strings.Contains(u.msg, "upstream stream interrupted") {
		t.Errorf("expected message to mention interruption, got %q", u.msg)
	}
	if u.StatusCode() != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", u.StatusCode())
	}
}

func TestTranslateTransportError_PlainErrorUnchanged(t *testing.T) {
	plain := errors.New("some other upstream error")
	got := translateTransportError(plain)
	if got != plain {
		t.Errorf("expected plain error unchanged, got %v", got)
	}
}

func TestTranslateTransportError_UnexpectedEOF(t *testing.T) {
	got := translateTransportError(io.ErrUnexpectedEOF)

	u, ok := got.(transportStatusError)
	if !ok {
		t.Fatalf("expected transportStatusError, got %T (%v)", got, got)
	}
	if u.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", u.StatusCode(), http.StatusBadGateway)
	}
	if u.Error() != "upstream connection closed unexpectedly" {
		t.Fatalf("message = %q", u.Error())
	}
}

func TestTranslateTransportError_DeadlineExceeded(t *testing.T) {
	got := translateTransportError(context.DeadlineExceeded)

	u, ok := got.(transportStatusError)
	if !ok {
		t.Fatalf("expected transportStatusError, got %T (%v)", got, got)
	}
	if u.StatusCode() != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", u.StatusCode(), http.StatusGatewayTimeout)
	}
	if !strings.Contains(u.Error(), "upstream timeout") {
		t.Fatalf("message = %q", u.Error())
	}
}

func TestTranslateTransportError_ContextCanceled(t *testing.T) {
	got := translateTransportError(context.Canceled)

	translated, ok := got.(transportStatusError)
	if !ok {
		t.Fatalf("expected transportStatusError, got %T (%v)", got, got)
	}
	if translated.StatusCode() != statusClientClosedRequest {
		t.Fatalf("status = %d, want %d", translated.StatusCode(), statusClientClosedRequest)
	}
	if !strings.Contains(translated.Error(), "client closed request") {
		t.Fatalf("message = %q", translated.Error())
	}
}

func TestTranslateTransportError_NonPeerStreamErrorUnchanged(t *testing.T) {
	// A stream error string that is not "received from peer" should not be
	// translated (e.g. a client-side cancel has no peer marker).
	raw := errors.New("stream error: stream ID 5; CANCEL")
	got := translateTransportError(raw)
	if got != raw {
		t.Errorf("expected non-peer stream error unchanged, got %v", got)
	}
}

func TestTranslateTransportError_OnlyOneSubstringUnchanged(t *testing.T) {
	// Only one of the two required substrings present -> not a peer reset, leave alone.
	raw := errors.New("stream error: stream ID 9; something else")
	got := translateTransportError(raw)
	if got != raw {
		t.Errorf("expected error with only one substring unchanged, got %v", got)
	}
}
