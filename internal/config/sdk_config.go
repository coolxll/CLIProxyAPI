// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// DisableHTTP11 opts out of HTTP/1.1 forcing for upstream channels that use
	// it by default (currently Lingma). By default (false) those channels use
	// HTTP/1.1 to avoid mid-stream HTTP/2 RST_STREAM errors from the upstream
	// surfacing as raw "stream error: stream ID N; INTERNAL_ERROR" messages to
	// clients. Set true to negotiate HTTP/2 via ALPN as before.
	DisableHTTP11 bool `yaml:"disable-http11" json:"disable-http11"`

	// LingmaThinkingFallback configures a one-shot no-thinking fallback for
	// large gm51model requests canceled before the upstream produces data.
	LingmaThinkingFallback LingmaThinkingFallbackConfig `yaml:"lingma-thinking-fallback" json:"lingma-thinking-fallback"`

	// LingmaUpstreamRecovery configures same-request recovery for transient
	// Lingma upstream failures before any response is exposed downstream.
	LingmaUpstreamRecovery LingmaUpstreamRecoveryConfig `yaml:"lingma-upstream-recovery" json:"lingma-upstream-recovery"`

	// DisableImageGeneration controls whether the built-in image_generation tool is injected/allowed.
	//
	// Supported values:
	//   - false (default): image_generation is enabled everywhere (normal behavior).
	//   - true: image_generation is disabled everywhere. The server stops injecting it, removes it from request payloads,
	//     and returns 404 for /v1/images/generations and /v1/images/edits.
	//   - "chat": disable image_generation injection for all non-images endpoints (e.g. /v1/responses, /v1/chat/completions),
	//     while keeping /v1/images/generations and /v1/images/edits enabled and preserving image_generation there.
	//   - "passthrough": do not modify the tool list on non-images endpoints — keep image_generation if the client
	//     sent it and do not inject it otherwise; on /v1/images/generations and /v1/images/edits behave like "chat".
	DisableImageGeneration DisableImageGenerationMode `yaml:"disable-image-generation" json:"disable-image-generation"`

	// GPTImage2BaseModel sets the base (mainline) model used by the legacy hosted
	// image_generation tool path when a Codex image request is not proxied directly
	// through the Image API.
	//
	// The value must start with "gpt-" (case-insensitive). If empty or invalid, the
	// default base model ("gpt-5.4-mini") is used.
	GPTImage2BaseModel string `yaml:"gpt-image-2-base-model,omitempty" json:"gpt-image-2-base-model,omitempty"`

	// VideoResultAuthCacheTTL controls how long video IDs stay pinned to the credential
	// that created them. Accepts duration strings like "30m" or "3h".
	// Empty or invalid values use the default 3h.
	VideoResultAuthCacheTTL string `yaml:"video-result-auth-cache-ttl,omitempty" json:"video-result-auth-cache-ttl,omitempty"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// LingmaThinkingFallbackConfig controls Lingma's one-shot retry downgrade.
type LingmaThinkingFallbackConfig struct {
	// Enabled allows the next identical large gm51model thinking request to
	// retry once with reasoning disabled after a pre-response cancellation.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// TTL controls how long a canceled request fingerprint remains eligible.
	// Empty or invalid values use the default of 2m.
	TTL string `yaml:"ttl,omitempty" json:"ttl,omitempty"`
}

// LingmaUpstreamRecoveryConfig controls same-request Lingma recovery.
type LingmaUpstreamRecoveryConfig struct {
	// Disabled turns off same-request recovery. Recovery is enabled by default.
	Disabled bool `yaml:"disabled" json:"disabled"`

	// MaxAttempts includes the initial request. Values outside 1-5 are clamped;
	// zero uses the default of 3.
	MaxAttempts int `yaml:"max-attempts,omitempty" json:"max-attempts,omitempty"`

	// BaseDelay is the initial context-aware retry backoff. Empty, invalid, or
	// non-positive values use the default of 200ms.
	BaseDelay string `yaml:"base-delay,omitempty" json:"base-delay,omitempty"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}
