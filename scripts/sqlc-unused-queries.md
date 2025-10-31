## Checking for unused sqlc queries

The `sqlc` layer in `exedb/` can accumulate stale queries when higher level call
sites are removed. To make it easy to prune dead SQL, use the helper script:

```bash
uv run find_unused_sqlc_queries.py
```

The script scans `exedb/query/*.sql` for every `-- name:` block and runs a
repository-wide `rg` search for the generated method name, ignoring the
generated bindings and `exedb/db.go`. Any query that is only referenced in the
ignored files is considered unused and will be listed in the output. The script
returns exit code `1` when unused queries are detected, which makes it suitable
for CI checks.

You can also request JSON output (and optionally include the raw match lines)
when integrating with other tooling:

```bash
uv run find_unused_sqlc_queries.py --json --print-matches
```
