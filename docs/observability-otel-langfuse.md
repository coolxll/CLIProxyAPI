# Observability with OpenTelemetry & Langfuse

CLIProxyAPI has built-in support for OpenTelemetry (OTel), allowing you to trace requests and analyze usage with compatible backends like [Langfuse](https://langfuse.com/).

## Quick Start (Docker Compose)

We provide a ready-to-use Docker Compose setup for Langfuse + OpenTelemetry Collector.

1.  **Prepare configuration**:
    ```bash
    cp .env.langfuse.example .env.langfuse
    ```
    *   Start Langfuse first to generate keys: `docker-compose -f docker-compose.langfuse.yml up -d langfuse-server db`
    *   Open http://localhost:18888, create an account and a project.
    *   Get **Public Key** and **Secret Key** from the project settings.
    *   Generate the Base64 auth string:
        *   **Linux/Mac**: `echo -n "pk-lf-xxxx:sk-lf-xxxx" | base64`
        *   **PowerShell**: `[Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes("pk-lf-xxxx:sk-lf-xxxx"))`
    *   Paste the result into `.env.langfuse` as `LANGFUSE_AUTH_BASE64`.

2.  **Start the stack**:
    ```bash
    docker-compose -f docker-compose.langfuse.yml --env-file .env.langfuse up -d
    ```

3.  **Run CLIProxyAPI**:
    Set the environment variable `OTEL_EXPORTER_OTLP_ENDPOINT` to point to your collector (default is `127.0.0.1:4318`, so no change needed if running locally).

    ```bash
    ./CLIProxyAPI
    ```

    Or in PowerShell:
    ```powershell
    $env:OTEL_EXPORTER_OTLP_ENDPOINT="127.0.0.1:4318"
    ./CLIProxyAPI
    ```

4.  **Verify**:
    Make a request to CLIProxyAPI (e.g., `curl http://localhost:8317/v1/chat/completions ...`). You should see the trace appear in Langfuse UI under "Traces".

## Configuration Details

### CLIProxyAPI Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `OTEL_SDK_DISABLED` | Set to `true` to disable OpenTelemetry entirely. | `false` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP HTTP exporter endpoint. | `127.0.0.1:4318` |
| `APP_VERSION` | Application version tag for traces. | (empty) |

**Note on Endpoints:**
The OTel Go SDK is strict about endpoint formatting.
-   **Recommended:** `127.0.0.1:4318` (host:port, no scheme)
-   **Supported:** `http://127.0.0.1:4318` (we automatically normalize this for you)

### Manual Setup (Langfuse Cloud / Existing Collector)

If you already have an OTel Collector or want to use Langfuse Cloud directly:

1.  **Langfuse Cloud**: Langfuse does not currently support direct OTLP ingestion from the browser/client without a proxy for security, but for backend-to-backend, you can use a collector.
    *   Configure your local OTel Collector to export to Langfuse Cloud.
    *   Point CLIProxyAPI to your local collector.

2.  **Direct OTLP (Custom Collector)**:
    If you have your own collector, just point `OTEL_EXPORTER_OTLP_ENDPOINT` to it.

## Troubleshooting

-   **No traces in Langfuse?**
    *   Check OTel Collector logs: `docker-compose -f docker-compose.langfuse.yml logs -f otel-collector`
    *   Verify `LANGFUSE_AUTH_BASE64` is correct (pk:sk encoded).
    *   Ensure CLIProxyAPI can reach port 4318.
-   **"connection refused"?**
    *   Make sure the collector is running and port 4318 is exposed.
