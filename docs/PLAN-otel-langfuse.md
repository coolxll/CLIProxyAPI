# Implementation Plan - OTel & Langfuse Integration

## Context
This plan details the completed implementation of OpenTelemetry (OTel) observability and Langfuse integration for CLIProxyAPI. The goal was to provide full request tracing, usage monitoring, and seamless integration with the Langfuse observability platform.

## Completed Changes

### 1. Dependency Management
- **Action**: Added required Go OTel SDK libraries.
- **Verification**: `go.mod` and `go.sum` updated with:
  - `go.opentelemetry.io/otel`
  - `go.opentelemetry.io/otel/sdk`
  - `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`
  - `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`
  - `go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin`

### 2. Telemetry Core (`internal/telemetry/otel.go`)
- **Action**: Created a new `telemetry` package.
- **Implementation**:
  - `Init()` function configures the global TracerProvider.
  - Uses `OTEL_SDK_DISABLED` env var to disable completely.
  - Uses `OTEL_EXPORTER_OTLP_ENDPOINT` (default `127.0.0.1:4318`) for OTLP HTTP export.
  - Configures standard resource attributes (`service.name`, `service.version`).

### 3. Application Startup (`internal/cmd/run.go`)
- **Action**: Integrated telemetry initialization into the main service startup flow.
- **Implementation**:
  - Calls `telemetry.Init("CLIProxyAPI")` before service build.
  - Defers shutdown to flush traces on exit.

### 4. Server Middleware (`internal/api/server.go`)
- **Action**: Added OTel tracing middleware to the Gin server.
- **Implementation**:
  - Registered `telemetry.GinMiddleware("CLIProxyAPI")` as the **first** middleware to capture the full request lifecycle.

### 5. Context Propagation (`sdk/api/handlers/handlers.go`)
- **Action**: Ensured trace context propagates from the API layer to internal executors.
- **Implementation**:
  - Updated `GetContextWithCancel` to extract the span from the Gin context and embed it into the `context.Context` used by downstream handlers.

### 6. HTTP Client Instrumentation (`internal/runtime/executor/proxy_helpers.go`)
- **Action**: Wrapped outbound HTTP clients with OTel instrumentation.
- **Implementation**:
  - Used `telemetry.WrapTransport()` to wrap the underlying `http.RoundTripper` (whether it's a proxy, default, or custom transport).
  - This ensures upstream calls (to OpenAI, Anthropic, etc.) appear as child spans.

### 7. Usage Attributes (`internal/runtime/executor/usage_helpers.go`)
- **Action**: Enriched traces with GenAI usage data.
- **Implementation**:
  - In `publishWithOutcome`, attributes are added to the current span:
    - `gen_ai.system` (provider name)
    - `gen_ai.request.model`
    - `gen_ai.usage.input_tokens`
    - `gen_ai.usage.output_tokens`

### 8. Log Correlation (`internal/logging/global_logger.go`, `internal/runtime/executor/logging_helpers.go`)
- **Action**: Injected Trace IDs into application logs.
- **Implementation**:
  - Updated `LogFormatter` to extract `trace_id` from the context.
  - Appended `|trace=xxxxxxxx` to the request ID field in logs for easy correlation between logs and traces.

### 9. Infrastructure & Configuration
- **Action**: Added Docker Compose setup for Langfuse.
- **Files**:
  - `docker-compose.langfuse.yml`: Defines Langfuse server, Postgres, and OTel Collector services.
  - `otel-collector.langfuse.yml`: Configures OTel Collector to receive OTLP and export to Langfuse.
  - `.env.langfuse.example`: Template for environment variables (requires manual Base64 generation for keys).

### 10. Documentation
- **Action**: Created comprehensive documentation.
- **Files**:
  - `docs/observability-otel-langfuse_CN.md` (Chinese): Detailed setup guide, including Windows-specific instructions for PowerShell.
  - `docs/observability-otel-langfuse.md` (English): Quick start guide.

## Verification Steps

1.  **Start Infrastructure**:
    ```bash
    cp .env.langfuse.example .env.langfuse
    # Fill in LANGFUSE_AUTH_BASE64 in .env.langfuse
    docker-compose -f docker-compose.langfuse.yml --env-file .env.langfuse up -d
    ```

2.  **Run Application**:
    ```powershell
    # Windows PowerShell
    $env:OTEL_EXPORTER_OTLP_ENDPOINT="127.0.0.1:4318"
    ./CLIProxyAPI
    ```

3.  **Generate Traffic**:
    ```bash
    curl http://localhost:8317/v1/chat/completions ...
    ```

4.  **Verify**:
    - **Logs**: Check console output for `|trace=...` in log entries.
    - **Langfuse**: Open http://localhost:18888 and check "Traces" for the request. Verify "Generations" show token usage.
