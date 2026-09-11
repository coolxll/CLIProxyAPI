package trae

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestDeriveStableDeviceID(t *testing.T) {
	jwt := syntheticTraeJWT(t, "12345678")
	did1 := deriveStableDeviceID(jwt, "")
	did2 := deriveStableDeviceID(jwt, "")

	if len(did1) != 16 {
		t.Fatalf("expected 16 digits, got %d (%s)", len(did1), did1)
	}
	for _, r := range did1 {
		if r < '0' || r > '9' {
			t.Fatalf("expected only digits in device ID, got %s", did1)
		}
	}
	if did1 != did2 {
		t.Fatalf("expected deterministic device ID, got %s != %s", did1, did2)
	}

	// Stable derivation from explicit user ID
	didUser := deriveStableDeviceID("", "12345678")
	if len(didUser) != 16 {
		t.Fatalf("expected 16 digits, got %d (%s)", len(didUser), didUser)
	}
}

func TestClaimCheckinCredits(t *testing.T) {
	called := false
	host := func(method string, raw []byte) ([]byte, error) {
		if method == pluginabi.MethodHostHTTPDo {
			var req hostHTTPRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("unmarshal host request: %v", err)
			}
			if req.Request.URL == "https://api.trae.cn/trae/api/v2/ug/checkin_credits/claim" {
				called = true
				if req.Request.Headers.Get("Authorization") != "Cloud-IDE-JWT test-jwt" {
					t.Errorf("expected Authorization header, got %s", req.Request.Headers.Get("Authorization"))
				}
				if req.Request.Headers.Get("x-device-id") != "1234567890123456" {
					t.Errorf("expected x-device-id header, got %s", req.Request.Headers.Get("x-device-id"))
				}
				return pluginruntime.OK(pluginapi.HTTPResponse{
					StatusCode: http.StatusOK,
					Body:       []byte(`{"code":0,"msg":"success","credits":4700}`),
				})
			}
		}
		t.Fatalf("unexpected call: %s", method)
		return nil, nil
	}

	rpc := hostRPC{call: host, callbackID: "test"}
	if err := claimCheckinCredits(rpc, "test-jwt", "1234567890123456"); err != nil {
		t.Fatalf("claimCheckinCredits error: %v", err)
	}
	if !called {
		t.Fatal("claimCheckinCredits endpoint was not called")
	}
}

func TestTraeRawChatV2PlaintextAndExtraHeader(t *testing.T) {
	creds := credentials{
		JWTToken:  "test-trae-jwt",
		MachineID: "test-machine-id",
		DeviceID:  "1234567890123456",
		UserID:    "user_42",
	}

	openaiReq := []byte(`{"messages":[{"role":"user","content":"Hello Trae GLM-5.2"}]}`)
	build, err := buildTraeRawChatRequest(creds, traeProtocolV2, "glm-5.2", openaiReq, nil)
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest failed: %v", err)
	}

	if build.TargetURL != "https://trae-api-cn.mchost.guru/api/ide/v2/llm_raw_chat" {
		t.Fatalf("expected v2 url, got %s", build.TargetURL)
	}

	// Check 6-field unencrypted body
	body := gjson.ParseBytes(build.RequestBody)
	if body.Get("config_name").String() != "glm-5.2" {
		t.Errorf("expected config_name glm-5.2, got %s", body.Get("config_name").String())
	}
	if body.Get("model_name").String() != "glm-5.2" {
		t.Errorf("expected model_name glm-5.2, got %s", body.Get("model_name").String())
	}
	if !body.Get("stream").Bool() {
		t.Error("expected stream: true")
	}
	if body.Get("session_id").String() == "" {
		t.Error("expected non-empty session_id")
	}
	if body.Get("conversation_id").String() == "" {
		t.Error("expected non-empty conversation_id")
	}
	if len(body.Get("messages").Array()) != 1 {
		t.Fatalf("expected 1 message, got %d", len(body.Get("messages").Array()))
	}
	if body.Get("messages.0.content.0.text").String() != "Hello Trae GLM-5.2" {
		t.Errorf("unexpected content: %s", body.Get("messages.0.content.0.text").String())
	}

	// Check Extra JSON header
	extraStr := build.ExtraHeaders.Get("Extra")
	if extraStr == "" {
		t.Fatal("missing Extra header")
	}
	extra := gjson.Parse(extraStr)
	if extra.Get("config_name").String() != "glm-5.2" {
		t.Errorf("expected Extra config_name glm-5.2, got %s", extra.Get("config_name").String())
	}
	if extra.Get("model_name").String() != "glm-5.2" {
		t.Errorf("expected Extra model_name glm-5.2, got %s", extra.Get("model_name").String())
	}
	if extra.Get("api_key").String() != "test-trae-jwt" {
		t.Errorf("expected Extra api_key to match JWT, got %s", extra.Get("api_key").String())
	}
	if extra.Get("display_name").String() != "GLM-5.2" {
		t.Errorf("expected Extra display_name GLM-5.2, got %s", extra.Get("display_name").String())
	}

	// Verify standard headers
	if build.ExtraHeaders.Get("X-App-Id") != "7b3f9dc2-8a4e-5c6d-2f1b-9e4a3c5b7df0" {
		t.Errorf("unexpected X-App-Id: %s", build.ExtraHeaders.Get("X-App-Id"))
	}
	if build.ExtraHeaders.Get("X-Device-Id") != "1234567890123456" {
		t.Errorf("unexpected X-Device-Id: %s", build.ExtraHeaders.Get("X-Device-Id"))
	}
}

func TestTraeSoloWorkLiteRequest(t *testing.T) {
	creds := credentials{
		JWTToken:  "test-trae-jwt",
		MachineID: "test-machine-id",
		DeviceID:  "1234567890123456",
		UserID:    "user_42",
	}

	openaiReq := []byte(`{"messages":[{"role":"user","content":"Solo work prompt"}]}`)
	build, err := buildTraeRawChatRequest(creds, traeProtocolSolo, "deepseek-v4-pro", openaiReq, nil)
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest solo failed: %v", err)
	}

	if build.TargetURL != "https://trae-api-cn.mchost.guru/api/agent/v3/llm_utils_chat" {
		t.Fatalf("expected solo_work_lite url, got %s", build.TargetURL)
	}

	body := gjson.ParseBytes(build.RequestBody)
	if body.Get("function").String() != "solo_work_lite" {
		t.Errorf("expected function solo_work_lite, got %s", body.Get("function").String())
	}
	if body.Get("config_name").String() != "DeepSeek-V4-Pro" {
		t.Errorf("expected config_name DeepSeek-V4-Pro, got %s", body.Get("config_name").String())
	}
	if body.Get("model").String() != "DeepSeek-V4-Pro" {
		t.Errorf("expected model DeepSeek-V4-Pro, got %s", body.Get("model").String())
	}
	if !body.Get("stream").Bool() {
		t.Error("expected stream: true")
	}

	if build.ExtraHeaders.Get("User-Agent") != "Trae/0.1.52" {
		t.Errorf("expected User-Agent Trae/0.1.52, got %s", build.ExtraHeaders.Get("User-Agent"))
	}
	if build.ExtraHeaders.Get("X-App-Id") != "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8" {
		t.Errorf("unexpected X-App-Id: %s", build.ExtraHeaders.Get("X-App-Id"))
	}
}
