package lingma

import (
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestResolveAPIBaseURL(t *testing.T) {
	cfgDefault := defaultPluginConfig()
	credsLingma := credentials{Type: ProviderID, UID: "u1"}
	if got := resolveAPIBaseURL(cfgDefault, credsLingma); got != defaultAPIBaseURL {
		t.Errorf("resolveAPIBaseURL(default, lingma) = %q, want %q", got, defaultAPIBaseURL)
	}

	credsQoder := credentials{Type: ProviderID, UID: "u2", IsQoder: true}
	if got := resolveAPIBaseURL(cfgDefault, credsQoder); got != defaultQoderCNBaseURL {
		t.Errorf("resolveAPIBaseURL(default, qoder) = %q, want %q", got, defaultQoderCNBaseURL)
	}

	credsSourceQoder := credentials{Type: ProviderID, UID: "u3", Source: "/home/user/.qoder-cn"}
	if got := resolveAPIBaseURL(cfgDefault, credsSourceQoder); got != defaultQoderCNBaseURL {
		t.Errorf("resolveAPIBaseURL(default, source-qoder) = %q, want %q", got, defaultQoderCNBaseURL)
	}

	cfgCustom := defaultPluginConfig()
	cfgCustom.APIBaseURL = "https://custom.endpoint.com"
	if got := resolveAPIBaseURL(cfgCustom, credsQoder); got != "https://custom.endpoint.com" {
		t.Errorf("resolveAPIBaseURL(custom, qoder) = %q, want https://custom.endpoint.com", got)
	}
}

func TestQoderChatURL(t *testing.T) {
	body := []byte(`{"agent_id":"agent_chat"}`)
	// QoderCN endpoint forces agent_common
	qoderURL := chatURL(defaultQoderCNBaseURL, body, "gm51model")
	if !strings.Contains(qoderURL, "AgentId=agent_common") {
		t.Errorf("chatURL with QoderCN base did not contain AgentId=agent_common: %s", qoderURL)
	}

	// Lingma endpoint preserves agent_id from body or model
	lingmaURL := chatURL(defaultAPIBaseURL, body, "gm51model")
	if !strings.Contains(lingmaURL, "AgentId=agent_chat") {
		t.Errorf("chatURL with Lingma base did not preserve agent_id: %s", lingmaURL)
	}
}

func TestApplyQoderCNBody(t *testing.T) {
	rawReasoning := []byte(`{
		"request_id": "req-123",
		"agent_id": "agent_chat",
		"task_id": "question_refine",
		"model_config": {
			"is_reasoning": true
		},
		"business": {
			"product": "ide"
		}
	}`)

	adapted := applyQoderCNBody(rawReasoning)
	if got := gjson.GetBytes(adapted, "agent_id").String(); got != "agent_common" {
		t.Errorf("agent_id = %q, want agent_common", got)
	}
	if got := gjson.GetBytes(adapted, "task_id").String(); got != "common" {
		t.Errorf("task_id = %q, want common", got)
	}
	if got := gjson.GetBytes(adapted, "session_type").String(); got != "qoderclicn" {
		t.Errorf("session_type = %q, want qoderclicn", got)
	}
	if got := gjson.GetBytes(adapted, "source").Int(); got != 1 {
		t.Errorf("source = %d, want 1", got)
	}
	if got := gjson.GetBytes(adapted, "business.product").String(); got != "qoderclicn" {
		t.Errorf("business.product = %q, want qoderclicn", got)
	}
	if got := gjson.GetBytes(adapted, "parameters.enable_thinking").Bool(); got != true {
		t.Errorf("parameters.enable_thinking = %v, want true", got)
	}
	if got := gjson.GetBytes(adapted, "parameters.reasoning_effort").String(); got != "high" {
		t.Errorf("parameters.reasoning_effort = %q, want high", got)
	}
	if got := gjson.GetBytes(adapted, "model_config.source").String(); got != "system" {
		t.Errorf("model_config.source = %q, want system", got)
	}

	rawNonReasoning := []byte(`{
		"request_id": "req-456",
		"agent_id": "agent_chat",
		"model_config": {
			"is_reasoning": false
		}
	}`)
	adaptedNon := applyQoderCNBody(rawNonReasoning)
	if got := gjson.GetBytes(adaptedNon, "parameters.enable_thinking").Bool(); got != false {
		t.Errorf("non-reasoning enable_thinking = %v, want false", got)
	}
	if got := gjson.GetBytes(adaptedNon, "model_config.source").String(); got != "" {
		t.Errorf("non-reasoning model_config.source = %q, want empty", got)
	}
}

func TestBuildHeadersUserAgent(t *testing.T) {
	creds := credentials{
		CosyKey:         "key-123",
		UID:             "uid-123",
		MachineID:       "mid-123",
		EncryptUserInfo: "info-123",
	}

	// Legacy Lingma request
	hLingma, err := buildHeaders(creds, "", "https://lingma-api.tongyi.aliyun.com/algo/chat", time.Now())
	if err != nil {
		t.Fatalf("buildHeaders error: %v", err)
	}
	if got := hLingma.Get("User-Agent"); got != "Go-http-client/1.1" {
		t.Errorf("Lingma User-Agent = %q, want Go-http-client/1.1", got)
	}

	// QoderCN URL
	hQoderURL, err := buildHeaders(creds, "", "https://gateway.qoder.com.cn/algo/chat", time.Now())
	if err != nil {
		t.Fatalf("buildHeaders error: %v", err)
	}
	if got := hQoderURL.Get("User-Agent"); got != "Bun/1.3.14" {
		t.Errorf("Qoder URL User-Agent = %q, want Bun/1.3.14", got)
	}

	// QoderCN Creds
	creds.IsQoder = true
	hQoderCreds, err := buildHeaders(creds, "", "https://custom.endpoint/algo/chat", time.Now())
	if err != nil {
		t.Fatalf("buildHeaders error: %v", err)
	}
	if got := hQoderCreds.Get("User-Agent"); got != "Bun/1.3.14" {
		t.Errorf("Qoder Creds User-Agent = %q, want Bun/1.3.14", got)
	}
}
