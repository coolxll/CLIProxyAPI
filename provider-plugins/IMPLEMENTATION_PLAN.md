# Lingma and Trae Provider Plugin Migration Plan

## Objective

Move the fork-specific Lingma and Trae providers out of the CLIProxyAPI core and
ship them as independently versioned dynamic provider plugins.

The migration must preserve the existing OpenAI and Claude compatibility
behavior, streaming semantics, account refresh, model discovery, usage
reporting, and provider-specific recovery logic.

## Non-goals

- Hosting MCP or arbitrary public HTTP services from these plugins.
- Changing Lingma or Trae upstream behavior while migration is in progress.
- Removing the native providers before side-by-side parity is demonstrated.
- Reading credentials from developer worktrees during automated tests.

## Safety strategy

Migration uses shadow provider identifiers first:

```text
lingma-plugin
trae-plugin
```

The existing native identifiers remain active:

```text
lingma
trae
```

This permits the same sanitized fixtures and opt-in live accounts to exercise
both implementations. The plugin identifiers change to the native names only
during the final cutover.

No credential file from a developer machine may be committed, copied into a
fixture, printed, or read by a default test target.

## Source inventory

### Lingma

The native implementation currently spans:

- credential parsing, signing, exchange, and refresh;
- dynamic model discovery;
- OpenAI and Claude request translation;
- OpenAI and Claude streaming response translation;
- SSE-only upstream aggregation;
- forced HTTP/1.1 transport;
- reasoning fallback and same-request upstream recovery;
- usage restoration and cached-token handling;
- CLI export and Management API import.

The implementation and tests are approximately 5,200 lines.

### Trae

The native implementation currently spans:

- JWT, machine, device, workspace, and refresh-token state;
- credential export and browser-assisted login;
- dynamic and fallback model discovery;
- V1, V2, and V3 request construction;
- encrypted request and response payloads;
- raw chat and agent task modes;
- tool-call shims and stream reconstruction;
- usage extraction and compatibility responses.

The implementation and tests are approximately 10,100 lines.

## Target repository layout

```text
provider-plugins/
├── cmd/
│   ├── lingma/
│   └── trae/
├── internal/
│   ├── pluginruntime/
│   ├── lingma/
│   └── trae/
├── testdata/
│   ├── lingma/
│   └── trae/
├── Makefile
└── README.md
```

The migration begins in this repository so parity tests can reuse sanitized
fixtures. Once stable, each provider can be released from an independent
repository without changing the C ABI contract.

## Capability mapping

| Native responsibility | Plugin capability |
| --- | --- |
| Credential import and refresh | `AuthProvider` |
| Static model metadata | `ModelRegistrar` / `ModelProvider.StaticModels` |
| Per-account model discovery | `ModelProvider.ModelsForAuth` |
| Provider request execution | `ProviderExecutor` |
| OpenAI and Claude request conversion | `RequestTranslator` |
| Provider response conversion | `ResponseTranslator` |
| Reasoning configuration | `ThinkingApplier` |
| Import and login commands | `CommandLinePlugin` |
| Status and diagnostics | `ManagementAPI` |
| Request accounting | standard translated usage plus `UsagePlugin` diagnostics |

## Required host changes

### P0: HTTP transport hints (implemented)

Lingma must be able to request an HTTP/1.1-only upstream transport while still
using the host proxy policy, request logging, cancellation, and stream bridge.

The branch adds a backward-compatible transport field to
`pluginapi.HTTPRequest`:

```go
type HTTPTransportOptions struct {
    ForceHTTP11 bool
}
```

The host callback bridge must apply the option through the existing bounded
HTTP/1.1 transport cache. Tests must cover non-streaming and streaming calls.

### P1: Provider-aware host HTTP callbacks

Host HTTP callbacks should preserve the originating plugin provider and auth
identity in request logs. If the existing callback context is insufficient,
extend the callback request with provider and auth identifiers without exposing
credential material.

### P1: Streaming backpressure validation

Trae can produce long-lived streams and fragmented encrypted frames. Validate
that `host.http.do_stream`, `host.http.stream_read`, `host.stream.emit`, and
`host.stream.close` propagate cancellation and do not buffer an entire stream.

No other blocking ABI gap is known at plan creation time.

## Delivery phases

### M0: Branch, plan, and buildable skeleton

- [x] Create a dedicated migration branch.
- [x] Record scope, safety rules, ABI gaps, and acceptance criteria.
- [x] Add a standalone provider-plugin module and build target.
- [x] Add a Lingma shadow plugin registration and credential parser.
- [x] Add the Trae shadow plugin registration and credential parser.

Exit criterion: both skeletons build as C shared libraries and their unit tests
do not require network access or real credentials.

### M1: Lingma authentication and model discovery

- [x] Move credential storage types and validation into the plugin.
- [x] Implement imported credential parsing under `lingma-plugin`.
- [x] Implement refresh through `AuthProvider.RefreshAuth`.
- [x] Add the HTTP/1.1 host transport option.
- [x] Implement per-auth model discovery using host HTTP callbacks.
- [x] Port sanitized model-list fixtures and golden tests.

Exit criterion: a shadow Lingma credential refreshes and publishes the same
models as the native implementation. ✓

### M2: Lingma translation and execution

- [x] Port OpenAI chat-completions request translation.
- [x] Port Claude request translation.
- [x] Port provider response and SSE translation.
- [x] Implement non-streaming aggregation and true streaming execution.
- [x] Port reasoning behavior, cached-token restoration, and usage parsing.
- [x] Port one-shot thinking fallback and same-request recovery.
- [x] Run native-versus-plugin golden parity tests.

Exit criterion: request payloads, stream events, terminal errors, and usage are
equivalent for the supported protocol matrix. ✓

### M3: Lingma cutover

- Add opt-in live E2E tests guarded by explicit environment variables.
- Change the plugin provider identifier from `lingma-plugin` to `lingma`.
- Remove native Lingma executor, auth, translator, config, and service cases.
- Keep a compatibility import path for existing auth JSON.
- Document rollback to the last native-provider release.

Exit criterion: the main server contains no Lingma provider implementation and
the plugin passes the full compatibility suite.

### M4: Trae authentication and model discovery

- Add the Trae shadow plugin and sanitized credential parser.
- Port token exchange, refresh, browser login, and import commands.
- Port dynamic model discovery and static fallback definitions.
- Verify multi-account selection and auth persistence.

Exit criterion: `trae-plugin` can authenticate and publish the same model set as
the native provider.

### M5: Trae protocol and executor migration

- Port V1/V2/V3 request builders.
- Port encryption and frame decoding.
- Port raw chat, agent tasks, stream reconstruction, and terminal handling.
- Port tool-call shims and supported tool behavior.
- Port usage extraction and compatibility response conversion.
- Run native-versus-plugin golden and opt-in live parity tests.

Exit criterion: all existing Trae unit fixtures pass through the plugin without
semantic differences.

### M6: Trae cutover and release hardening

- Change the provider identifier from `trae-plugin` to `trae`.
- Remove native Trae code and hard-coded service cases.
- Produce versioned artifacts for Darwin, Linux, and Windows.
- Add checksums, plugin-store manifests, upgrade notes, and rollback notes.
- Verify plugin hot reload and version selection.

Exit criterion: both providers are independently installable and the core can
track upstream CLIProxyAPI without provider-specific merge conflicts.

## Test matrix

### Offline tests

- Registration schema and capability declarations.
- Credential parsing with fully synthetic values.
- Secret redaction and stable auth IDs.
- Static and dynamic model normalization.
- Request golden tests for every supported client protocol.
- Response and streaming golden tests for every upstream protocol version.
- Cancellation, early EOF, retryable failure, and malformed frame behavior.
- Usage and cached-token preservation.
- Plugin reconfiguration and shutdown.

### Integration tests

- Plugin discovery by versioned artifact name.
- Auth-file parsing and persistence through the host.
- Model registration through the host registry.
- Non-streaming execution through the host HTTP callback.
- Streaming execution with cancellation and backpressure.
- Native-versus-plugin parity while shadow identifiers are active.

### Live tests

Live tests are opt-in and must require explicit environment variables. They may
read only the path named by the test operator and must never discover local
credential files automatically.

## Cutover rules

Native code is removed only after all of the following are true for a provider:

1. Offline golden parity is complete.
2. Host integration tests pass with a versioned dynamic library.
3. At least one opt-in live account passes non-streaming and streaming calls.
4. Existing credential JSON is accepted without manual secret transformation.
5. Plugin disablement cleanly removes models and executors.
6. A documented rollback path exists.

## Immediate next work

1. Finish the Trae M0 skeleton.
2. Move Lingma credential signing into the plugin module.
3. Implement host-callback-backed Lingma model discovery.
