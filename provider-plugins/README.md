# Lingma Provider Plugin

This module contains the independently buildable Lingma provider plugin. It
uses the shadow provider ID `lingma-plugin`, so the native `lingma` provider
remains available as a rollback path.

## Build and test

Go 1.26, CGO, and a C compiler are required.

```bash
make -C provider-plugins test
make -C provider-plugins build
```

The versioned shared library is written to `provider-plugins/bin/`.

Run the real dynamic-library host integration test separately because a Go
`c-shared` library owns an independent runtime:

```bash
go test -tags providerplugins -run TestLingmaDynamicLibraryLoadsAndExecutesThroughHostCallbacks ./internal/pluginhost
```

## Configure

```yaml
plugins:
  enabled: true
  dir: "provider-plugins/bin"
  configs:
    lingma-plugin:
      enabled: true
      priority: 10
```

Export an existing Lingma credential for the shadow provider:

```bash
go run ./cmd/lingma-export -plugin
```

The generated credential uses `type: lingma-plugin`. Existing credentials with
`type: lingma` continue to use the native provider.

## Package

```bash
make -C provider-plugins package
```

The package is written to `provider-plugins/dist/` using the plugin-store asset
naming convention.
