# exedb

## Booleans

SQLite stores booleans as INTEGER. Add column overrides in `sqlc.yaml` to map them to Go `bool`:

```yaml
overrides:
  - column: "table_name.column_name"
    go_type: "bool"
```

Then run `sqlc generate`.
