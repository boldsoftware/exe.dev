#!/bin/bash
# Deploy script for blogd binary
# Builds the binary locally and deploys to blog VM

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BINARY_NAME="blogd.$TIMESTAMP"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo "==========================================="
echo "Deploying blogd"
echo "==========================================="
echo ""

# VCS build info (vcs.revision) is not embedded when building from a git
# worktree, so the /debug/gitsha endpoint would return "unknown".
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    GIT_DIR="$(git rev-parse --git-dir)"
    if [ -f "$GIT_DIR/commondir" ]; then
        echo -e "${RED}ERROR: Cannot deploy from a git worktree (VCS build info won't be embedded).${NC}"
        echo "Run this script from the main checkout instead."
        exit 1
    fi
fi

go mod verify

echo -e "${YELLOW}Building binary...${NC}"
GOOS=linux GOARCH=amd64 go build -o "$BINARY_NAME" ./cmd/blogd

if [ ! -f "$BINARY_NAME" ]; then
    echo -e "${RED}ERROR: Failed to build binary${NC}"
    exit 1
fi

BINARY_SIZE=$(ls -lh "$BINARY_NAME" | awk '{print $5}')
echo -e "${GREEN}✓ Binary built successfully (size: $BINARY_SIZE)${NC}"
echo ""

echo -e "${YELLOW}Deploying to exeblog...${NC}"
scp "$BINARY_NAME" exedev@exeblog:~
ssh exedev@exeblog chmod a+x "$BINARY_NAME"
ssh exedev@exeblog sudo systemctl restart blogd

rm "$BINARY_NAME"

echo ""
echo -e "${GREEN}==========================================="
echo "Deployment Complete!"
echo "==========================================="
echo -e "${NC}"
