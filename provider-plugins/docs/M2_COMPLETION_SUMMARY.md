# Lingma M2 Completion Summary

> Historical milestone record. See the top-level README and release checklist
> for the current shadow release-candidate status.

## Status: ✅ COMPLETE

All M2 (Translation and Execution) golden parity tests have been implemented and verified.

## What Was Accomplished

### Phase 1: Request Translation Parity (4 tests)
- ✅ OpenAI basic request translation
- ✅ OpenAI request with tools
- ✅ OpenAI request with reasoning_effort
- ✅ Claude basic request translation

**Files created:**
- `testdata/lingma/request_openai_basic.json`
- `testdata/lingma/request_openai_with_tools.json`
- `testdata/lingma/request_openai_reasoning.json`
- `testdata/lingma/request_claude_basic.json`
- `internal/lingma/request_parity_test.go`

### Phase 2: Response Translation Parity (4 tests)
- ✅ OpenAI streaming response
- ✅ OpenAI non-streaming response
- ✅ Claude streaming response
- ✅ Claude non-streaming response

**Files created:**
- `internal/lingma/response_parity_test.go`

### Phase 3-4: Thinking Application Parity (26 subtests)
- ✅ preserveClaudeCodeThinking (12 test cases)
- ✅ claudeCodeThinkingEnabled (14 test cases)

**Files created:**
- `internal/lingma/thinking_parity_test.go`
- `internal/lingma/native_thinking_parity_test.go` (helper with native function copies)

The previously documented disabled-thinking divergence was removed during
release hardening. The plugin and native implementation now both preserve the
request fields and update only `model_config.is_reasoning`.

### Phase 5: Usage Normalization Parity (16 subtests)
- ✅ normalizeLingmaUsage (15 test cases covering all token field variations)
- ✅ Non-existent usage handling

**Files created:**
- `internal/lingma/usage_parity_test.go`
- Added `nativeNormalizeLingmaUsageForTest` to `native_thinking_parity_test.go`
- Added `pluginNormalizeLingmaUsageForTest` to `usage_parity_test.go`

## Test Results

```
Total tests: 30 passing
- M1 tests: 17 (model parsing, auth, registration)
- M2 parity tests: 13 top-level tests with 56 subtests
  - Request parity: 4 tests
  - Response parity: 4 tests
  - Thinking parity: 26 subtests
  - Usage parity: 16 subtests
```

## Implementation Approach

Since the provider-plugins module has a `replace` directive pointing to the parent module, parity tests can import native packages directly. However, for unexported functions, we created test helpers that copy the native implementation:

- `nativeParseLingmaModelsForTest` (M1)
- `nativePreserveLingmaClaudeCodeThinkingForTest` (M2)
- `nativeClaudeCodeThinkingEnabledForTest` (M2)
- `nativeNormalizeLingmaUsageForTest` (M2)
- `pluginNormalizeLingmaUsageForTest` (M2)

This approach ensures tests remain isolated and don't require exporting internal functions.

## Verification

All tests pass:
```bash
cd provider-plugins && go test ./internal/lingma/ -v
```

Main server builds successfully:
```bash
go build -o test-output ./cmd/server && rm test-output
```

## Cutover status

Dynamic-library integration and opt-in live tests now exist. The provider
identifier remains `lingma-plugin`, and the native implementation remains
available until operator-run live validation and a separate cutover decision.
