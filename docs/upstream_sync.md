# Syncing the Fork with Upstream

The fork keeps a short downstream patch stack on top of `upstream/main`.
Upstream is fetch-only for normal maintenance; rewritten downstream history is
pushed only to `origin/main`.

## Sync workflow

Start from a clean `main` branch, then run:

```bash
git fetch upstream
git fetch origin
git rebase upstream/main
go test ./...
go build -o test-output ./cmd/server
rm test-output
git push --force-with-lease origin main
```

When a conflict occurs, resolve it in favor of the current upstream structure
while preserving the downstream Lingma, Trae, and provider-plugin behavior.
Stage the resolution and continue with `git rebase --continue`. Use
`git rebase --abort` to return to the pre-sync state.

Before a major rewrite, push a dated backup branch to `origin`. Never push the
fork's downstream commits to `upstream`.

## Commits that cannot be upstreamed

Some downstream commits fix problems that only exist because of downstream API
extensions. They cherry-pick cleanly — textually — and still cannot be submitted,
because they do not compile against upstream. Check by compiling, not by reading
the diff.

### `15091d7d` — prefer plugin-reported usage over frame-derived usage

The fix adds `StreamUsageBuffer.ObserveNative` so usage a plugin reports for a
request outranks usage the host re-derives from translated stream frames. The
plugin's own accounting follows provider-native semantics; a frame re-read
through another protocol's semantics is not equivalent (Claude frames report
cache-*exclusive* `input_tokens`, which inflates the published cache read rate).

It depends on two symbols that exist only in this fork, both introduced by
`fbeeeb10 feat(plugins): add Lingma provider plugin integration`:

- `pluginapi.ExecutorStreamChunk.Usage` — upstream's chunk carries only `Payload`
  and `Err`; `pluginapi.ExecutorResponse.Usage` is likewise absent upstream.
- `pluginUsageDetailToCore` (`internal/pluginhost/adapters_executors.go`) — the
  conversion helper the fix calls.

Cherry-picking onto `upstream/main` applies without conflict and then fails:

```
internal/pluginhost/adapters_executors.go:843:31: undefined: pluginUsageDetailToCore
internal/pluginhost/adapters_executors.go:843:61: chunk.Usage undefined
    (type pluginapi.ExecutorStreamChunk has no field or method Usage)
```

**Upstream cannot hit this bug.** Its plugin executors have no native usage
channel at all — a plugin returns payload bytes and the host derives usage from
frames, so there is no precedence conflict to resolve. The bug is a consequence
of this fork's extension, and the fix belongs here.

To re-verify at any sync:

```bash
git worktree add /tmp/upstream-check upstream/main
cd /tmp/upstream-check
git cherry-pick --no-commit 15091d7d
go build ./internal/pluginhost/
```

If the plugin usage-reporting API is ever offered upstream, revisit this: the
API extension and this fix would then be submitted in that order, extension
first. Until then `ObserveNative` stays a downstream-only symbol. That is also
what makes it a reliable marker for "does this build carry the fix": check with
`grep -a -o -E 'StreamUsageBuffer\)\.[A-Za-z]+' <binary> | sort -u`, which works
on published builds because Go's reflect method-name tables survive
`-ldflags="-s -w"`.

## Major Go Module Version Bumps (e.g. v7 -> v8)

When upstream bumps the major version path (e.g., `github.com/router-for-me/CLIProxyAPI/v8`):

1. **Go Internal Package Visibility Rule**:
   Go strictly prohibits importing an `internal/` package from an ancestor whose module path does not match. If `provider-plugins/go.mod` or any subpackage declares `.../v7`, attempting to import `.../v8/internal/...` triggers:
   ```text
   use of internal package github.com/router-for-me/CLIProxyAPI/v8/internal/... not allowed
   ```
   All downstream packages and plugins must bump their module declarations and import paths in lockstep:
   ```bash
   find . -type f -name "*.go" -exec sed -i '' 's#github.com/router-for-me/CLIProxyAPI/v7#github.com/router-for-me/CLIProxyAPI/v8#g' {} +
   # Update provider-plugins/go.mod
   ```

2. **Common Upstream Sync Conflict Spots**:
   - `internal/thinking/`: Check newly added upstream providers and appliers. Ensure downstream additions (e.g., Lingma thinking options) cleanly plug into `ApplyThinking()`.
   - `internal/runtime/executor/`: Check for duplicate utility functions (e.g., package-level helpers like `firstNonEmpty` in `trae_common.go` vs `devin_executor.go`).
   - `internal/pluginhost/`: Upstream enforces `hostCallbackInstance` isolation on all plugin callbacks. Downstream HTTP callback wrappers (`openCallbackContextForPluginInstanceHTTP` and `openHostHTTPCallbackContext`) must pass `a.instance` so that plugin Cgo callbacks are not rejected with `host callback ID does not belong to the calling plugin instance`.
   - Downstream-only tests: Update or retire tests that assert obsolete model filtering (e.g., `codex-free` definitions).
   - `AGENTS.md` & `config.example.yaml`: Preserve downstream architectural rules, conventions, and config additions.

3. **Post-Merge Model Catalog Refresh**:
   Upstream expects model definitions in `internal/registry/models.json` and `model_definitions.go` to match the latest provider models. Always run:
   ```bash
   ./.github/scripts/refresh-model-catalogs.sh
   ```
   Verify and commit updated catalogs if any definitions changed.

## Publishing and Homelab Deployment Workflow

After merging and pushing to `origin/main`:

1. **Tag and Push Docker Image**:
   ```bash
   git tag vYYYY.MM.DD
   git push origin vYYYY.MM.DD
   ```
   Monitor GitHub Actions workflow `docker-image.yml`:
   ```bash
   gh run list --workflow=docker-image.yml --repo coolxll/CLIProxyAPI --limit 1
   gh run view <run-id> --repo coolxll/CLIProxyAPI
   ```

2. **Extract Manifest Digest**:
   The multi-arch manifest step pushes the final manifest. Extract the `sha256:...` digest from the `docker_manifest` step logs or test pull on a remote host:
   ```bash
   gh run view --job=<manifest-job-id> --repo coolxll/CLIProxyAPI --log | grep -E "pushing sha256:[a-f0-9]+ to ghcr.io"
   ```

3. **Update Declarative Homelab Stacks**:
   In the homelab infrastructure repository (`homelab-infra`):
   - Update `hosts/<host>/stacks/<cpa-stack>/compose.yaml` with the new digest:
     `image: ghcr.io/coolxll/cliproxyapi@sha256:<digest>`
   - Commit and push to `homelab-infra`'s `main` branch.

4. **Rollout to Homelab Hosts**:
   Deploy across all homelab instances via SSH:
   ```bash
   ssh <host> "cd <homelab-infra-path> && git pull --ff-only && cd hosts/<host>/stacks/<cpa-stack> && sudo docker compose pull cliproxyapi && sudo docker compose up -d cliproxyapi"
   ```
   **Key Host Considerations**:
   - **File Permissions**: Files like `.env` or `app.env` are often mode `0600 root:root`. Always run `sudo docker compose` if unprivileged compose complains with permission denied.
   - **Local Host Git State**: If a remote host diverged or has unpushed commits, back up the commit on a temporary branch (`git branch backup/<name> main`) before resetting to `origin/main`.
   - **Mounted Plugins**: Built-in plugins inside the Docker image can be overridden by host-mounted volumes (`/CLIProxyAPI/plugins/...`). Verify that mounted `.so` binaries remain ABI-compatible or rebuild them if necessary.

