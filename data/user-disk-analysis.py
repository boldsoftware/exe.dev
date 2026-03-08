#!/usr/bin/env python3
"""
Snapshot exe.dev data into a SQLite database and print reports.

Pulls from:
  - /debug/users?format=json        (users + credit/LLM usage)
  - /debug/vms?format=json&source=db (VMs)
  - Prometheus exelet_vm_disk_used_bytes (disk usage per VM)
"""

import json
import sqlite3
import sys
import urllib.request

EXED_HOST = "https://exed-02.crocodile-vector.ts.net"
PROM_HOST = "http://mon:9090"
DB_PATH = "snapshot.db"


def fetch_json(url):
    req = urllib.request.Request(url)
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read())


def fetch_users():
    return fetch_json(f"{EXED_HOST}/debug/users?format=json")


def fetch_boxes():
    return fetch_json(f"{EXED_HOST}/debug/vms?format=json&source=db")


def fetch_disk_usage():
    """Query Prometheus for exelet_vm_disk_used_bytes, return {vm_name: bytes}."""
    url = f"{PROM_HOST}/api/v1/query?query=exelet_vm_disk_used_bytes"
    data = fetch_json(url)
    result = {}
    for series in data.get("data", {}).get("result", []):
        name = series["metric"].get("vm_name", "")
        value = float(series["value"][1])
        result[name] = value
    return result


def build_db(users, boxes, disk_usage):
    db = sqlite3.connect(DB_PATH)
    db.execute("DROP TABLE IF EXISTS users")
    db.execute("DROP TABLE IF EXISTS vms")
    db.execute("""
        CREATE TABLE users (
            user_id TEXT PRIMARY KEY,
            email TEXT,
            credit_total_used_usd REAL
        )
    """)
    db.execute("""
        CREATE TABLE vms (
            name TEXT PRIMARY KEY,
            user_id TEXT,
            disk_used_bytes REAL
        )
    """)
    for u in users:
        db.execute(
            "INSERT INTO users VALUES (?, ?, ?)",
            (u["user_id"], u["email"], u["credit_total_used_usd"]),
        )
    for b in boxes:
        disk = disk_usage.get(b["name"], 0)
        db.execute(
            "INSERT OR IGNORE INTO vms VALUES (?, ?, ?)",
            (b["name"], b.get("owner_user_id", ""), disk),
        )
    db.commit()
    return db


def report(db):
    print("=== Top LLM Credit Consumers (USD) ===")
    print(f"{'Email':<40} {'Used ($)':>10}")
    print("-" * 52)
    for row in db.execute("""
        SELECT email, credit_total_used_usd
        FROM users
        ORDER BY credit_total_used_usd DESC
        LIMIT 20
    """):
        print(f"{row[0]:<40} {row[1]:>10.2f}")

    print()
    print("=== Top Disk Consumers (by user) ===")
    print(f"{'Email':<40} {'Disk (GB)':>10} {'VMs':>5}")
    print("-" * 57)
    for row in db.execute("""
        SELECT u.email,
               SUM(v.disk_used_bytes) AS total_disk,
               COUNT(v.name) AS vm_count
        FROM vms v
        JOIN users u ON v.user_id = u.user_id
        WHERE v.disk_used_bytes > 0
        GROUP BY v.user_id
        ORDER BY total_disk DESC
        LIMIT 20
    """):
        gb = row[1] / (1024 ** 3)
        print(f"{row[0]:<40} {gb:>10.2f} {row[2]:>5}")

    print()
    print("=== Top Disk Consumers (by VM) ===")
    print(f"{'VM Name':<30} {'Email':<30} {'Disk (GB)':>10}")
    print("-" * 72)
    for row in db.execute("""
        SELECT v.name, COALESCE(u.email, v.user_id), v.disk_used_bytes
        FROM vms v
        LEFT JOIN users u ON v.user_id = u.user_id
        WHERE v.disk_used_bytes > 0
        ORDER BY v.disk_used_bytes DESC
        LIMIT 20
    """):
        gb = row[2] / (1024 ** 3)
        print(f"{row[0]:<30} {row[1]:<30} {gb:>10.2f}")

    print()
    totals = db.execute("""
        SELECT COUNT(*), SUM(credit_total_used_usd) FROM users
    """).fetchone()
    vm_totals = db.execute("""
        SELECT COUNT(*), SUM(disk_used_bytes) FROM vms WHERE disk_used_bytes > 0
    """).fetchone()
    print(f"Total users: {totals[0]}, total credit used: ${totals[1]:.2f}")
    disk_tb = (vm_totals[1] or 0) / (1024 ** 4)
    print(f"VMs with disk data: {vm_totals[0]}, total disk used: {disk_tb:.2f} TB")


def main():
    print("Fetching users...")
    users = fetch_users()
    print(f"  {len(users)} users")

    print("Fetching boxes...")
    boxes = fetch_boxes()
    print(f"  {len(boxes)} boxes")

    print("Fetching disk usage from Prometheus...")
    disk_usage = fetch_disk_usage()
    print(f"  {len(disk_usage)} VMs with disk metrics")

    print(f"Building {DB_PATH}...")
    db = build_db(users, boxes, disk_usage)

    print()
    report(db)
    db.close()
    print(f"\nDatabase saved to {DB_PATH}")


if __name__ == "__main__":
    main()
