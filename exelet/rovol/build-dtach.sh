#!/bin/sh
# Build dtach with the same configuration as other exe.dev tools
# Usage: build-dtach.sh <target_arch>
# where target_arch is arm64 or amd64
#
# Once this is built via the various Makefiles,
#   docker run -v $(pwd)/container/rovol/arm64:/exe.dev -it ubuntu:24.04 /exe.dev/bin/dtach --help
# can test whether it's built successfully.

set -e

ARCH="$1"
if [ -z "$ARCH" ]; then
    echo "Usage: $0 <target_arch>" >&2
    echo "where target_arch is arm64 or amd64" >&2
    exit 1
fi

INTERPRETER="ld-musl.so.1"

echo "Building dtach for $ARCH..."

# Download and extract dtach
WORKDIR="/src/dtach"
mkdir -p "$WORKDIR"
cd "$WORKDIR"

# Get latest release or use a specific version
DTACH_VERSION="0.9"
curl -fsSLO "https://github.com/crigler/dtach/archive/v${DTACH_VERSION}.tar.gz"
tar xzf "v${DTACH_VERSION}.tar.gz"
cd "dtach-${DTACH_VERSION}"

# Configure with correct interpreter and rpath for /exe.dev
export CFLAGS="-O1 -fno-strict-aliasing -fno-stack-protector"
export LDFLAGS="-Wl,-rpath,/exe.dev/lib -Wl,--dynamic-linker,/exe.dev/lib/${INTERPRETER}"

./configure \
    --prefix=/exe.dev \
    --bindir=/exe.dev/bin

# Build
make

# Strip the binary
strip --strip-all --remove-section=.comment --remove-section=.note dtach

echo "dtach built successfully for $ARCH"
