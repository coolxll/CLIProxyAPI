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

// http11ProxyMaxTransports bounds the number of cached per-proxy HTTP/1.1
// transports. Proxy URLs can change at runtime (auth refresh, config
// hot-reload, management edits), so an unbounded cache would leak connection
// pools and their idle-connection goroutines forever. When the cap is reached
// the oldest entry is evicted and its idle connections are closed.
const http11ProxyMaxTransports = 16

// http11ProxyTransportCache holds at most http11ProxyMaxTransports cached
// HTTP/1.1 transports keyed by proxy URL. It is guarded by a mutex so that
// eviction can close idle connections on the evicted transport safely.
type http11ProxyTransportCache struct {
	mu       sync.Mutex
	entries  map[string]*http.Transport // nil entries mark in-progress builds
	order    []string                   // LRU order, oldest first
	building map[string]*sync.WaitGroup // per-key wait groups for concurrent first builds
}

var http11ProxyTransports = &http11ProxyTransportCache{
	entries:  make(map[string]*http.Transport),
	building: make(map[string]*sync.WaitGroup),
}

// ResetHTTP11ProxyTransportsForTest clears the per-proxy HTTP/1.1 transport
// cache, closing idle connections on every cached transport. Intended for
// tests that need a clean cache between subtests.
func ResetHTTP11ProxyTransportsForTest(t testing.TB) {
	t.Helper()
	http11ProxyTransports.reset()
}

func (c *http11ProxyTransportCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tr := range c.entries {
		if tr != nil {
			tr.CloseIdleConnections()
		}
	}
	c.entries = make(map[string]*http.Transport)
	c.order = nil
	c.building = make(map[string]*sync.WaitGroup)
}

// get returns the cached HTTP/1.1 transport for proxyURL, building one on
// first use. On eviction, the oldest transport's idle connections are closed.
// Returns nil if the proxy transport could not be built.
func (c *http11ProxyTransportCache) get(proxyURL string) *http.Transport {
	c.mu.Lock()
	if tr, ok := c.entries[proxyURL]; ok && tr != nil {
		c.touchLocked(proxyURL)
		c.mu.Unlock()
		return tr
	}
	// Wait for an in-progress build instead of racing to build a duplicate.
	if wg, building := c.building[proxyURL]; building && wg != nil {
		c.mu.Unlock()
		wg.Wait()
		c.mu.Lock()
		tr, _ := c.entries[proxyURL]
		c.mu.Unlock()
		return tr
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.building[proxyURL] = wg
	c.mu.Unlock()

	// Build outside the lock so concurrent builds for other keys proceed.
	base := buildProxyTransport(proxyURL)
	var clone *http.Transport
	if base != nil {
		clone = CloneTransportWithHTTP11(base)
	}

	c.mu.Lock()
	delete(c.building, proxyURL)
	if clone != nil {
		c.entries[proxyURL] = clone
		c.order = append(c.order, proxyURL)
		c.evictLocked()
	}
	c.mu.Unlock()
	wg.Done()
	return clone
}

// touchLocked moves proxyURL to the most-recently-used position. Caller must
// hold c.mu.
func (c *http11ProxyTransportCache) touchLocked(proxyURL string) {
	for i, key := range c.order {
		if key == proxyURL {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, proxyURL)
			return
		}
	}
	c.order = append(c.order, proxyURL)
}

// evictLocked closes idle connections on and removes the oldest transports
// while the cache exceeds the cap. Caller must hold c.mu.
func (c *http11ProxyTransportCache) evictLocked() {
	for len(c.order) > http11ProxyMaxTransports {
		oldest := c.order[0]
		c.order = c.order[1:]
		if tr, ok := c.entries[oldest]; ok && tr != nil {
			tr.CloseIdleConnections()
		}
		delete(c.entries, oldest)
	}
}

// resolveProxyURL returns the effective proxy URL for the given auth/config,
// matching NewProxyAwareHTTPClient's priority: auth.ProxyURL first, then
// cfg.ProxyURL. It is the single source of truth so the per-proxy cache key
// matches the transport NewProxyAwareHTTPClient would build.
func resolveProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if v := strings.TrimSpace(auth.ProxyURL); v != "" {
			return v
		}
	}
	if cfg != nil {
		if v := strings.TrimSpace(cfg.ProxyURL); v != "" {
			return v
		}
	}
	return ""
}

// NewHTTP11Client returns an HTTP client that forces HTTP/1.1. When
// forceHTTP11 is false, the client is returned unchanged (HTTP/2 negotiated
// via ALPN as usual).
//
// The no-proxy path uses a shared singleton transport; the proxy path uses a
// bounded per-proxy-URL cached transport — both to avoid leaking a connection
// pool per request. A context-injected RoundTripper that is not an
// *http.Transport (e.g. a test double) is left untouched.
func NewHTTP11Client(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration, forceHTTP11 bool) *http.Client {
	if !forceHTTP11 {
		return NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	}

	// Resolve the proxy URL first so the proxy path can reuse the cached
	// HTTP/1.1 transport directly, instead of calling NewProxyAwareHTTPClient
	// (which builds a fresh proxy transport on every call only to be
	// discarded).
	proxyURL := resolveProxyURL(cfg, auth)

	// Proxy path: reuse a cached HTTP/1.1 transport per proxy URL so the
	// connection pool persists across requests. Falls back to
	// NewProxyAwareHTTPClient if the cache build failed (e.g. invalid proxy
	// URL), which then degrades to the context RoundTripper / default
	// transport.
	if proxyURL != "" {
		if cached := http11ProxyTransports.get(proxyURL); cached != nil {
			return &http.Client{Timeout: timeout, Transport: cached}
		}
		// Proxy build failed: fall through to the proxy-aware client, which
		// logs and degrades to the context/default transport. HTTP/1.1
		// forcing is best-effort in this degraded case.
		return NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	}

	// No-proxy path: use the shared singleton, unless a context-injected
	// RoundTripper is present (e.g. a test double or a custom transport).
	client := NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	if client.Transport == nil {
		http11TransportOnce.Do(initHTTP11Transport)
		client.Transport = http11Transport
		return client
	}
	// Context-injected *http.Transport: clone per call to force HTTP/1.1.
	if transport, ok := client.Transport.(*http.Transport); ok {
		if cloned := CloneTransportWithHTTP11(transport); cloned != nil {
			client.Transport = cloned
		}
	}
	return client
}
