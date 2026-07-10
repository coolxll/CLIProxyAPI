package config

import "testing"

func TestParseLingmaThinkingFallbackConfig(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
lingma-thinking-fallback:
  enabled: true
  ttl: "90s"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	if !cfg.LingmaThinkingFallback.Enabled {
		t.Fatal("fallback is disabled")
	}
	if cfg.LingmaThinkingFallback.TTL != "90s" {
		t.Fatalf("ttl = %q, want 90s", cfg.LingmaThinkingFallback.TTL)
	}
}
