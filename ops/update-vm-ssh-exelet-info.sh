#!/bin/bash
set -e

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <exelet-addr> [db-path]"
  echo ""
  echo "Generates sqlite UPDATE commands for all VMs on an exelet."
  echo "The exelet address is used as the ctrhost value in the database."
  echo ""
  echo "Arguments:"
  echo "  exelet-addr    Address of the exelet (e.g., tcp://exe-ctr-18:9080)"
  echo "  db-path        Path to exe.db (default: ./exe.db)"
  echo ""
  echo "Example:"
  echo "  $0 tcp://exe-ctr-18:9080"
  echo "  $0 tcp://exe-ctr-18:9080 | sqlite3 ./exe.db"
  exit 1
fi

EXELET_ADDR="$1"
CTRHOST="$1"
DB_PATH="${2:-./exe.db}"

# Get list of instance IDs (skip header line)
instances=$(exelet-ctl -a "$EXELET_ADDR" compute instances list | tail -n +2 | awk '{print $1}')

if [[ -z "$instances" ]]; then
  echo "No instances found on $EXELET_ADDR" >&2
  exit 0
fi

echo "-- Generated UPDATE commands for exelet $EXELET_ADDR"
echo "-- Run with: sqlite3 $DB_PATH < this_file.sql"
echo ""

for id in $instances; do
  # Get instance details as JSON and extract ssh_port (--json must come before ID due to urfave)
  json=$(exelet-ctl -a "$EXELET_ADDR" compute instances get --json "$id")
  ssh_port=$(echo "$json" | jq -r '.ssh_port')
  name=$(echo "$json" | jq -r '.name')

  if [[ -z "$ssh_port" || "$ssh_port" == "null" || "$ssh_port" == "0" ]]; then
    echo "-- WARNING: No SSH port found for instance $id ($name), skipping" >&2
    continue
  fi

  echo "UPDATE boxes SET ctrhost = '$CTRHOST', ssh_port = $ssh_port, status = 'running', updated_at = CURRENT_TIMESTAMP WHERE name = '$name';"
done

echo ""
echo "-- Done"
