# Lingma and Trae Provider Plugins

This module contains independently versioned dynamic provider plugins for
Lingma and Trae. Version `0.2.0` is a shadow release candidate: the native
providers remain available and unchanged while the plugins are validated.

| Provider | Native ID | Shadow plugin ID | Status |
| --- | --- | --- | --- |
| Lingma | `lingma` | `lingma-plugin` | Offline parity complete; live test opt-in |
| Trae | `trae` | `trae-plugin` | V1/V2/V3, tools, streaming, browser login, and offline parity complete; live test opt-in |

The different IDs are deliberate. They let this branch run alongside the main
branch and prevent an unfinished plugin from replacing a working native
provider. No native code is removed until the cutover gates in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) pass.

## Discovery and configuration

The host scans the configured plugin directory for platform libraries. A
versioned filename such as `trae-plugin-v0.2.0.dylib` is discovered as plugin
ID `trae-plugin`; when several compatible versions are present, the host
selects the configured or newest compatible version.

```yaml
plugins:
  enabled: true
  dir: "provider-plugins/bin"
  configs:
    lingma-plugin:
      enabled: true
      priority: 10
    trae-plugin:
      enabled: true
      priority: 10
```

Keep the native auth files as `type: lingma` and `type: trae`. Shadow plugin
credentials use `type: lingma-plugin` and `type: trae-plugin`, so both paths can
be exercised independently.

## Build and test

Go 1.26 and a C compiler are required.

```bash
make -C provider-plugins build
make -C provider-plugins test
make -C provider-plugins package
```

`build` writes versioned shared libraries to `provider-plugins/bin/`. `package`
creates plugin-store-compatible ZIP files and `checksums.txt` in
`provider-plugins/dist/`.

The root host integration suite builds and loads the actual shared libraries:

```bash
go test ./internal/pluginhost
```

Default tests are offline and use synthetic credentials only.

## Credentials

Export local Lingma credentials for the shadow plugin:

```bash
go run ./cmd/lingma-export -plugin
```

Export or interactively acquire Trae credentials for the shadow plugin:

```bash
go run ./cmd/trae-export -plugin
go run ./cmd/trae-export -plugin -login
```

Without `-plugin`, both commands continue to produce native-provider
credentials. The Trae plugin also implements the management API browser login
flow at the dynamically exposed `trae-plugin-auth-url` endpoint.

## Opt-in live tests

Live tests read only the exact credential path supplied by the operator. They
never search local auth directories. Set one or both paths:

```bash
CLIPROXY_LINGMA_PLUGIN_LIVE_AUTH_FILE=/absolute/path/lingma.json \
CLIPROXY_LINGMA_PLUGIN_LIVE_MODEL=gm51model \
go test ./internal/pluginhost -run TestLingmaAndTraeDynamicLibrariesLoadAndExecuteThroughHostCallbacks

CLIPROXY_TRAE_PLUGIN_LIVE_AUTH_FILE=/absolute/path/trae.json \
CLIPROXY_TRAE_PLUGIN_LIVE_MODEL=glm-5 \
go test ./internal/pluginhost -run TestLingmaAndTraeDynamicLibrariesLoadAndExecuteThroughHostCallbacks
```

The test converts the credential type in memory, performs one non-streaming
request and one streaming request, and does not write the supplied file.

## Release and rollback

Release archives follow the plugin-store naming contract:

```text
<plugin-id>_<version>_<goos>_<goarch>.zip
```

Pushing a pre-1.0 `v<version>` tag (for example `v0.2.0`) runs the cross-platform release
workflow for Darwin arm64, Linux amd64, and Windows amd64 and publishes
`checksums.txt`. Each archive contains an unversioned library at its root, as
required by the official plugin store. The files under [manifests](manifests/)
are fixed-version installation and validation manifests; they are not the
official store registry entries.

The official store reads the latest GitHub release from the repository in
`registry.json`. Do not submit the current monorepo URL as the store repository:
a later CLIProxyAPI core release would become `latest` and hide the plugin
release. Before store submission, publish both plugins from a dedicated release
repository (or one repository per plugin), then open a store pull request that
adds the two metadata entries to `registry.json`.

To roll back, disable the shadow plugin entry (or remove its artifact), retain
the native credential file, and continue routing through `lingma` or `trae`.
The branch intentionally keeps both native providers available.
