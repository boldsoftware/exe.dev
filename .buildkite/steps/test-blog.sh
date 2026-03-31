#!/usr/bin/env bash
set -euo pipefail
trap 'echo Error in $0 at line $LINENO' ERR

export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"

echo "--- :go: Set up Go"
go version

echo "--- :test_tube: Run blog tests"
go test -race -count=1 ./blog/...

echo "--- :hammer: Build blogd"
go build -o /dev/null ./cmd/blogd
