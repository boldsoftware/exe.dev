#!/usr/bin/env bash
# Download a Shelley release binary and verify its checksum.
#
# Usage:
#   download-shelley.sh <version> <asset> <output_path>
#
# Example:
#   download-shelley.sh v0.89.914374232 shelley_linux_amd64 exeuntu/shelley

set -euo pipefail

VERSION="$1"
ASSET="$2"
OUTPUT="$3"

BASE_URL="https://github.com/boldsoftware/shelley/releases/download/${VERSION}"

echo "Downloading Shelley $VERSION ($ASSET)"
curl -fsSL --retry 5 --retry-all-errors "${BASE_URL}/${ASSET}" -o "$OUTPUT"
chmod +x "$OUTPUT"

# Download and verify checksum
CHECKSUMS=$(curl -fsSL --retry 5 --retry-all-errors "${BASE_URL}/checksums.txt")
EXPECTED=$(echo "$CHECKSUMS" | grep "$ASSET" | awk '{print $1}')
ACTUAL=$(sha256sum "$OUTPUT" | awk '{print $1}')

if [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "Checksum mismatch!" >&2
    echo "Expected: $EXPECTED" >&2
    echo "Actual:   $ACTUAL" >&2
    exit 1
fi

echo "Checksum verified: $ACTUAL"
echo "Shelley version info:"
"$OUTPUT" version || true
