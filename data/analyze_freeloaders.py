#!/usr/bin/env python3
"""Analyze VM free-loaders by cross-referencing exe.db with Stripe subscriptions."""

import argparse
import base64
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import urllib.request


def load_db(path):
    """Load a .sql.zst, .sql, or .sqlite3/.db file into a SQLite connection."""
    if path.endswith(".sql.zst"):
        # Decompress with zstd
        try:
            subprocess.run(["zstd", "--version"], capture_output=True, check=True)
        except FileNotFoundError:
            sys.exit("Error: 'zstd' command not found. Install it with: brew install zstd")
        sql_path = tempfile.mktemp(suffix=".sql")
        subprocess.run(["zstd", "-d", path, "-o", sql_path, "-f"], check=True)
        conn = _import_sql(sql_path)
        os.unlink(sql_path)
        return conn
    elif path.endswith(".sql"):
        return _import_sql(path)
    else:
        # Assume existing SQLite DB
        conn = sqlite3.connect(path)
        conn.row_factory = sqlite3.Row
        return conn


def _import_sql(sql_path):
    conn = sqlite3.connect(":memory:")
    conn.row_factory = sqlite3.Row
    with open(sql_path, "r") as f:
        conn.executescript(f.read())
    return conn


def fetch_stripe_active_customers(api_key):
    """Paginate Stripe /v1/subscriptions?status=active and return set of customer IDs."""
    auth = base64.b64encode(f"{api_key}:".encode()).decode()
    base_url = "https://api.stripe.com/v1/subscriptions?limit=100&status=active"

    customers = set()
    url = base_url
    pages = 0

    while url:
        req = urllib.request.Request(url)
        req.add_header("Authorization", f"Basic {auth}")
        with urllib.request.urlopen(req) as resp:
            data = json.loads(resp.read())

        for sub in data["data"]:
            customers.add(sub["customer"])

        pages += 1
        if data.get("has_more") and data["data"]:
            last_id = data["data"][-1]["id"]
            url = base_url + f"&starting_after={last_id}"
        else:
            url = None

    print(f"Stripe: {len(customers)} active subscribers ({pages} pages fetched)")
    return customers


def classify_users(conn, stripe_active):
    """Classify every user into a payment category. Returns dict user_id -> category."""
    # Map user_id -> (billing_exemption, account_id)
    rows = conn.execute("""
        SELECT u.user_id, u.billing_exemption, a.id AS account_id
        FROM users u
        LEFT JOIN accounts a ON a.created_by = u.user_id
    """).fetchall()

    # Set of account_ids that ever had an 'active' billing event
    ever_active = set()
    for r in conn.execute(
        "SELECT DISTINCT account_id FROM billing_events WHERE event_type = 'active'"
    ):
        ever_active.add(r["account_id"])

    status = {}
    for r in rows:
        uid = r["user_id"]
        exemption = r["billing_exemption"]
        acct = r["account_id"]

        if exemption == "free":
            status[uid] = "free_exemption"
        elif exemption == "trial":
            status[uid] = "trial"
        elif acct and acct in stripe_active:
            status[uid] = "active_subscriber"
        elif acct and acct in ever_active:
            status[uid] = "churned_subscriber"
        elif acct:
            status[uid] = "account_never_paid"
        else:
            status[uid] = "no_account"

    return status


def aggregate(conn, user_status):
    """Aggregate box stats by payment category."""
    boxes = conn.execute(
        "SELECT id, status, created_by_user_id, allocated_cpus FROM boxes"
    ).fetchall()

    categories = [
        "active_subscriber",
        "churned_subscriber",
        "account_never_paid",
        "trial",
        "free_exemption",
        "no_account",
    ]
    stats = {
        c: {"total": 0, "running": 0, "stopped": 0, "users": set(), "cpus": 0}
        for c in categories
    }

    for b in boxes:
        uid = b["created_by_user_id"]
        cat = user_status.get(uid, "no_account")
        s = stats[cat]
        s["total"] += 1
        s["users"].add(uid)
        if b["status"] == "running":
            s["running"] += 1
            s["cpus"] += b["allocated_cpus"] or 0
        elif b["status"] == "stopped":
            s["stopped"] += 1

    return categories, stats


def print_table(categories, stats):
    header = f"{'Category':<26} {'Total VMs':>10} {'Running':>10} {'Stopped':>10} {'Users':>8} {'CPUs (run)':>11}"
    sep = "=" * len(header)
    print()
    print(header)
    print(sep)

    paying_running = paying_cpus = 0
    nonpay_running = nonpay_cpus = 0

    for cat in categories:
        s = stats[cat]
        users = len(s["users"])
        print(
            f"{cat:<26} {s['total']:>10} {s['running']:>10} {s['stopped']:>10} {users:>8} {s['cpus']:>11}"
        )
        if cat == "active_subscriber":
            paying_running = s["running"]
            paying_cpus = s["cpus"]
        else:
            nonpay_running += s["running"]
            nonpay_cpus += s["cpus"]

    print(sep)

    total_running = paying_running + nonpay_running
    total_cpus = paying_cpus + nonpay_cpus

    print(f"{'PAYING':<26} {'':>10} {paying_running:>10} {'':>10} {'':>8} {paying_cpus:>11}")
    print(f"{'NOT PAYING':<26} {'':>10} {nonpay_running:>10} {'':>10} {'':>8} {nonpay_cpus:>11}")
    print()

    if total_running:
        pp = paying_running / total_running * 100
        print(f"Running VMs: {paying_running} paying ({pp:.1f}%) vs {nonpay_running} non-paying ({100-pp:.1f}%)")
    if total_cpus:
        cp = paying_cpus / total_cpus * 100
        print(f"Running CPUs: {paying_cpus} paying ({cp:.1f}%) vs {nonpay_cpus} non-paying ({100-cp:.1f}%)")
    print()

    for cat in categories:
        s = stats[cat]
        users = len(s["users"])
        if users and s["running"]:
            avg_vms = s["running"] / users
            avg_cpus = s["cpus"] / s["running"] if s["running"] else 0
            print(f"{cat:<26} avg {avg_vms:.1f} running VMs/user, avg {avg_cpus:.1f} CPUs/running VM")


def main():
    parser = argparse.ArgumentParser(description="Analyze VM free-loaders vs paying customers")
    parser.add_argument("db_path", help="Path to exe.db (.sql.zst, .sql, or .sqlite3/.db)")
    parser.add_argument("--stripe-key", required=True, help="Stripe API key")
    args = parser.parse_args()

    if not os.path.exists(args.db_path):
        sys.exit(f"Error: {args.db_path} not found")

    print(f"Loading {args.db_path}...")
    conn = load_db(args.db_path)

    stripe_active = fetch_stripe_active_customers(args.stripe_key)

    print("Classifying users...")
    user_status = classify_users(conn, stripe_active)

    categories, stats = aggregate(conn, user_status)
    print_table(categories, stats)

    conn.close()


if __name__ == "__main__":
    main()
