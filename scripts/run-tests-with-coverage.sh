#!/usr/bin/env bash
set -euo pipefail

if ! command -v go >/dev/null 2>&1; then
	echo "go command not found" >&2
	exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
	echo "python3 command not found" >&2
	exit 1
fi

pkg=$(go list .)

if [ -z "$pkg" ]; then
	echo "failed to determine Go package for current directory" >&2
	exit 1
fi

sanitize() {
	local value="$1"
	value=$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]')
	value=$(printf '%s' "$value" | tr -c 'a-z0-9' '_')
	value=$(printf '%s' "$value" | sed -E 's/_+/_/g; s/^_+|_+$//g')

	if [ -z "$value" ]; then
		echo "failed to sanitize identifier: $1" >&2
		exit 1
	fi

	printf '%s' "$value"
}

build_run_pattern() {
	python3 - "$1" <<'PY'
import re
import sys

name = sys.argv[1]
parts = name.split('/')
components = [f"^{re.escape(part)}$" for part in parts]
print('/'.join(components))
PY
}

tests=()
while IFS= read -r test; do
	tests+=("$test")
done < <(go test -list '^Test' "$pkg" | awk '/^Test/ {print $1}')

if [ ${#tests[@]} -eq 0 ]; then
	echo "no tests found in current directory" >&2
	exit 1
fi

sanitized_pkg=$(sanitize "$pkg")

for test_name in "${tests[@]}"; do
	sanitized_test=$(sanitize "$test_name")
	cover_file="${sanitized_pkg}_${sanitized_test}.cover"

	printf 'Running %s %s -> %s\n' "$pkg" "$test_name" "$cover_file"

	pattern=$(build_run_pattern "$test_name")
	rm -f "$cover_file"

	go test -count=1 -run "$pattern" -covermode=atomic -coverprofile="$cover_file" "$pkg"
done
