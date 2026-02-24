#!/usr/bin/env python3
"""Export non-secret tables from an exe.db into a new SQLite database.

Usage: export-db.py <source> <dest.db>

Source can be:
  - an exe.db file (SQLite database)
  - an exe.db.TIMESTAMP.sql.zst file (zstd-compressed SQL dump, requires zstd)
"""
import os
import sqlite3
import subprocess
import sys
import tempfile

# Tables to skip entirely (secrets, tokens, ephemeral auth state).
SKIP_TABLES = {
    "ssh_host_key",
    "auth_cookies",
    "auth_tokens",
    "email_verifications",
    "pending_ssh_keys",
    "pending_registrations",
    "passkey_challenges",
    "checkout_params",
    "server_meta",
    "mobile_pending_vm",
    "hll_sketches",
}

# Columns to redact per table. Value is the SQL replacement expression.
REDACT_COLUMNS = {
    "boxes": {
        "ssh_server_identity_key": "NULL",
        "ssh_authorized_keys": "NULL",
        "ssh_client_private_key": "NULL",
        "creation_log": "NULL",
    },
    "ssh_keys": {
        "public_key": "'[redacted-' || id || ']'",
    },
    "passkeys": {
        "credential_id": "X'00'",
        "public_key": "X'00'",
        "aaguid": "NULL",
    },
}


def get_tables(conn):
    rows = conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
    ).fetchall()
    return [r[0] for r in rows]


def get_columns(conn, table):
    rows = conn.execute(f"PRAGMA table_info('{table}')").fetchall()
    return [r[1] for r in rows]


def get_create_sql(conn, table):
    """Get all CREATE statements for a table (table + indexes)."""
    stmts = []
    for row in conn.execute(
        "SELECT sql FROM sqlite_master WHERE tbl_name=? AND sql IS NOT NULL ORDER BY type DESC",
        (table,),
    ).fetchall():
        stmts.append(row[0])
    return stmts


def export(src_path, dst_path):
    src = sqlite3.connect(src_path)
    dst = sqlite3.connect(dst_path)

    for table in get_tables(src):
        if table in SKIP_TABLES:
            print(f"skip: {table}")
            continue

        redact = REDACT_COLUMNS.get(table, {})
        label = f"{table} (redacted)" if redact else table
        print(f"copy: {label}")

        # Create table and indexes.
        for stmt in get_create_sql(src, table):
            dst.execute(stmt)

        # Build SELECT with redacted columns.
        cols = get_columns(src, table)
        select_cols = []
        for c in cols:
            if c in redact:
                select_cols.append(redact[c])
            else:
                select_cols.append(f'"{c}"')

        placeholders = ",".join("?" for _ in cols)
        select = f"SELECT {','.join(select_cols)} FROM \"{table}\""
        col_list = ",".join(f'"{c}"' for c in cols)
        insert = f'INSERT INTO "{table}" ({col_list}) VALUES ({placeholders})'

        rows = src.execute(select).fetchall()
        if rows:
            dst.executemany(insert, rows)

    dst.commit()
    dst.close()
    src.close()
    print(f"\nExported to {dst_path}")


def restore_sql_zst(zst_path):
    """Decompress a .sql.zst dump into a temporary SQLite database. Returns the temp db path."""
    print(f"Restoring {zst_path} ...")
    fd, tmp_db = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    try:
        zstd = subprocess.Popen(["zstd", "-d", zst_path, "-c"], stdout=subprocess.PIPE)
        sqlite = subprocess.Popen(["sqlite3", tmp_db], stdin=zstd.stdout)
        zstd.stdout.close()
        sqlite.wait()
        zstd.wait()
        if zstd.returncode != 0:
            raise RuntimeError(f"zstd exited {zstd.returncode}")
        if sqlite.returncode != 0:
            raise RuntimeError(f"sqlite3 exited {sqlite.returncode}")
    except Exception:
        os.unlink(tmp_db)
        raise
    print(f"Restored to {tmp_db}")
    return tmp_db


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} <source.db|source.sql.zst> <dest.db>", file=sys.stderr)
        sys.exit(1)

    src_path, dst_path = sys.argv[1], sys.argv[2]
    if not os.path.isfile(src_path):
        print(f"Error: source '{src_path}' not found", file=sys.stderr)
        sys.exit(1)
    if os.path.exists(dst_path):
        print(f"Error: destination '{dst_path}' already exists", file=sys.stderr)
        sys.exit(1)

    tmp_db = None
    if src_path.endswith(".sql.zst"):
        tmp_db = restore_sql_zst(src_path)
        src_path = tmp_db

    try:
        export(src_path, dst_path)
    finally:
        if tmp_db:
            os.unlink(tmp_db)
