#!/usr/bin/env bash
set -euo pipefail

# Runs the ClickHouse integration test against a real ClickHouse container.
# Requires docker. Gated by EXE_CLICKHOUSE_TEST=1.

export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH"

echo "--- :docker: docker version"
docker version --format 'client: {{.Client.Version}} server: {{.Server.Version}}' || true

echo "--- :clickhouse: Run ClickHouse integration test"
EXE_CLICKHOUSE_TEST=1 go test -count=1 -v -timeout 5m ./exechsync/
