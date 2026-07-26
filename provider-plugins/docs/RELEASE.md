# Provider Plugin Release Checklist

## Before tagging

1. Update `Version` in both provider packages, `VERSION` in the Makefile, and
   both files under `manifests/`.
2. Run `gofmt -w .` at the repository root.
3. Run the provider module tests, host dynamic-library integration tests, race
   tests, and the required server compile check.
4. Run the opt-in live tests with explicitly supplied Lingma and Trae
   credential paths. Do not use auto-discovery in tests.
5. Confirm the native provider IDs and auth files still work. Do not change the
   shadow IDs before a separately reviewed cutover.

## Publishing

Create and push an annotated pre-1.0 tag named `v<version>`. The
release workflow builds native C shared libraries and packages the exact
archive names expected by the plugin store:

```text
lingma-plugin_<version>_<goos>_<goarch>.zip
trae-plugin_<version>_<goos>_<goarch>.zip
```

The release must contain all platform archives plus `checksums.txt`. Validate
one archive on each supported operating system by installing it into an empty
plugin directory and checking registration, auth parsing, model discovery,
non-streaming execution, streaming execution, cancellation, and reload.

Each ZIP must contain exactly one dynamic library at its root, named
`lingma-plugin.<extension>` or `trae-plugin.<extension>` without a version in
the filename. The installer writes the versioned destination name.

The files under `manifests/` pin a release tag for local installation and
validation. They are not copied into the official store. The official store
accepts a pull request that changes only its `registry.json` and points each
entry at the plugin's GitHub repository.

Do not use the CLIProxyAPI monorepo as that repository. The store resolves the
repository's latest release, so a later core release would supersede the plugin
release. Publish the two plugins from a dedicated release repository (both may
share one repository if every release contains both complete asset sets), then:

1. Create the public `v<version>` release and verify all archives and
   `checksums.txt`.
2. Fork `router-for-me/CLIProxyAPI-Plugins-Store`.
3. Add `lingma-plugin` and `trae-plugin` metadata entries to `registry.json`.
4. Open a pull request containing the repository URL, release tag, asset
   evidence, and a short capability description.

The upstream pull request remains intentionally deferred until the dedicated
release repository exists and the opt-in live tests pass.

## Rollback

1. Disable `lingma-plugin` and `trae-plugin` in `plugins.configs`.
2. Remove only the affected versioned artifact from the configured plugin
   directory.
3. Keep or restore the native `lingma` and `trae` auth files.
4. Restart or reload configuration and verify routing uses the native provider
   IDs.

Because version `0.2.0` uses shadow IDs, rollback does not require rewriting
native credentials or reverting the main branch.
