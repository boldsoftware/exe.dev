# exedb

## Booleans

SQLite stores booleans as INTEGER. Always add column overrides in `sqlc.yaml` to map boolean columns to Go `bool`:

```yaml
overrides:
  - column: "table_name.column_name"
    go_type: "bool"
```

Then run `go generate ./exedb`.
