# CLIProxyAPI Provider Plugins

This directory contains the migration workspace for fork-specific providers
that will be delivered as CLIProxyAPI dynamic plugins.

Current status:

- Lingma: M0 shadow registration and synthetic credential parsing.
- Trae: M0 shadow registration and synthetic credential parsing.

The current plugin provider identifiers deliberately differ from the native
providers. This prevents an incomplete development plugin from replacing a
working native executor:

```text
native: lingma       plugin: lingma-plugin
native: trae         plugin: trae-plugin
```

See [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) for the migration and
cutover gates.

## Build

```bash
make -C provider-plugins build
```

The versioned shared libraries are written to `provider-plugins/bin/`.

## Test

```bash
make -C provider-plugins test
```

Default tests are offline and use synthetic credentials only.

## Development configuration

Do not enable the shadow plugin for production traffic. During development,
copy the artifact into a dedicated plugin directory and configure its artifact
ID:

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

The M0 implementations parse only credentials whose `type` is
`lingma-plugin` or `trae-plugin`; they intentionally ignore native credentials.
