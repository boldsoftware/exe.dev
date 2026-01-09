#!/bin/bash
set -euo pipefail

YES_FLAG=false
DB_PATH=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --yes|-y)
            YES_FLAG=true
            shift
            ;;
        -*)
            echo "Unknown option: $1" >&2
            echo "Usage: $0 [--yes] <path-to-exe.db>" >&2
            exit 1
            ;;
        *)
            DB_PATH="$1"
            shift
            ;;
    esac
done

if [ -z "$DB_PATH" ]; then
    echo "Usage: $0 [--yes] <path-to-exe.db>" >&2
    echo "  --yes, -y    Execute commands without prompting" >&2
    exit 1
fi

if [ ! -f "$DB_PATH" ]; then
    echo "Error: database file not found: $DB_PATH" >&2
    exit 1
fi

# Query boxes with their user_id, excluding failed ones
# Group by (ctrhost, user_id) and collect instance IDs
# Instance ID format: vm{id:06d}-{name}
# Output: ctrhost|user_id|"vm000001-name1" "vm000002-name2"...
sqlite3 -separator '|' "$DB_PATH" "
SELECT ctrhost, created_by_user_id, printf('vm%06d-%s', id, name)
FROM boxes
WHERE status NOT IN ('failed', 'creating')
ORDER BY ctrhost, created_by_user_id;
" | awk -F'|' '
{
    key = $1 "|" $2
    if (key in names) {
        names[key] = names[key] " \"" $3 "\""
    } else {
        names[key] = "\"" $3 "\""
        keys[++n] = key
    }
}
END {
    for (i = 1; i <= n; i++) {
        print keys[i] "|" names[keys[i]]
    }
}
' | while IFS='|' read -r ctrhost user_id vm_ids; do
    cmd="exelet-ctl --addr \"$ctrhost\" compute instances set-group --group \"$user_id\" $vm_ids"

    if [ "$YES_FLAG" = true ]; then
        echo "+ $cmd"
        eval "$cmd"
    else
        echo "Run: $cmd"
        read -p "[y/N/q] " -n 1 -r
        echo
        case $REPLY in
            y|Y)
                eval "$cmd"
                ;;
            q|Q)
                echo "Aborted."
                exit 0
                ;;
            *)
                echo "Skipped."
                ;;
        esac
    fi
done
