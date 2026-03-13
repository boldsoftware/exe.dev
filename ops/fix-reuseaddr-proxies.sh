#!/usr/bin/env bash
# One-time script to replace reuseaddr socat proxies with non-reuseaddr ones.
#
# What it does:
#   1. Finds all socat listener processes (parents with TCP-LISTEN in cmdline)
#   2. Kills them (SIGTERM). Forked children handling active SSH sessions survive.
#   3. Restarts exelet. RecoverProxies finds no listeners and spawns new ones
#      without reuseaddr.
#
# Safe to run on a live node — active SSH connections are not interrupted.

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
    echo "Error: must run as root" >&2
    exit 1
fi

SERVICE_NAME="${1:-exelet}"

echo "=== Finding socat listener processes ==="
LISTENER_PIDS=()
for pid in /proc/[0-9]*/comm; do
    dir=$(dirname "$pid")
    pid_num=$(basename "$dir")
    comm=$(cat "$pid" 2>/dev/null) || continue
    if [[ "$comm" == "socat" ]]; then
        cmdline=$(tr '\0' ' ' <"$dir/cmdline" 2>/dev/null) || continue
        if [[ "$cmdline" == *"TCP-LISTEN"* ]]; then
            LISTENER_PIDS+=("$pid_num")
            echo "  PID $pid_num: $cmdline"
        fi
    fi
done

if [[ ${#LISTENER_PIDS[@]} -eq 0 ]]; then
    echo "No socat listener processes found. Nothing to do."
    exit 0
fi

echo ""
echo "Will kill ${#LISTENER_PIDS[@]} socat listener(s) and restart $SERVICE_NAME."
echo "Active SSH sessions (forked children) will not be affected."
read -rp "Proceed? [y/N] " confirm
if [[ "$confirm" != [yY] ]]; then
    echo "Aborted."
    exit 1
fi

echo ""
echo "=== Killing ${#LISTENER_PIDS[@]} socat listener(s) ==="
for pid in "${LISTENER_PIDS[@]}"; do
    echo "  Killing PID $pid"
    kill "$pid" 2>/dev/null || true
done

# Brief pause for sockets to close
sleep 1

# Verify they're gone
REMAINING=0
for pid in "${LISTENER_PIDS[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
        echo "  WARNING: PID $pid still alive, sending SIGKILL"
        kill -9 "$pid" 2>/dev/null || true
        REMAINING=$((REMAINING + 1))
    fi
done
if [[ $REMAINING -gt 0 ]]; then
    sleep 1
fi

echo ""
echo "=== Restarting $SERVICE_NAME ==="
systemctl restart "$SERVICE_NAME"
sleep 2

echo ""
echo "=== Verifying new proxies ==="
NEW_COUNT=0
for pid in /proc/[0-9]*/comm; do
    dir=$(dirname "$pid")
    pid_num=$(basename "$dir")
    comm=$(cat "$pid" 2>/dev/null) || continue
    if [[ "$comm" == "socat" ]]; then
        cmdline=$(tr '\0' ' ' <"$dir/cmdline" 2>/dev/null) || continue
        if [[ "$cmdline" == *"TCP-LISTEN"* ]]; then
            NEW_COUNT=$((NEW_COUNT + 1))
            if [[ "$cmdline" == *"reuseaddr"* ]]; then
                echo "  WARNING: PID $pid_num still has reuseaddr: $cmdline"
            else
                echo "  OK: PID $pid_num: $cmdline"
            fi
        fi
    fi
done

echo ""
echo "=== Done: $NEW_COUNT proxy listener(s) running ==="
