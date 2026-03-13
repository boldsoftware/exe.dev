"""ClickHouse data source: HTTP client + proxy object.

Client layer (host-side, never exposed to sandbox):
    ClickHouseClient — urllib.request-based ClickHouse HTTP API client
    with Basic auth. Sends SQL via POST, receives JSON results.

Proxy object (exposed to sandbox):
    ClickHouseSource — root entry point: schema discovery (databases,
                       tables, describe_table) and SQL query execution.

ClickHouse is a SQL database, not a hierarchical object model like
GitHub/Discord/Missive. The agent writes SQL directly — no nested
proxy objects, no row-level proxies. Results are concrete values
(list of dicts). See proxy_api_design.md for the design philosophy.
"""

import base64
import json
import logging
import time
import urllib.error
import urllib.request

from panopticon.proxy import ProxyObject

log = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Client layer (host-side only)
# ---------------------------------------------------------------------------



class ClickHouseClient:
    """ClickHouse HTTP API client using urllib.request.

    Sends SQL via POST body with FORMAT JSON appended.
    Uses HTTP Basic auth. Never exposed to the sandbox.
    """

    def __init__(self, url: str, user: str = "readonly", password: str = ""):
        url = (url or "").strip().rstrip("/")
        if not url:
            raise ValueError("EXE_CLICKHOUSE_URL must be set")
        password = (password or "").strip()
        if not password:
            raise ValueError("EXE_CLICKHOUSE_PASSWORD must be set")
        self._url = url
        self._user = user
        self._password = password
        creds = base64.b64encode(f"{user}:{password}".encode()).decode()
        self._auth_header = f"Basic {creds}"

    def execute(self, sql: str, database: str | None = None) -> dict:
        """Execute a SQL query and return the parsed JSON response.

        Appends FORMAT JSON to the SQL. Raises on HTTP errors.
        Returns the full ClickHouse JSON response dict with keys:
        meta, data, rows, statistics, etc.
        """
        params = {}
        if database:
            params["database"] = database

        query_url = self._url
        if params:
            qs = "&".join(f"{k}={urllib.request.quote(str(v))}" for k, v in params.items())
            query_url = f"{query_url}?{qs}"

        body = f"{sql}\nFORMAT JSON".encode("utf-8")

        req = urllib.request.Request(
            query_url,
            data=body,
            headers={
                "Authorization": self._auth_header,
                "Content-Type": "text/plain; charset=utf-8",
            },
            method="POST",
        )

        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                return json.loads(resp.read())
        except urllib.error.HTTPError as exc:
            # ClickHouse returns error details in the response body
            error_body = ""
            try:
                error_body = exc.read().decode("utf-8", errors="replace")[:500]
            except Exception:
                pass
            log.error("ClickHouse HTTP %d: %s", exc.code, error_body)
            raise RuntimeError(
                f"ClickHouse error: {error_body}"
            ) from exc


# ---------------------------------------------------------------------------
# Proxy object (sandbox-visible)
# ---------------------------------------------------------------------------


class ClickHouseSource(ProxyObject):
    """Root entry point for a ClickHouse analytics database.

    Provides schema discovery (.databases, .tables, .describe_table) and
    SQL query execution (.query). The agent writes standard ClickHouse SQL;
    results come back as lists of dicts.
    """

    def __init__(self, client: ClickHouseClient, database: str | None = None):
        self._client = client
        self._database = database

        doc = "ClickHouse analytics database. Write standard ClickHouse SQL."

        schema = self._fetch_schema_summary()
        if schema:
            doc += f"\n\nTable schemas:\n{schema}"

        super().__init__(
            proxy_id="clickhouse",
            type_name="ClickHouseSource",
            doc=doc,
            dir_attrs=["databases", "tables", "query", "describe_table"],
            attr_docs={
                "databases": "List of database names on the server (excludes system databases).",
                "tables": "Tables in the default database. List of dicts, each with "
                    "'name' (str), 'engine' (str), and 'total_rows' (int, approximate count). "
                    "Use describe_table(name) to see columns.",
                "query": "query(sql) — Execute a SQL query (readonly user). "
                    "Returns the standard ClickHouse JSON response: "
                    "{'meta': [...], 'data': [{...}, ...], 'rows': N, "
                    "'rows_before_limit_at_least': N, 'statistics': {...}}. "
                    "Hits the server on every call (not cached).",
                "describe_table": "describe_table(name, database=None) — Column names, types, "
                    "default expressions, and comments for a table. Returns list of dicts. "
                    "Hits the server on every call (not cached).",
            },
            methods={
                "query": self._query,
                "describe_table": self._describe_table,
            },
        )
        self._databases = None
        self._databases_fetched_at = 0.0
        self._tables = None
        self._tables_fetched_at = 0.0

    def _fetch_schema_summary(self) -> str:
        """Fetch column names and types for all non-system tables (one query)."""
        try:
            if self._database:
                where = f"database = '{self._database}'"
            else:
                where = (
                    "database NOT IN "
                    "('system', 'INFORMATION_SCHEMA', 'information_schema')"
                )
            result = self._client.execute(
                f"SELECT table, name, type FROM system.columns "
                f"WHERE {where} ORDER BY table, position"
            )
            tables: dict[str, list[str]] = {}
            for row in result.get("data", []):
                tables.setdefault(row["table"], []).append(
                    f"{row['name']} {row['type']}"
                )
            return "\n".join(
                f"  {table}({', '.join(cols)})"
                for table, cols in tables.items()
            )
        except Exception:
            log.debug("Failed to fetch schema summary", exc_info=True)
            return ""

    @property
    def databases(self):
        """Database names, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._databases is None or (now - self._databases_fetched_at) > 3600:
            result = self._client.execute("SHOW DATABASES")
            self._databases = [
                row["name"]
                for row in result.get("data", [])
                if row.get("name") not in (
                    "system", "INFORMATION_SCHEMA", "information_schema",
                )
            ]
            self._databases_fetched_at = now
        return self._databases

    @property
    def tables(self):
        """Tables in the default database, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._tables is None or (now - self._tables_fetched_at) > 3600:
            if self._database:
                where = f"database = '{self._database}'"
            else:
                where = (
                    "database NOT IN "
                    "('system', 'INFORMATION_SCHEMA', 'information_schema')"
                )
            result = self._client.execute(
                f"SELECT name, engine, total_rows "
                f"FROM system.tables "
                f"WHERE {where} "
                f"ORDER BY name"
            )
            self._tables = [
                {
                    "name": row["name"],
                    "engine": row.get("engine", ""),
                    "total_rows": row.get("total_rows", 0),
                }
                for row in result.get("data", [])
            ]
            self._tables_fetched_at = now
        return self._tables

    def _query(self, sql):
        """Execute a SQL query. Host-side implementation."""
        return self._client.execute(sql, database=self._database)

    def _describe_table(self, name, database=None):
        """Describe a table's columns. Host-side implementation."""
        db = database or self._database
        if db:
            qualified = f"`{db}`.`{name}`"
        else:
            qualified = f"`{name}`"
        result = self._client.execute(f"DESCRIBE TABLE {qualified}")
        return result.get("data", [])
