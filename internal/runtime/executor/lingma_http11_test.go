package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNewLingmaHTTPClient_ForcesHTTP11ByDefault(t *testing.T) {
	cfg := &config.Config{}
	client := newLingmaHTTPClient(context.Background(), cfg, nil, 0)

	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if tr.ForceAttemptHTTP2 {
		t.Errorf("expected ForceAttemptHTTP2=false (HTTP/1.1 forced), got true")
	}
	if len(tr.TLSClientConfig.NextProtos) != 1 || tr.TLSClientConfig.NextProtos[0] != "http/1.1" {
		t.Errorf("expected NextProtos=[http/1.1], got %v", tr.TLSClientConfig.NextProtos)
	}
}

func TestNewLingmaHTTPClient_OptOutViaDisableHTTP11(t *testing.T) {
	cfg := &config.Config{}
	cfg.DisableHTTP11 = true
	client := newLingmaHTTPClient(context.Background(), cfg, nil, 0)

	// When opted out, no transport is set (falls back to http.DefaultTransport,
	// which negotiates HTTP/2). The singleton HTTP/1.1 transport must NOT be used.
	if client.Transport != nil {
		tr, ok := client.Transport.(*http.Transport)
		if ok && !tr.ForceAttemptHTTP2 {
			t.Errorf("expected HTTP/2 (ForceAttemptHTTP2=true) when opted out, got false")
		}
	}
}

func TestNewLingmaHTTPClient_NilCfgForcesHTTP11(t *testing.T) {
	// A nil cfg (e.g. bare &Config{} paths) must still force HTTP/1.1.
	client := newLingmaHTTPClient(context.Background(), nil, nil, 0)

	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if tr.ForceAttemptHTTP2 {
		t.Errorf("expected ForceAttemptHTTP2=false with nil cfg, got true")
	}
}
