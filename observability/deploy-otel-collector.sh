#!/bin/bash
set -euo pipefail

# Deploy OpenTelemetry Collector to the mon host.
# This script is idempotent - safe to run multiple times.
# It will upgrade the collector if a newer version is available.
#
# The collector receives OTLP logs and forwards them to:
# - Honeycomb (separate API keys for staging and production)
# - JSON files at /var/log/otel/{staging,production,unknown}/
# - S3 bucket s3://exe.dev-logs/{staging,production,unknown}/
#
# Services should set deployment.environment resource attribute to "staging" or "production"
# to route logs to the appropriate file.
#
# Usage:
#   ./deploy-otel-collector.sh
#   (reads API keys from mon:/etc/default/otel-collector, or pass them as env vars)

OTEL_PORT_GRPC=4317
OTEL_PORT_HTTP=4318
OTEL_HEALTH_PORT=13133
OTEL_CONFIG_PATH=/etc/otel-collector/config.yml
OTEL_ENV_FILE=/etc/default/otel-collector
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Use provided API keys, or read them from the existing env file on mon
if [ -z "${HONEYCOMB_API_KEY_STAGING:-}" ] || [ -z "${HONEYCOMB_API_KEY_PRODUCTION:-}" ]; then
    echo "API keys not provided locally, reading from mon:${OTEL_ENV_FILE}..."
    REMOTE_ENV=$(ssh ubuntu@mon "sudo cat ${OTEL_ENV_FILE} 2>/dev/null" || true)
    if [ -n "$REMOTE_ENV" ]; then
        eval "$REMOTE_ENV"
    fi
    if [ -z "${HONEYCOMB_API_KEY_STAGING:-}" ] || [ -z "${HONEYCOMB_API_KEY_PRODUCTION:-}" ]; then
        echo "ERROR: Both HONEYCOMB_API_KEY_STAGING and HONEYCOMB_API_KEY_PRODUCTION are required" >&2
        echo "Provide them as env vars or ensure ${OTEL_ENV_FILE} exists on mon" >&2
        exit 1
    fi
fi

# We use otel-collector-contrib which includes all exporters/connectors
OTEL_BINARY_NAME="otelcol-contrib"

echo "=========================================="
echo "Deploying OpenTelemetry Collector to mon"
echo "=========================================="
echo ""

# Get the latest version from GitHub
echo "Checking latest otel-collector-contrib version..."
LATEST_VERSION=$(curl -s https://api.github.com/repos/open-telemetry/opentelemetry-collector-releases/releases/latest | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

if [ -z "$LATEST_VERSION" ]; then
    echo "ERROR: Could not determine latest otel-collector version"
    exit 1
fi

echo "Latest version: ${LATEST_VERSION}"

# Check current installed version on mon
CURRENT_VERSION=$(ssh ubuntu@mon "${OTEL_BINARY_NAME} --version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 | sed 's/v//' || echo 'none'")
echo "Current installed version: ${CURRENT_VERSION}"

if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ]; then
    echo "otel-collector is already at latest version ${LATEST_VERSION}"
    NEEDS_INSTALL=false
else
    echo "Will install/upgrade otel-collector to ${LATEST_VERSION}"
    NEEDS_INSTALL=true
fi

if [ "$NEEDS_INSTALL" = true ]; then
    # Determine architecture
    ARCH=$(ssh ubuntu@mon "uname -m")
    if [ "$ARCH" = "x86_64" ]; then
        ARCH="amd64"
    elif [ "$ARCH" = "aarch64" ]; then
        ARCH="arm64"
    fi

    DOWNLOAD_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${LATEST_VERSION}/otelcol-contrib_${LATEST_VERSION}_linux_${ARCH}.tar.gz"

    echo "Downloading and installing otel-collector ${LATEST_VERSION} on mon..."
    ssh ubuntu@mon "bash -s" <<EOF
set -euo pipefail
cd /tmp
curl -sLO "${DOWNLOAD_URL}"
tar xzf "otelcol-contrib_${LATEST_VERSION}_linux_${ARCH}.tar.gz"
sudo mv "${OTEL_BINARY_NAME}" /usr/local/bin/${OTEL_BINARY_NAME}
sudo chmod +x /usr/local/bin/${OTEL_BINARY_NAME}
rm -f "otelcol-contrib_${LATEST_VERSION}_linux_${ARCH}.tar.gz"
echo "Installed: \$(/usr/local/bin/${OTEL_BINARY_NAME} --version | head -1)"
EOF
fi

# Create log directories
echo "Creating log directories..."
ssh ubuntu@mon "sudo mkdir -p /var/log/otel/{staging,production,unknown} && sudo chmod -R 755 /var/log/otel"

# Deploy config file
echo "Deploying otel-collector config..."
ssh ubuntu@mon "sudo mkdir -p /etc/otel-collector"
scp "${SCRIPT_DIR}/otel-collector-config.yml" ubuntu@mon:/tmp/otel-collector-config.yml
ssh ubuntu@mon "sudo mv /tmp/otel-collector-config.yml ${OTEL_CONFIG_PATH} && sudo chmod 644 ${OTEL_CONFIG_PATH}"

# Create environment file with API keys
echo "Creating environment file..."
ssh ubuntu@mon "sudo tee ${OTEL_ENV_FILE} > /dev/null" <<EOF
HONEYCOMB_API_KEY_STAGING=${HONEYCOMB_API_KEY_STAGING}
HONEYCOMB_API_KEY_PRODUCTION=${HONEYCOMB_API_KEY_PRODUCTION}
EOF
ssh ubuntu@mon "sudo chmod 600 ${OTEL_ENV_FILE}"

# Deploy systemd service
echo "Deploying systemd service..."
scp "${SCRIPT_DIR}/otel-collector.service" ubuntu@mon:/tmp/otel-collector.service
ssh ubuntu@mon "sudo mv /tmp/otel-collector.service /etc/systemd/system/otel-collector.service"

# Reload and restart
echo "Reloading systemd and restarting otel-collector..."
ssh ubuntu@mon "sudo systemctl daemon-reload && sudo systemctl enable otel-collector && sudo systemctl restart otel-collector"

# Verify it's running
echo "Verifying otel-collector is running..."
sleep 3
if ssh ubuntu@mon "curl -sf http://localhost:${OTEL_HEALTH_PORT}/ > /dev/null 2>&1"; then
    echo "Health check passed"
else
    echo "WARNING: Health check endpoint not responding yet"
    echo "Checking service status..."
    ssh ubuntu@mon "sudo systemctl status otel-collector --no-pager" || true
fi

echo ""
echo "=========================================="
echo "OpenTelemetry Collector deployment complete!"
echo "=========================================="
echo ""
echo "Endpoints on mon:"
echo "  - OTLP gRPC:  mon:${OTEL_PORT_GRPC}"
echo "  - OTLP HTTP:  mon:${OTEL_PORT_HTTP}"
echo "  - Health:     mon:${OTEL_HEALTH_PORT}"
echo "  - zPages:     http://mon:55679/debug/tracez"
echo ""
echo "Log directories (local):"
echo "  - /var/log/otel/staging/"
echo "  - /var/log/otel/production/"
echo "  - /var/log/otel/unknown/"
echo ""
echo "S3 bucket:"
echo "  - s3://exe.dev-logs/staging/"
echo "  - s3://exe.dev-logs/production/"
echo "  - s3://exe.dev-logs/unknown/"
echo ""
echo "Config: ${OTEL_CONFIG_PATH}"
echo "Env:    ${OTEL_ENV_FILE}"
echo ""
echo "To send logs, configure your service with:"
echo "  OTEL_EXPORTER_OTLP_ENDPOINT=http://mon:${OTEL_PORT_HTTP}"
echo "  OTEL_RESOURCE_ATTRIBUTES=deployment.environment=staging"
echo ""
echo "View logs:"
echo "  ssh ubuntu@mon journalctl -fu otel-collector"
echo "=========================================="
