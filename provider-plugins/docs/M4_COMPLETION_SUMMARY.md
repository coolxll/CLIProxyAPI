# Trae M4 Completion Summary

> Historical milestone record. See the top-level README and release checklist
> for the current shadow release-candidate status.

## Status: ✅ COMPLETE

All M4 (Authentication and Model Discovery) implementation tasks have been completed and verified.

## What Was Accomplished

### Phase 1: Token Refresh (credentials.go)
- ✅ `credentialsFromStorage()` — Parse and validate credential JSON
- ✅ `validateCredentials()` — Check required fields (jwt_token, machine_id, device_id)
- ✅ `refreshToken()` — OAuth token exchange via host HTTP callback
  - POST to `{host}/cloudide/api/v3/trae/oauth/ExchangeToken`
  - Payload: `{"ClientID":"ono9krqynydwx5","ClientSecret":"-","UserID":"","RefreshToken":"..."}`
  - Parse new AccessToken and RefreshToken from response
  - Extract user ID from JWT if not already set
- ✅ `jwtExpiresAt()` — Extract expiry from JWT
- ✅ `userIDFromJWT()` — Extract user ID from JWT payload
- ✅ `credentialExpiry()` — Return credential expiry time
- ✅ `nextRefreshTime()` — Calculate next refresh time (5 minutes before expiry)
- ✅ `stableAuthID()` — Generate stable identifier from credentials
- ✅ `normalizedFileName()` — Generate safe filename from user ID
- ✅ `safeUpstreamMessage()` — Extract readable error messages from upstream responses
- ✅ `extractString()` — Extract string values from nested JSON using path candidates

**Constants ported:**
- `traeAuthClientID = "ono9krqynydwx5"`
- `traeAPIHost = "https://api.trae.com.cn"`

### Phase 2: Model Discovery (models.go)
- ✅ `fetchModels()` — Fetch models with detail param primary, model list fallback
- ✅ `fetchModelsFromDetailParam()` — POST to detail param endpoint
- ✅ `fetchModelsFromModelList()` — GET from model list endpoint
- ✅ `parseTraeDetailParamWithConfigs()` — Parse get_detail_param response
  - Filter by `usage == "chat_completion"`, `config_switch == true`, `is_invisible_to_user == false`
  - Skip `custom_model_*`, `custom_claude*`, `custom_gemini*`, `*-auto`, `*_auto`
  - Extract model name, display name, context length, max tokens, multimodal flag
- ✅ `parseTraeModels()` — Parse model_list response
  - Filter by `status == true`
  - Skip `Doubao_1_5_thinking_pro`
  - Deduplicate by lowercase ID
- ✅ `appendTraeNoThinkingModel()` — Ensure no_thinking_model is present
- ✅ `appendTraeV1RawChatModels()` — Add V1 models (seed_m8, deepseek-R1, deepseek-V3, deepseek-V3-0324)
- ✅ `appendTraeV3AgentModels()` — Add V3 models (glm-4.7, glm-5, glm-5.1, DeepSeek-V4-Pro, DeepSeek-V4-Flash, kimi-k2.6, qwen-3.6-plus)
- ✅ `setTraeCommonHeaders()` — Set common Trae API headers (Authorization, X-App-Id, x-device-id, x-machine-id, etc.)

**API endpoints:**
- Detail param: `https://trae-api-cn.mchost.guru/api/ide/v1/get_detail_param`
- Model list: `https://trae-api-cn.mchost.guru/api/ide/v1/model_list?type=llm_raw_chat`

### Phase 3: Static Models (static_models.go)
- ✅ `staticModels()` — Return full static catalog of 22 Trae models
  - 4 V1 Raw Chat Models (seed_m8, deepseek-R1, deepseek-V3, deepseek-V3-0324)
  - 1 V2 Synthetic Model (no_thinking_model)
  - 9 V3 Core Models (DeepSeek-V4-Pro, DeepSeek-V4-Flash, Doubao-Seed-2.0-Code, glm-5.1, glm-5v-turbo, kimi-k2.6, qwen-3.6-plus, qwen3-coder, minimax-m2.7)
  - 8 V3 Optional Models (glm-5, glm-4.7, kimi-k2.5, kimi-k2, qwen-3.5, doubao_1_8, Doubao_1_6, minimax-m2.5)
  - All with correct context lengths, max tokens, and multimodal flags

### Phase 4: Plugin Integration (plugin.go, host_rpc.go)
- ✅ Updated `plugin.go` to wire in auth refresh and model discovery
  - `refreshAuth()` — Call `refreshToken()` via host RPC
  - `modelsForAuth()` — Call `fetchModels()` via host RPC
  - `staticModels()` — Return static model catalog
  - Updated registration to declare `ModelProvider: true`
  - Updated version to `0.2.0`
- ✅ Created `host_rpc.go` — Host RPC layer (copied from Lingma pattern)
  - `hostRPC` struct with `do()`, `doStream()`, `readHTTPStream()`, `closeHTTPStream()`, `emit()`, `closeOutputStream()`, `log()` methods
  - Generic `hostCall[T]()` function for type-safe host callbacks

### Phase 5: Tests and Verification
- ✅ Created 4 test fixtures in `testdata/trae/`:
  - `credential_valid.json` — Valid credential for testing
  - `token_refresh_response.json` — Sample token refresh response
  - `model_list_detail_param.json` — Sample get_detail_param response (with Trae-native model names)
  - `model_list_raw_chat.json` — Sample model_list response (with Trae-native model names)

- ✅ Created `credentials_test.go` with 20 tests:
  - Credential parsing and validation (4 tests)
  - Token refresh via host callback (3 tests)
  - JWT expiry extraction (2 tests)
  - JWT user ID extraction (2 tests)
  - Credential expiry calculation (2 tests)
  - Next refresh time calculation (2 tests)
  - Auth ID generation (1 test)
  - Filename normalization (1 test)
  - String extraction (1 test)
  - Safe upstream message extraction (1 test with 4 subtests)

- ✅ Created `models_test.go` with 11 tests:
  - Fetch models from detail param (1 test)
  - Fetch models from model list with fallback (1 test)
  - Parse detail param response (1 test)
  - Parse model list response (1 test)
  - Append no_thinking_model (2 tests)
  - Append V1 raw chat models (1 test)
  - Append V3 agent models (1 test)
  - Static models catalog (1 test)
  - Model existence check (1 test)
  - Common headers construction (1 test)

- ✅ All 35 tests pass (4 existing + 31 new)
- ✅ Main server builds successfully

## Test Results

```
Total tests: 35 passing
- Existing tests: 4 (registration, credential parsing)
- Credential tests: 20 (parsing, validation, refresh, JWT, expiry)
- Model tests: 11 (discovery, parsing, static models, headers)
```

## Implementation Approach

The Trae plugin follows the same pattern as Lingma:
1. Use host HTTP callback for all API calls (no direct network access)
2. Shadow provider identifier `trae-plugin` during migration phase
3. Port native functions from `cmd/trae-export/main.go` and `internal/runtime/executor/trae_models.go`
4. Create test helpers using `HostCall` function pattern (not struct mocks)
5. Use `pluginruntime.OK()` to wrap responses in proper envelope format

## Key Differences from Native

1. **Provider ID**: Plugin uses `trae-plugin`, native uses `trae`
2. **Model Type**: Plugin models have `Type: "trae-plugin"`, native has `Type: "trae"`
3. **Browser login**: Implemented through plugin auth start/poll and the
   management callback fragment bridge
4. **No SDK authenticator**: Plugin handles auth directly

## Verification

All tests pass:
```bash
cd provider-plugins && go test -v ./internal/trae/
```

Main server builds successfully:
```bash
cd /Users/lynn/Workspace/cpa/CLIProxyAPI && go build -o test-output ./cmd/server && rm test-output
```

## Later status

M5 is complete, including V1/V2/V3 execution, encrypted frames, tools,
streaming terminal behavior, usage conversion, offline parity, and real dynamic
library host integration. Cutover remains intentionally deferred.
