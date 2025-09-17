#!/usr/bin/env python3
import sys
from pathlib import Path

BASELINE_PATH = Path('e1e/e1e.cover')


def fatal(message: str) -> None:
    print(message, file=sys.stderr)
    sys.exit(1)


def load_covered_blocks(path: Path) -> set[str]:
    covered: set[str] = set()
    try:
        with path.open('r', encoding='utf-8') as handle:
            header = handle.readline().strip()
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


def main() -> None:
    if not BASELINE_PATH.is_file():
        fatal(f"expected baseline coverage at {BASELINE_PATH}")

    baseline_covered = load_covered_blocks(BASELINE_PATH)

    cover_files = sorted(Path('.').glob('*.cover'))
    if not cover_files:
        fatal("no .cover files found in current directory")

    results: list[tuple[int, str]] = []
    for cover_path in cover_files:
        covered = load_covered_blocks(cover_path)
        unique_lines = len(covered - baseline_covered)
        results.append((unique_lines, cover_path.name))

    for unique_lines, name in sorted(results):
        print(f"{name} {unique_lines}")


if __name__ == '__main__':
    main()
