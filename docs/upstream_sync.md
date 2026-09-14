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
