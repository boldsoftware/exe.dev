#!/usr/bin/env python3
"""
Locate sqlc queries that are no longer referenced in application code.

The script scans `exedb/query/*.sql` for `-- name:` declarations and uses `rg`
to look for those query identifiers in the repository, ignoring the generated
Go bindings (`*.sql.go`), the raw SQL files themselves, and `exedb/db.go`
where the prepared statements live.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Dict, Iterator, List, Sequence, Tuple


REPO_ROOT = Path(__file__).resolve().parents[1]
QUERY_DIR = REPO_ROOT / "exedb" / "query"
DEFAULT_IGNORE_PREFIXES = (
    "exedb/query/",
    "exedb/db.go",
)
DEFAULT_IGNORE_SUFFIXES = (".sql.go",)


def parse_args(argv: Sequence[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--json",
        action="store_true",
        help="Emit machine-readable JSON output instead of human readable text.",
    )
    parser.add_argument(
        "--print-matches",
        action="store_true",
        help="Include the filtered ripgrep matches for each query.",
    )
    parser.add_argument(
        "--query-dir",
        type=Path,
        default=QUERY_DIR,
        help="Directory containing the sqlc query files (default: %(default)s).",
    )
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=REPO_ROOT,
        help="Repository root to search (default: %(default)s).",
    )
    return parser.parse_args(argv)


def iter_query_names(query_dir: Path) -> Iterator[Tuple[str, Path]]:
    sql_paths = sorted(query_dir.glob("*.sql"))
    for sql_path in sql_paths:
        with sql_path.open("r", encoding="utf-8") as handle:
            for line in handle:
                line = line.strip()
                if not line.startswith("-- name:"):
                    continue
                # -- name: Identifier [:type]
                parts = line.split()
                if len(parts) < 3:
                    continue
                name = parts[2]
                yield name, sql_path


def run_rg(query: str, cwd: Path) -> subprocess.CompletedProcess:
    pattern = rf"\b{re.escape(query)}\b"
    cmd = ["rg", "--no-heading", "--line-number", "--", pattern, "."]
    return subprocess.run(
        cmd,
        cwd=str(cwd),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
        text=True,
    )


def filter_matches(raw_output: str) -> List[str]:
    matches: List[str] = []
    for line in raw_output.splitlines():
        if not line:
            continue
        path, _, _ = line.partition(":")
        normalized = path[2:] if path.startswith("./") else path
        if any(normalized.startswith(prefix) for prefix in DEFAULT_IGNORE_PREFIXES):
            continue
        if any(normalized.endswith(suffix) for suffix in DEFAULT_IGNORE_SUFFIXES):
            continue
        matches.append(line)
    return matches


def main(argv: Sequence[str]) -> int:
    args = parse_args(argv)
    results: Dict[str, Dict[str, Sequence[str]]] = {}
    unused: Dict[str, Dict[str, Sequence[str]]] = {}

    for query_name, sql_path in iter_query_names(args.query_dir):
        completed = run_rg(query_name, args.repo_root)
        filtered = filter_matches(completed.stdout)
        result = {
            "sql_file": str(sql_path.relative_to(args.repo_root)),
            "matches": filtered if args.print_matches else len(filtered),
        }
        results[query_name] = result
        if not filtered:
            unused[query_name] = {
                "sql_file": str(sql_path.relative_to(args.repo_root)),
                "matches": filtered,
            }

    if args.json:
        payload = {
            "unused": unused,
            "all_queries": results,
        }
        print(json.dumps(payload, indent=2))
        return 0 if not unused else 1

    if not unused:
        print("No unused queries found.")
        return 0

    print("Unused queries:")
    for name, data in sorted(unused.items(), key=lambda item: item[1]["sql_file"]):
        print(f"- {name} ({data['sql_file']})")
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
