# ClickHouse Cloud

Host: `mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443` (HTTPS)

## Users

| User | Purpose | Password env var |
|------|---------|-----------------|
| `default` | Admin | `CLICKHOUSE_PASSWORD` |
| `readonly` | Read-only queries (SELECT only, `readonly = 1`) | `CLICKHOUSE_READONLY_PASSWORD` |

## Creating a new read-only user

Generate a password:

```bash
openssl rand -base64 18
```

Connect as admin and create the user:

```bash
curl --user "default:$CLICKHOUSE_PASSWORD" \
  --data-binary "CREATE USER IF NOT EXISTS <username> IDENTIFIED WITH sha256_password BY '<password>' SETTINGS readonly = 1" \
  https://mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443

curl --user "default:$CLICKHOUSE_PASSWORD" \
  --data-binary "GRANT CURRENT GRANTS(SELECT ON *.*) TO <username>" \
  https://mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443
```

Note: `GRANT CURRENT GRANTS(...)` is used instead of plain `GRANT` because the
`default` user lacks GRANT OPTION on some system tables (e.g. `system.zookeeper`).

## Querying

```bash
curl --user "readonly:$CLICKHOUSE_READONLY_PASSWORD" \
  --data-binary 'SELECT version()' \
  https://mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443
```
