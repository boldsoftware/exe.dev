#!/bin/sh
set -e

if [ -z "$1" ]; then
	echo "usage: yolo_claude.sh <prompt>" >&2
	exit 1
fi

claude --dangerously-skip-permissions --model opus -p "$1"
