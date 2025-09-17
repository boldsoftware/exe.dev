#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 [--dump-lines] COVER_PROFILE" >&2
}

dump_lines=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dump-lines)
      dump_lines=true
      shift
      ;;
    --help)
      usage
      exit 0
      ;;
    -*)
      echo "unknown flag: $1" >&2
      usage
      exit 1
      ;;
    *)
      break
      ;;
  esac
done

if [ "$#" -ne 1 ]; then
  usage
  exit 1
fi

profile=$1
baseline="e1e/e1e.cover"

if ! command -v go >/dev/null 2>&1; then
  echo "go command not found" >&2
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 command not found" >&2
  exit 1
fi

if [ ! -f "$profile" ]; then
  echo "coverage profile not found: $profile" >&2
  exit 1
fi

if [ ! -f "$baseline" ]; then
  echo "baseline coverage profile not found: $baseline" >&2
  exit 1
fi

diff_profile=$(mktemp -t exe-coverage-diff-XXXXXX.cover)
lines_dump_file=""

if $dump_lines; then
  lines_dump_file=$(mktemp -t exe-coverage-lines-XXXXXX.txt)
fi

cleanup() {
  rm -f "$diff_profile"
  if [ -n "$lines_dump_file" ] && [ -f "$lines_dump_file" ]; then
    rm -f "$lines_dump_file"
  fi
}
trap cleanup EXIT

python_args=("-" "$baseline" "$profile" "$diff_profile")
if $dump_lines; then
  python_args+=("$lines_dump_file")
fi

unique_count=$(python3 "${python_args[@]}" <<'PY'
import sys
from pathlib import Path
from typing import Optional

args = sys.argv[1:]


def fatal(message: str) -> None:
    print(message, file=sys.stderr)
    sys.exit(1)


if len(args) == 3:
    baseline_arg, profile_arg, output_arg = args
    lines_arg: Optional[str] = None
elif len(args) == 4:
    baseline_arg, profile_arg, output_arg, lines_arg = args
else:
    fatal("unexpected invocation")

baseline_path = Path(baseline_arg)
profile_path = Path(profile_arg)
out_path = Path(output_arg)
lines_path = Path(lines_arg) if lines_arg is not None else None

module_name: Optional[str] = None
go_mod_path = Path("go.mod")
if go_mod_path.exists():
    try:
        with go_mod_path.open("r", encoding="utf-8") as handle:
            for raw in handle:
                stripped = raw.strip()
                if stripped.startswith("module "):
                    module_name = stripped.split(None, 1)[1]
                    break
    except OSError as exc:
        fatal(f"failed to read go.mod: {exc}")


def load_covered_blocks(path: Path) -> set[str]:
    covered: set[str] = set()
    try:
        with path.open('r', encoding='utf-8') as handle:
            header = handle.readline()
            if not header.startswith('mode:'):
                fatal(f"{path} does not appear to be a Go coverage profile")

            for raw in handle:
                line = raw.strip()
                if not line:
                    continue

                try:
                    location, _, count_str = line.rsplit(' ', 2)
                    count = int(count_str)
                except ValueError as exc:
                    fatal(f"{path}: malformed coverage record: {line!r} ({exc})")

                if count <= 0:
                    continue

                if ':' not in location or ',' not in location:
                    fatal(f"{path}: malformed location: {location!r}")

                covered.add(location)
    except FileNotFoundError:
        fatal(f"coverage file not found: {path}")

    return covered


def resolve_source_path(file_part: str, module: Optional[str]) -> Path:
    path = Path(file_part)
    if path.exists():
        return path
    if module and file_part.startswith(f"{module}/"):
        candidate = Path(file_part[len(module) + 1 :])
        if candidate.exists():
            return candidate
    fatal(f"source file not found: {file_part}")


def get_line_text(
    file_part: str,
    line_no: int,
    cache: dict[Path, list[str]],
    module: Optional[str],
) -> str:
    resolved = resolve_source_path(file_part, module)
    try:
        lines = cache[resolved]
    except KeyError:
        try:
            with resolved.open('r', encoding='utf-8') as handle:
                lines = handle.readlines()
        except FileNotFoundError:
            fatal(f"source file not found: {file_part}")
        cache[resolved] = lines

    if line_no <= 0 or line_no > len(lines):
        fatal(f"{file_part}: line {line_no} not present (file has {len(lines)} lines)")

    return lines[line_no - 1].rstrip('\n')


def filter_profile(
    baseline_blocks: set[str],
    path: Path,
    output: Path,
    cache: dict[Path, list[str]],
    module: Optional[str],
) -> tuple[int, list[tuple[str, int, str]]]:
    unique_blocks_total = 0
    seen_blocks: set[str] = set()
    unique_blocks: list[tuple[str, int, str]] = []
    blocks: list[str] = []

    with path.open('r', encoding='utf-8') as handle:
        header = handle.readline()
        if not header.startswith('mode:'):
            fatal(f"{path} does not appear to be a Go coverage profile")

        blocks.append(header)

        for raw in handle:
            stripped = raw.strip()
            if not stripped:
                continue

            try:
                location, meta, count_str = raw.rsplit(' ', 2)
                count = int(count_str)
            except ValueError as exc:
                fatal(f"{path}: malformed coverage record: {stripped!r} ({exc})")

            if count <= 0:
                continue

            try:
                file_part, range_part = location.split(':', 1)
                start_part, end_part = range_part.split(',', 1)
                start_line = int(start_part.split('.', 1)[0])
                end_line = int(end_part.split('.', 1)[0])
            except ValueError as exc:
                fatal(f"{path}: malformed location: {location!r} ({exc})")

            if location in baseline_blocks:
                continue

            if location in seen_blocks:
                continue

            seen_blocks.add(location)
            unique_blocks_total += 1

            line_text = get_line_text(file_part, start_line, cache, module)
            unique_blocks.append((file_part, start_line, line_text))

            blocks.append(raw)

    with output.open('w', encoding='utf-8') as handle:
        for block in blocks:
            handle.write(block)

    return unique_blocks_total, unique_blocks


baseline_covered = load_covered_blocks(baseline_path)
unique_block_count, new_lines = filter_profile(baseline_covered, profile_path, out_path, {}, module_name)

if lines_path is not None:
    with lines_path.open('w', encoding='utf-8') as handle:
        for file_part, line_no, line_text in new_lines:
            handle.write(f"{file_part}:{line_no}: {line_text}\n")

print(unique_block_count)
PY
)

case "$unique_count" in
  '' )
    echo "failed to generate differential coverage" >&2
    exit 1
    ;;
  0 )
    echo "no differential coverage lines relative to $baseline" >&2
    exit 0
    ;;
esac

if $dump_lines; then
  cat "$lines_dump_file"
  exit 0
fi

html_file=$(mktemp -t exe-coverage-XXXXXX.html)
rm -f "$html_file"
html_file="${html_file}.html"

if ! go tool cover -html="$diff_profile" -o "$html_file"; then
  echo "failed to render coverage report" >&2
  exit 1
fi

declare -a openers
openers=("python3" "open" "xdg-open")

for opener in "${openers[@]}"; do
  case "$opener" in
    python3)
      if python3 - "$html_file" <<'PY'
import sys
import webbrowser
path = sys.argv[1]
if not webbrowser.open(f"file://{path}"):
    sys.exit(1)
PY
      then
        echo "opened coverage report: $html_file"
        exit 0
      fi
      ;;
    open)
      if command -v open >/dev/null 2>&1 && open "$html_file" >/dev/null 2>&1; then
        echo "opened coverage report: $html_file"
        exit 0
      fi
      ;;
    xdg-open)
      if command -v xdg-open >/dev/null 2>&1 && xdg-open "$html_file" >/dev/null 2>&1; then
        echo "opened coverage report: $html_file"
        exit 0
      fi
      ;;
  esac
done

echo "coverage report written to $html_file" >&2
exit 0
