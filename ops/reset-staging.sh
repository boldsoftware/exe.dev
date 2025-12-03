#!/bin/bash
# Reset script for staging environment
# This script cleans up both exed and exelet staging hosts

set -euo pipefail

EXED_HOST="ubuntu@exed-staging-01"
EXELET_HOST="ubuntu@exe-ctr-staging-01"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m' # No Color

# Helper function to run commands on exed host
run_on_exed() {
    ssh -o ConnectTimeout=10 "$EXED_HOST" "$@"
}

# Helper function to run commands on exelet host
run_on_exelet() {
    ssh -o ConnectTimeout=10 "$EXELET_HOST" "$@"
}

# Prompt for confirmation, returns 0 if confirmed, 1 if declined
confirm_action() {
    local message="$1"
    echo ""
    echo -e "${YELLOW}$message${NC}"
    read -p "Proceed? [y/N] " -n 1 -r
    echo ""
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        return 0
    else
        echo -e "${BLUE}Skipped.${NC}"
        return 1
    fi
}

# =============================================================================
# WARNING BANNER
# =============================================================================
echo ""
echo -e "${RED}${BOLD}"
echo "╔═══════════════════════════════════════════════════════════════════════════╗"
echo "║                                                                           ║"
echo "║   ██╗    ██╗ █████╗ ██████╗ ███╗   ██╗██╗███╗   ██╗ ██████╗              ║"
echo "║   ██║    ██║██╔══██╗██╔══██╗████╗  ██║██║████╗  ██║██╔════╝              ║"
echo "║   ██║ █╗ ██║███████║██████╔╝██╔██╗ ██║██║██╔██╗ ██║██║  ███╗             ║"
echo "║   ██║███╗██║██╔══██║██╔══██╗██║╚██╗██║██║██║╚██╗██║██║   ██║             ║"
echo "║   ╚███╔███╔╝██║  ██║██║  ██║██║ ╚████║██║██║ ╚████║╚██████╔╝             ║"
echo "║    ╚══╝╚══╝ ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═══╝╚═╝╚═╝  ╚═══╝ ╚═════╝              ║"
echo "║                                                                           ║"
echo "║                    DESTRUCTIVE OPERATION                                  ║"
echo "║                                                                           ║"
echo "╠═══════════════════════════════════════════════════════════════════════════╣"
echo "║                                                                           ║"
echo "║  This script will PERMANENTLY DELETE the following:                       ║"
echo "║                                                                           ║"
echo "║  On exed-staging-01:                                                      ║"
echo "║    • exe.db database files (backup will be created)                       ║"
echo "║                                                                           ║"
echo "║  On exe-ctr-staging-01:                                                   ║"
echo "║    • All content in /data/exelet/{instances,network,runtime,storage}/     ║"
echo "║    • All cloud-hypervisor, socat, and virtiofsd processes                 ║"
echo "║    • All ZFS zvols prefixed with tank/019*                                ║"
echo "║    • All tap network interfaces (tap-*)                                   ║"
echo "║                                                                           ║"
echo "║  ALL STAGING DATA WILL BE LOST!                                           ║"
echo "║                                                                           ║"
echo "╚═══════════════════════════════════════════════════════════════════════════╝"
echo -e "${NC}"
echo ""
echo -e "${BOLD}Type 'yes' to continue:${NC}"
read -r confirmation
if [[ "$confirmation" != "yes" ]]; then
    echo -e "${RED}Aborted.${NC}"
    exit 1
fi

# =============================================================================
# VERIFY CONNECTIVITY
# =============================================================================
echo ""
echo -e "${BLUE}Verifying SSH connectivity...${NC}"

if ! run_on_exed "echo 'exed host connected'" >/dev/null 2>&1; then
    echo -e "${RED}ERROR: Cannot connect to exed host ($EXED_HOST)${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Connected to $EXED_HOST${NC}"

if ! run_on_exelet "echo 'exelet host connected'" >/dev/null 2>&1; then
    echo -e "${RED}ERROR: Cannot connect to exelet host ($EXELET_HOST)${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Connected to $EXELET_HOST${NC}"

# =============================================================================
# EXED OPERATIONS
# =============================================================================
echo ""
echo -e "${BOLD}==========================================="
echo "EXED HOST: exed-staging-01"
echo -e "===========================================${NC}"

# --- Stop exed.service ---
echo ""
echo -e "${BLUE}Current exed.service status:${NC}"
run_on_exed "sudo systemctl status exed --no-pager -l" || true

if confirm_action "Stop exed.service?"; then
    run_on_exed "sudo systemctl stop exed"
    echo -e "${GREEN}✓ exed.service stopped${NC}"
fi

# --- Backup exe.db files ---
echo ""
echo -e "${BLUE}Existing database files in home directory:${NC}"
run_on_exed "ls -la ~/exe.db* 2>/dev/null || echo '(no database files found)'"

if confirm_action "Backup exe.db files?"; then
    TIMESTAMP=$(date +%Y%m%d-%H%M%S)
    run_on_exed "
        if [ -f ~/exe.db ]; then
            cp ~/exe.db ~/exe.db.backup.$TIMESTAMP
            echo 'Backed up exe.db to exe.db.backup.$TIMESTAMP'
        fi
        if [ -f ~/exe.db-shm ]; then
            cp ~/exe.db-shm ~/exe.db-shm.backup.$TIMESTAMP
            echo 'Backed up exe.db-shm to exe.db-shm.backup.$TIMESTAMP'
        fi
        if [ -f ~/exe.db-wal ]; then
            cp ~/exe.db-wal ~/exe.db-wal.backup.$TIMESTAMP
            echo 'Backed up exe.db-wal to exe.db-wal.backup.$TIMESTAMP'
        fi
    "
    echo -e "${GREEN}✓ Database files backed up${NC}"
fi

# --- Remove exe.db files ---
echo ""
echo -e "${BLUE}Database files that will be removed:${NC}"
run_on_exed "ls -la ~/exe.db ~/exe.db-shm ~/exe.db-wal 2>/dev/null || echo '(no database files to remove)'"

if confirm_action "Remove exe.db files?"; then
    run_on_exed "rm -f ~/exe.db ~/exe.db-shm ~/exe.db-wal"
    echo -e "${GREEN}✓ Database files removed${NC}"
fi

# --- Restart exed.service ---
if confirm_action "Restart exed.service?"; then
    run_on_exed "sudo systemctl start exed"
    sleep 2
    echo -e "${GREEN}✓ exed.service started${NC}"
    echo ""
    echo -e "${BLUE}New exed.service status:${NC}"
    run_on_exed "sudo systemctl status exed --no-pager -l" || true
fi

# =============================================================================
# EXELET OPERATIONS
# =============================================================================
echo ""
echo -e "${BOLD}==========================================="
echo "EXELET HOST: exe-ctr-staging-01"
echo -e "===========================================${NC}"

# --- Stop exelet.service ---
echo ""
echo -e "${BLUE}Current exelet.service status:${NC}"
run_on_exelet "sudo systemctl status exelet --no-pager -l" || true

if confirm_action "Stop exelet.service?"; then
    run_on_exelet "sudo systemctl stop exelet"
    echo -e "${GREEN}✓ exelet.service stopped${NC}"
fi

# --- Remove /data/exelet content ---
echo ""
echo -e "${BLUE}Content in /data/exelet directories:${NC}"
run_on_exelet "
    for dir in instances network runtime storage; do
        echo \"--- /data/exelet/\$dir ---\"
        sudo ls -la /data/exelet/\$dir 2>/dev/null || echo '(directory empty or does not exist)'
    done
"

if confirm_action "Remove all content in /data/exelet/{instances,network,runtime,storage}/?"; then
    set +e
    run_on_exelet "sudo find /data/exelet/instances /data/exelet/network /data/exelet/runtime /data/exelet/storage -mindepth 1 -delete 2>/dev/null; true"
    set -e
    echo -e "${GREEN}✓ /data/exelet directories cleaned${NC}"
fi

# --- Kill processes ---
echo ""
echo -e "${BLUE}cloud-hypervisor processes:${NC}"
run_on_exelet "ps aux | grep '[c]loud-hypervisor' || echo '(none found)'"

echo ""
echo -e "${BLUE}socat processes:${NC}"
run_on_exelet "ps aux | grep '[s]ocat' || echo '(none found)'"

echo ""
echo -e "${BLUE}virtiofsd processes:${NC}"
run_on_exelet "ps aux | grep '[v]irtiofsd' || echo '(none found)'"

if confirm_action "Kill all cloud-hypervisor, socat, and virtiofsd processes?"; then
    # Use pgrep/kill with patterns that match binaries but not this ssh command
    set +e
    run_on_exelet "pgrep -f '/cloud-hypervisor' | xargs -r sudo kill -9; pgrep -f '/virtiofsd' | xargs -r sudo kill -9; pgrep -f 'socat.*exelet' | xargs -r sudo kill -9; true"
    set -e
    # Verify critical processes are stopped
    # Check by looking for actual running processes, not command line matches
    echo -e "${BLUE}Verifying processes are stopped...${NC}"
    MAX_ATTEMPTS=50
    attempt=0
    while true; do
        # Count actual cloud-hypervisor and virtiofsd processes (not grep/ssh)
        count=$(run_on_exelet "ps -eo comm | grep -E '^(cloud-hyperviso|virtiofsd)$' | wc -l" 2>/dev/null || echo "0")
        if [ "$count" -eq 0 ]; then
            break
        fi
        attempt=$((attempt + 1))
        if [ $attempt -ge $MAX_ATTEMPTS ]; then
            echo -e "${RED}ERROR: Failed to kill all processes after $MAX_ATTEMPTS attempts${NC}"
            echo -e "${RED}Remaining processes:${NC}"
            run_on_exelet "ps aux | grep -E 'cloud-hypervisor|virtiofsd' | grep -v grep" || true
            exit 1
        fi
        sleep 0.1
    done
    echo -e "${GREEN}✓ All processes killed and verified${NC}"
fi

# --- Remove ZFS zvols ---
echo ""
echo -e "${BLUE}ZFS zvols prefixed with tank/019*:${NC}"
ZVOLS=$(run_on_exelet "sudo zfs list -t volume -o name -H 2>/dev/null | grep '^tank/019' || echo ''")
if [ -z "$ZVOLS" ]; then
    echo "(none found)"
else
    echo "$ZVOLS"

    if confirm_action "Remove these ZFS zvols?"; then
        run_on_exelet "
            sudo zfs list -t volume -o name -H 2>/dev/null | grep '^tank/019' | while read -r zvol; do
                echo \"Destroying \$zvol...\"
                sudo zfs destroy -r \"\$zvol\"
            done
        "
        echo -e "${GREEN}✓ ZFS zvols removed${NC}"
    fi
fi

# --- Remove tap interfaces ---
echo ""
echo -e "${BLUE}tap network interfaces (tap-*):${NC}"
TAPS=$(run_on_exelet "ip link show 2>/dev/null | grep -oE 'tap-[^:@]+' || echo ''")
if [ -z "$TAPS" ]; then
    echo "(none found)"
else
    echo "$TAPS"

    if confirm_action "Remove these tap network interfaces?"; then
        run_on_exelet "
            ip link show 2>/dev/null | grep -oE 'tap-[^:@]+' | while read -r tap; do
                echo \"Deleting \$tap...\"
                sudo ip link delete \"\$tap\"
            done
        "
        echo -e "${GREEN}✓ tap interfaces removed${NC}"
    fi
fi

# --- Restart exelet.service ---
if confirm_action "Restart exelet.service?"; then
    run_on_exelet "sudo systemctl start exelet"
    sleep 2
    echo -e "${GREEN}✓ exelet.service started${NC}"
    echo ""
    echo -e "${BLUE}New exelet.service status:${NC}"
    run_on_exelet "sudo systemctl status exelet --no-pager -l" || true
fi

# =============================================================================
# COMPLETION
# =============================================================================
echo ""
echo -e "${GREEN}${BOLD}==========================================="
echo "Staging Reset Complete"
echo -e "===========================================${NC}"
echo ""
echo "Summary:"
echo "  • exed-staging-01: Database reset, service restarted"
echo "  • exe-ctr-staging-01: Instances, processes, zvols, and tap interfaces cleaned"
echo ""
echo "Next steps:"
echo "  • Verify exed health: curl https://exe-staging.dev/health"
echo "  • Check exed logs: ssh $EXED_HOST journalctl -fu exed"
echo "  • Check exelet logs: ssh $EXELET_HOST journalctl -fu exelet"
