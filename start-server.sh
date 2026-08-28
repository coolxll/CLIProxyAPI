#!/bin/bash
# Start a local CLIProxyAPI server.

set -euo pipefail

CONFIG_FILE="config.yaml"
REQUEST_LOG_BASE_DIR="/tmp/cliproxy-debug"
REQUEST_LOG_DIR="$REQUEST_LOG_BASE_DIR/$(date +%Y%m%d-%H%M%S)"
RUN_CONFIG="$REQUEST_LOG_DIR/config.yaml"
export WRITABLE_PATH="$REQUEST_LOG_DIR"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "Config file not found: $CONFIG_FILE" >&2
    exit 1
fi

mkdir -p "$REQUEST_LOG_DIR"

# Keep the checked-in config untouched, but force request logging for this run.
awk '!/^[[:space:]]*#?[[:space:]]*request-log[[:space:]]*:/' "$CONFIG_FILE" > "$RUN_CONFIG"
printf '\nrequest-log: true\n' >> "$RUN_CONFIG"

echo "Starting CLIProxyAPI with config: $RUN_CONFIG"
echo "Listening port: 8317"
echo "API key: sk-lingma-test"
echo "Request logs: $WRITABLE_PATH/logs"
echo "Press Ctrl+C to stop."

exec go run ./cmd/server --config "$RUN_CONFIG"
