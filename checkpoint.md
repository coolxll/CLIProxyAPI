# Checkpoint

## Current Task

Refactor executor e2e tests so Trae v1, Trae v3, and Lingma share one Claude/OpenAI compatibility matrix, then verify whether the Trae v3 Claude Code / Claude SDK tool loop is actually usable.

The matrix suite has been added in `internal/runtime/executor/executor_e2e_suite_test.go`. Trae and Lingma e2e files now expose provider target factories. Trae v3 default e2e model has been changed from `glm-5` to `glm-4.7` because `glm-5` queues too often.

## Current State

- `trae-v3` target default is now `glm-4.7`.
- `TestExecutorE2E_CompatibilityMatrix/trae-v3/glm-4.7/Claude_Read_tool_use` passed live with `-count=1`.
- `TestExecutorE2E_CompatibilityMatrix/trae-v3/glm-4.7/Claude_workspace_inspection_tool_use` passed live with `-count=1`.
- These prove the first Claude turn can return valid Claude `tool_use` events for v3.
- This does **not** fully prove Claude Code / Claude SDK agent loops are equivalent, because a real loop also needs `tool_result` submission using the exact `tool_use.id` returned by the first turn.

## Important Finding

Trae v3 tool result submission is special:

- First turn returns a `trae_...` encoded tool id containing session/conversation/task/agent/native tool state.
- Second turn with a tool result is translated into `commit_toolcall_result`.
- The result of `commit_toolcall_result` may not contain the final assistant answer directly.
- It may only accept/write the tool result, emit history/status events, or return `required_context: pending_message`.
- Therefore an e2e test must not require final user-facing content to be returned directly from the commit response.

Current executor behavior includes a fallback for v3 commit streams:

- If the commit stream has no content delta, it emits `"Tool result received."`.
- That proves the stream completed and the tool result path did not fail, but it does not prove the full agent task produced a final README summary or git analysis.

## Live Validation Notes

Commands run:

```bash
GOCACHE=$PWD/.tmp-gocache go test -count=1 -v -tags=e2e -run '^TestExecutorE2E_CompatibilityMatrix$/^trae-v3$/^glm-4\.7$/^Claude_' ./internal/runtime/executor
```

Result:

- `Claude_Read_tool_use`: passed.
- `Claude_workspace_inspection_tool_use`: passed.
- `Claude_tool_result_follow_up`: was skipped before v3 capability was enabled.

Additional v3 commit checks:

- `TestTraeE2E_V3_ToolCommitFallbackDeltaViaExecutor` passed after fixing the test to drain the full initial stream before committing. The earlier failure was `failed to get agent state: redis: nil`, likely caused by committing before upstream agent state was fully persisted.
- `TestTraeE2E_V3_ToolCommitFlow` passed live, but can take a long time and may emit progress/status/history rather than final content.
- `TestTraeE2E_V3_BeijingWeatherMockToolCommitViaExecutor` exposed that `glm-4.7` can emit a placeholder `tool_name` before the real MCP tool; the test was updated to select the target tool name. A later run hung in the commit stream, so this is not a stable mandatory verification path.

## Code Changes Already Made

- Added shared e2e matrix file:
  - `internal/runtime/executor/executor_e2e_suite_test.go`
- Added Trae e2e target factory:
  - `trae-v1/<model>`, default `deepseek-R1`
  - `trae-v3/<model>`, default `glm-4.7`
- Added Lingma e2e target factory:
  - env `LINGMA_E2E_MODEL`, else first fetched model, else `qwen-2.5-max`
- Expanded Lingma auth lookup to support:
  - `LINGMA_E2E_AUTH_FILE`
  - `auths/lingma-*.json`
  - repo-root `lingma-*.json`
- Removed duplicate high-level Trae/Lingma basic e2e cases in favor of the matrix.
- Kept provider-specific raw/debug/commit tests.
- Changed Trae v3 target `SupportsToolResultFollowUp` from `false` to `true`, but the shared follow-up scenario still needs to be rewritten to use a real first-turn tool id.

## Next Step

Rewrite `runExecutorE2EClaudeToolResultFollowUp` in `internal/runtime/executor/executor_e2e_suite_test.go`:

1. First turn:
   - Ask the target to use `Read` on the repo `README.md`.
   - Use Claude format.
   - Collect the real returned `tool_use` id, name, and input.
   - Validate name is `Read`, id is valid, and input has `file_path`.

2. Execute mock/local tool result inside the test:
   - Read a small prefix of the requested file if it is inside the repo or use a deterministic README snippet.
   - Keep payload small.

3. Second turn:
   - Send Claude messages containing:
     - original user request
     - assistant `tool_use` block with the real id/name/input
     - user `tool_result` block with the same id and content
   - For Trae v3 this should naturally hit `commit_toolcall_result` because the id is real and contains task/agent state.

4. Assertions:
   - No setup error and no stream error.
   - Explicitly fail on 4001, 503, `redis: nil`, and other commit-state errors.
   - Claude stream remains hygienic: no raw OpenAI chunks, no `tool_calls=` residue.
   - Accept either:
     - non-empty assistant text, including the current `"Tool result received."` fallback, or
     - a new valid `tool_use`.
   - Do **not** require final README summary content from the commit response, because v3 may require pending-message continuation outside `commit_toolcall_result`.

5. Re-run targeted live checks:

```bash
GOCACHE=$PWD/.tmp-gocache go test -count=1 -v -tags=e2e -run '^TestExecutorE2E_CompatibilityMatrix$/^trae-v3$/^glm-4\.7$/^Claude_tool_result_follow_up$' ./internal/runtime/executor
GOCACHE=$PWD/.tmp-gocache go test -count=1 -v -tags=e2e -run '^TestExecutorE2E_CompatibilityMatrix$/^trae-v3$/^glm-4\.7$/^Claude_' ./internal/runtime/executor
```

Then run normal verification:

```bash
GOCACHE=$PWD/.tmp-gocache go test ./internal/runtime/executor
GOCACHE=$PWD/.tmp-gocache go test -tags=e2e -run '^$' ./internal/runtime/executor
GOCACHE=$PWD/.tmp-gocache go build -o /private/tmp/cli-proxy-api-test-output ./cmd/server
rm /private/tmp/cli-proxy-api-test-output
```

## Caveats

- Some live v3 e2e runs are slow or unstable due to upstream queue/progress behavior.
- Use `-count=1` for live e2e verification to avoid Go test cache confusion.
- Network calls require elevated execution because the local proxy at `127.0.0.1:7897` is blocked by the default sandbox.
- Do not claim full Claude Code agent-task equivalence until the real first-turn `tool_use` -> second-turn `tool_result` loop passes for v3.
