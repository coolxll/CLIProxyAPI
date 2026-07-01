package helps

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CloneTransportWithHTTP11 returns a clone of base configured to use HTTP/1.1
// only. It disables HTTP/2 negotiation (ForceAttemptHTTP2=false, wiped
// TLSNextProto) and advertises only "http/1.1" in the ALPN handshake.
//
// This is used by upstream channels that hit mid-stream HTTP/2 RST_STREAM
// errors from their upstream (e.g. Go stdlib HTTP/2 client surfacing
// "stream error: stream ID N; INTERNAL_ERROR; received from peer"). Forcing
// HTTP/1.1 turns a mid-stream reset into a clean connection close that the
// executor can handle gracefully.
//
// base is never mutated; Clone() is called first. Returns nil if base is nil.
func CloneTransportWithHTTP11(base *http.Transport) *http.Transport {
	if base == nil {
		return nil
	}

	clone := base.Clone()
	clone.ForceAttemptHTTP2 = false
	// Wipe TLSNextProto to prevent implicit HTTP/2 upgrade.
	clone.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{}
	} else {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
	}
	// Actively advertise only HTTP/1.1 in the ALPN handshake.
	clone.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return clone
}

// http11Transport is a singleton HTTP/1.1 transport shared by all upstream
// channels that force HTTP/1.1 (currently Lingma and Antigravity) when no
// proxy is configured. It is initialized once to avoid leaking a new
// connection pool (and the goroutines managing it) on every request.
var (
	http11Transport     *http.Transport
	http11TransportOnce sync.Once

	// http11ProxyTransports caches HTTP/1.1 transports keyed by proxy URL, so
	// proxied requests reuse a single connection pool per proxy instead of
	// cloning (and discarding) a fresh transport on every request.
	http11ProxyTransports sync.Map
)

// SetHTTP11TransportForTest overrides the shared no-proxy HTTP/1.1 singleton
// transport for tests. Pass nil to clear the override (restoring lazy init).
// The caller is responsible for invoking sync.Once init as needed. This is
// intended only for test injection of a custom dialer/transport.
func SetHTTP11TransportForTest(t testing.TB, transport *http.Transport) {
	t.Helper()
	http11Transport = transport
	http11TransportOnce = sync.Once{}
	if transport != nil {
		// Mark init as done so the lazy init does not overwrite the override.
		http11TransportOnce.Do(func() {})
	}
	t.Cleanup(func() {
		http11Transport = nil
		http11TransportOnce = sync.Once{}
	})
}

func initHTTP11Transport() {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	http11Transport = CloneTransportWithHTTP11(base)
}

// http11TransportForProxy returns a cached HTTP/1.1 transport for the given
// proxy URL, building one from a fresh proxy transport on first use. The
// transport is reused across requests so its connection pool persists.
func http11TransportForProxy(proxyURL string) *http.Transport {
	if v, ok := http11ProxyTransports.Load(proxyURL); ok {
		return v.(*http.Transport)
	}
	base := buildProxyTransport(proxyURL)
	if base == nil {
		return nil
	}
	clone := CloneTransportWithHTTP11(base)
	if clone == nil {
		return nil
	}
	// LoadOrStore guards against a concurrent first-build race; the loser
	// discards its clone and reuses the winner's.
	if actual, loaded := http11ProxyTransports.LoadOrStore(proxyURL, clone); loaded {
		return actual.(*http.Transport)
	}
	return clone
}

// NewHTTP11Client returns an HTTP client that forces HTTP/1.1, wrapping the
// proxy-aware client from NewProxyAwareHTTPClient. When forceHTTP11 is false,
// the client is returned unchanged (HTTP/2 negotiated via ALPN as usual).
//
// The no-proxy path uses a shared singleton transport; the proxy path uses a
// per-proxy-URL cached transport — both to avoid leaking a connection pool per
// request. A context-injected RoundTripper that is not an *http.Transport
// (e.g. a test double) is left untouched.
func NewHTTP11Client(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration, forceHTTP11 bool) *http.Client {
	if !forceHTTP11 {
		return NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	}

	// Resolve the proxy URL the same way NewProxyAwareHTTPClient does, so the
	// per-proxy cache key matches the transport it would have built.
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	client := NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	if client.Transport == nil {
		// No proxy and no context RoundTripper: use the shared singleton.
		http11TransportOnce.Do(initHTTP11Transport)
		client.Transport = http11Transport
		return client
	}
	// Proxy path: reuse a cached HTTP/1.1 transport per proxy URL so the
	// connection pool persists across requests instead of being rebuilt every
	// call. Falls back to a per-call clone if the cache build failed.
	if proxyURL != "" {
		if cached := http11TransportForProxy(proxyURL); cached != nil {
			client.Transport = cached
			return client
		}
	}
	// Context-injected *http.Transport (or a failed cache): clone per call.
	if transport, ok := client.Transport.(*http.Transport); ok {
		if cloned := CloneTransportWithHTTP11(transport); cloned != nil {
			client.Transport = cloned
		}
	}
	return client
}
