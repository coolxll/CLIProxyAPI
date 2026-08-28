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
