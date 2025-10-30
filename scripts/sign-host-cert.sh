#!/bin/bash
#
# Test out an ssh cert in development.
# You can make one with:
#	ssh-keygen -t ed25519 -C "exe.dev host CA" -f ssh_ca
# Then run this script to sign the host key in your dev DB.
#
set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "Usage: $0 /path/to/exe.db /path/to/ssh_ca_key" >&2
	exit 1
fi

DB_PATH=$1
CA_KEY_PATH=$2

if [ ! -f "$DB_PATH" ]; then
	echo "Database file not found: $DB_PATH" >&2
	exit 1
fi

if [ ! -f "$CA_KEY_PATH" ]; then
	echo "CA key file not found: $CA_KEY_PATH" >&2
	exit 1
fi

if ! command -v sqlite3 >/dev/null 2>&1; then
	echo "sqlite3 binary not found in PATH" >&2
	exit 1
fi

if ! command -v ssh-keygen >/dev/null 2>&1; then
	echo "ssh-keygen binary not found in PATH" >&2
	exit 1
fi

HOST_PUBLIC_KEY=$(sqlite3 "$DB_PATH" "SELECT public_key FROM ssh_host_key WHERE id = 1;")
if [ -z "$HOST_PUBLIC_KEY" ]; then
	echo "No SSH host public key found in database" >&2
	exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

host_pub_file="$tmpdir/host_key.pub"
printf '%s\n' "$HOST_PUBLIC_KEY" >"$host_pub_file"

IDENTITY="exe-dev-host"
PRINCIPALS="localhost,127.0.0.1"
VALIDITY="+0s:+52w"

ssh-keygen -s "$CA_KEY_PATH" -I "$IDENTITY" -h -n "$PRINCIPALS" -V "$VALIDITY" "$host_pub_file" >/dev/null

host_cert_file="${host_pub_file%.pub}-cert.pub"
if [ ! -f "$host_cert_file" ]; then
	echo "ssh-keygen did not produce expected host certificate file" >&2
	exit 1
fi

sqlite3 "$DB_PATH" "UPDATE ssh_host_key SET cert_sig = CAST(readfile('$host_cert_file') AS TEXT), updated_at = CURRENT_TIMESTAMP WHERE id = 1;"

CA_PUBLIC_KEY=$(ssh-keygen -yf "$CA_KEY_PATH")
if [ -z "$CA_PUBLIC_KEY" ]; then
	echo "Failed to derive CA public key" >&2
	exit 1
fi

printf '@cert-authority [localhost]:2222 %s exe-dev-ca\n' "$CA_PUBLIC_KEY"
