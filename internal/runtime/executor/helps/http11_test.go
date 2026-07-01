package helps

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

func TestNewHTTP11Client_ProxyPathReusesCachedTransport(t *testing.T) {
	ResetHTTP11ProxyTransportsForTest(t)
	t.Cleanup(func() { ResetHTTP11ProxyTransportsForTest(t) })

	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://127.0.0.1:0"}}
	c1 := NewHTTP11Client(context.Background(), cfg, nil, 0, true)
	c2 := NewHTTP11Client(context.Background(), cfg, nil, 0, true)

	tr1, ok1 := c1.Transport.(*http.Transport)
	tr2, ok2 := c2.Transport.(*http.Transport)
	if !ok1 || !ok2 {
		t.Fatalf("expected *http.Transport on proxy path, got %T / %T", c1.Transport, c2.Transport)
	}
	if tr1 != tr2 {
		t.Errorf("expected the same cached transport across calls (no per-call rebuild), got %p vs %p", tr1, tr2)
	}
	if tr1.ForceAttemptHTTP2 {
		t.Errorf("expected ForceAttemptHTTP2=false on cached proxy transport")
	}
}

func TestNewHTTP11Client_ProxyCacheBounded(t *testing.T) {
	ResetHTTP11ProxyTransportsForTest(t)
	t.Cleanup(func() { ResetHTTP11ProxyTransportsForTest(t) })

	cfg := &config.Config{}
	// Insert more than the cap to confirm eviction keeps the cache bounded.
	for i := 0; i < http11ProxyMaxTransports+5; i++ {
		cfg.ProxyURL = "http://127.0.0.1:" + itoa(i)
		_ = NewHTTP11Client(context.Background(), cfg, nil, 0, true)
	}

	http11ProxyTransports.mu.Lock()
	defer http11ProxyTransports.mu.Unlock()
	if got := len(http11ProxyTransports.entries); got > http11ProxyMaxTransports {
		t.Errorf("expected cache bounded at %d entries, got %d", http11ProxyMaxTransports, got)
	}
	if len(http11ProxyTransports.order) != len(http11ProxyTransports.entries) {
		t.Errorf("order slice length %d != entries %d", len(http11ProxyTransports.order), len(http11ProxyTransports.entries))
	}
}

func TestNewHTTP11Client_NoProxyUsesSingleton(t *testing.T) {
	SetHTTP11TransportForTest(t, CloneTransportWithHTTP11(http.DefaultTransport.(*http.Transport).Clone()))

	cfg := &config.Config{}
	client := NewHTTP11Client(context.Background(), cfg, nil, 0, true)

	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if tr.ForceAttemptHTTP2 {
		t.Errorf("expected ForceAttemptHTTP2=false (HTTP/1.1 forced)")
	}
}

func TestNewHTTP11Client_ForceFalseReturnsProxyAware(t *testing.T) {
	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{ProxyURL: "http://127.0.0.1:0"}
	client := NewHTTP11Client(context.Background(), cfg, auth, 0, false)

	// When forceHTTP11 is false, NewProxyAwareHTTPClient builds a fresh proxy
	// transport (HTTP/2-capable) — ForceAttemptHTTP2 stays at the clone default.
	if client.Transport == nil {
		t.Fatal("expected a transport on the proxy path")
	}
}

// itoa avoids pulling in strconv for a tiny test helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

