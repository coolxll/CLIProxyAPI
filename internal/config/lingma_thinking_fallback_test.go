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

func TestParseLingmaUpstreamRecoveryConfig(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
lingma-upstream-recovery:
  disabled: true
  max-attempts: 4
  base-delay: "350ms"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	if !cfg.LingmaUpstreamRecovery.Disabled {
		t.Fatal("upstream recovery is enabled")
	}
	if cfg.LingmaUpstreamRecovery.MaxAttempts != 4 {
		t.Fatalf("max-attempts = %d, want 4", cfg.LingmaUpstreamRecovery.MaxAttempts)
	}
	if cfg.LingmaUpstreamRecovery.BaseDelay != "350ms" {
		t.Fatalf("base-delay = %q, want 350ms", cfg.LingmaUpstreamRecovery.BaseDelay)
	}
}
