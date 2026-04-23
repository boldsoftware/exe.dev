#!/bin/sh
set -e

if [ -z "$1" ]; then
    echo "usage: yolo_codex.sh <prompt>" >&2
    exit 1
fi

codex --dangerously-bypass-approvals-and-sandbox exec -m gpt-5.4 --json "$1" 2>/dev/null |
    jq -rs '[.[] | select(.type == "item.completed" and .item.type == "agent_message") | .item.text] | last'
