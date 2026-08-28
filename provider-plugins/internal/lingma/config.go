package lingma

import (
	"bytes"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type pluginConfig struct {
	APIBaseURL          string
	ForceHTTP11         bool
	ThinkingFallback    bool
	ThinkingFallbackTTL time.Duration
	RecoveryDisabled    bool
	RecoveryMaxAttempts int
	RecoveryBaseDelay   time.Duration
}

type rawPluginConfig struct {
	APIBaseURL          string `yaml:"api-base-url"`
	ForceHTTP11         *bool  `yaml:"force-http-1-1"`
	ThinkingFallback    bool   `yaml:"thinking-fallback-enabled"`
	ThinkingFallbackTTL string `yaml:"thinking-fallback-ttl"`
	Recovery            struct {
		Disabled    bool   `yaml:"disabled"`
		MaxAttempts int    `yaml:"max-attempts"`
		BaseDelay   string `yaml:"base-delay"`
	} `yaml:"upstream-recovery"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		APIBaseURL:          defaultAPIBaseURL,
		ForceHTTP11:         true,
		ThinkingFallbackTTL: 2 * time.Minute,
		RecoveryMaxAttempts: 3,
		RecoveryBaseDelay:   200 * time.Millisecond,
	}
}

func parsePluginConfig(raw []byte) (pluginConfig, error) {
	config := defaultPluginConfig()
	if len(bytes.TrimSpace(raw)) == 0 {
		return config, nil
	}
	var decoded rawPluginConfig
	if errUnmarshal := yaml.Unmarshal(raw, &decoded); errUnmarshal != nil {
		return config, errUnmarshal
	}
	if decoded.APIBaseURL != "" {
		config.APIBaseURL = decoded.APIBaseURL
	}
	if decoded.ForceHTTP11 != nil {
		config.ForceHTTP11 = *decoded.ForceHTTP11
	}
	config.ThinkingFallback = decoded.ThinkingFallback
	if decoded.ThinkingFallbackTTL != "" {
		if parsed, errParse := time.ParseDuration(decoded.ThinkingFallbackTTL); errParse == nil && parsed > 0 {
			config.ThinkingFallbackTTL = parsed
		}
	}
	config.RecoveryDisabled = decoded.Recovery.Disabled
	if decoded.Recovery.MaxAttempts != 0 {
		config.RecoveryMaxAttempts = decoded.Recovery.MaxAttempts
	}
	if config.RecoveryMaxAttempts < 1 {
		config.RecoveryMaxAttempts = 1
	}
	if config.RecoveryMaxAttempts > 5 {
		config.RecoveryMaxAttempts = 5
	}
	if decoded.Recovery.BaseDelay != "" {
		if parsed, errParse := time.ParseDuration(decoded.Recovery.BaseDelay); errParse == nil && parsed > 0 {
			config.RecoveryBaseDelay = parsed
		}
	}
	return config, nil
}

type oneShotFallback struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func newOneShotFallback() *oneShotFallback {
	return &oneShotFallback{entries: make(map[string]time.Time)}
}

func (s *oneShotFallback) mark(key string, ttl time.Duration) {
	if s == nil || key == "" || ttl <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for existing, expiry := range s.entries {
		if !expiry.After(now) {
			delete(s.entries, existing)
		}
	}
	if len(s.entries) >= 1024 {
		var oldestKey string
		var oldest time.Time
		for existing, expiry := range s.entries {
			if oldestKey == "" || expiry.Before(oldest) {
				oldestKey, oldest = existing, expiry
			}
		}
		delete(s.entries, oldestKey)
	}
	s.entries[key] = now.Add(ttl)
}

func (s *oneShotFallback) consume(key string) bool {
	if s == nil || key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, exists := s.entries[key]
	if !exists {
		return false
	}
	delete(s.entries, key)
	return expiry.After(time.Now())
}
