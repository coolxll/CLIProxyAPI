package helps

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestCloneTransportWithHTTP11(t *testing.T) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	// Ensure base looks like the stdlib default (HTTP/2 enabled).
	base.ForceAttemptHTTP2 = true
	base.TLSNextProto = map[string]func(authority string, c *tls.Conn) http.RoundTripper{
		"h2": func(string, *tls.Conn) http.RoundTripper { return nil },
	}
	base.TLSClientConfig = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}

	clone := CloneTransportWithHTTP11(base)

	if clone == nil {
		t.Fatal("expected non-nil clone")
	}
	if clone.ForceAttemptHTTP2 {
		t.Errorf("expected ForceAttemptHTTP2=false, got true")
	}
	if len(clone.TLSNextProto) != 0 {
		t.Errorf("expected TLSNextProto wiped, got %d entries", len(clone.TLSNextProto))
	}
	if clone.TLSClientConfig == nil {
		t.Fatal("expected non-nil TLSClientConfig")
	}
	if got := clone.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Errorf("expected NextProtos=[http/1.1], got %v", got)
	}

	// Base must be unmutated.
	if !base.ForceAttemptHTTP2 {
		t.Errorf("base ForceAttemptHTTP2 was mutated")
	}
	if len(base.TLSNextProto) == 0 {
		t.Errorf("base TLSNextProto was mutated")
	}
}

func TestCloneTransportWithHTTP11NilBase(t *testing.T) {
	if got := CloneTransportWithHTTP11(nil); got != nil {
		t.Errorf("expected nil for nil base, got %v", got)
	}
}
