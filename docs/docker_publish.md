# CLIProxyAPI Docker Image Build & Deploy

## Overview

Images are published to **GitHub Container Registry (GHCR)** via GitHub Actions. The workflow triggers on `v*` tags and produces multi-arch images (amd64 + arm64).

Registry: `ghcr.io/coolxll/cliproxyapi`

## Release Workflow

### 1. Merge to main

```bash
git checkout main
git merge <feature-branch> --no-ff -m "Merge branch '<feature-branch>' into main"
git push origin main
```

### 2. Tag to trigger CI build

The `docker-image` workflow (`.github/workflows/docker-image.yml`) triggers on `v*` tags or manual dispatch.

```bash
git tag v<YYYY.MM.DD>    # e.g. v2026.05.23
git push origin v<YYYY.MM.DD>
```

This builds and pushes:
- `ghcr.io/coolxll/cliproxyapi:latest` (multi-arch manifest)
- `ghcr.io/coolxll/cliproxyapi:v<YYYY.MM.DD>` (multi-arch manifest)
- `ghcr.io/coolxll/cliproxyapi:latest-amd64` / `latest-arm64`
- `ghcr.io/coolxll/cliproxyapi:v<YYYY.MM.DD>-amd64` / `-arm64`

### 3. Monitor build

```bash
# List recent runs (note: --repo required for fork)
gh run list --workflow docker-image.yml --repo coolxll/CLIProxyAPI --limit 3

# Watch a specific run
gh run watch <run_id> --repo coolxll/CLIProxyAPI
```

### 4. Deploy to la-tri

SSH alias `la-tri` is in `~/.ssh/config`. Compose project at `/opt/app/cli-proxy-api/`.

```bash
# Backup current image
ssh la-tri "docker tag ghcr.io/coolxll/cliproxyapi:latest ghcr.io/coolxll/cliproxyapi:rollback-$(date +%Y%m%d)"

# Pull & restart
ssh la-tri "cd /opt/app/cli-proxy-api && docker compose pull && docker compose up -d"

# Verify
ssh la-tri "docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}' | grep cli"
ssh la-tri "docker logs cliproxyapi --tail 20"
```

### 5. Rollback (if needed)

```bash
ssh la-tri "docker tag ghcr.io/coolxll/cliproxyapi:rollback-YYYYMMDD ghcr.io/coolxll/cliproxyapi:latest && cd /opt/app/cli-proxy-api && docker compose up -d"
```

## Server Details (la-tri)

| Item | Value |
|------|-------|
| Compose file | `/opt/app/cli-proxy-api/docker-compose.yml` |
| Container name | `cliproxyapi` |
| Port | `127.0.0.1:8317:8317` |
| Network | `proxy-network` (external) |
| Config volume | `./config.yaml:/CLIProxyAPI/config.yaml` |
| Auth volume | `./.cli-proxy-api:/root/.cli-proxy-api` |

## Manual Build (fallback)

If CI is unavailable, use `docker-publish.ps1` for local Docker Hub push:

```powershell
.\docker-publish.ps1
```

This builds locally and pushes to `coolxll/cli-proxy-api` on Docker Hub.
