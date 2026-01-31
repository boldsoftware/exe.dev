# Inbound Email Architecture

This document describes how inbound email works for exe.dev VMs.

## Overview

```
Internet → MX (maddy) → LMTP (exed) → SCP → VM Maildir
```

1. MX records for `*.exe.xyz` point to `mail.exe.xyz`
2. maddy accepts mail and forwards to exed via LMTP
3. exed validates the recipient and delivers via SCP to the VM

## Components

### MX Records (exens)

- Served by the embedded DNS server in exens
- Only returned for boxes with `email_receive_enabled=1`
- Points to `mail.{boxHost}` (e.g., `mail.exe.xyz` for prod, `mail.exe-staging.xyz` for staging)

### LMTP Server (execore/lmtp.go)

- Listens on Unix socket `/var/run/exed/lmtp.sock`
- Validates recipients:
  - Domain must be `*.exe.xyz`
  - Box must exist
  - Box must have `email_receive_enabled=1`
- Returns SMTP error codes:
  - `550 5.1.1` - mailbox not found / email disabled
  - `550 5.1.2` - invalid domain
  - `451 4.3.0` - temporary failure

### Email Delivery

- Prepends `Delivered-To: <recipient>` header to identify the envelope recipient
- Connects to VM via SSH using stored credentials
- Atomically writes email to `~/Maildir/new/{hash}.eml` (via temp file in /tmp)
- Content-addressable filename prevents duplicates

### Email Limit Enforcement

After each successful delivery, the LMTP server counts emails in `~/Maildir/new/`. If the count exceeds the configured limit (`stage.Env.MaxMaildirEmails`):
1. `email_receive_enabled` is set to 0 in the database
2. An email notification is sent to the VM owner
3. MX records are no longer served for the box

Limits per stage:
- Test: 5
- Local: 5
- Staging: 1,000
- Prod: 1,000

### maddy (ops/maddy/)

External mail server that handles:
- TLS termination (ACME via Route53)
- SPF/DKIM validation
- Forwarding to LMTP

## Database Schema

```sql
ALTER TABLE boxes ADD COLUMN email_receive_enabled INTEGER NOT NULL DEFAULT 0;
```

## REPL Command

```
share receive-email <vm> [on|off]
```

When enabling:
1. Sets `email_receive_enabled=1` in database
2. Creates `~/Maildir/new/` on VM
3. Writes welcome email if first-time setup

## Local Testing

Connect directly to the LMTP socket (no maddy needed):

```bash
nc -U /var/run/exed/lmtp.sock
LHLO localhost
MAIL FROM:<test@example.com>
RCPT TO:<anything@vmname.exe.xyz>
DATA
Subject: Test

Hello
.
QUIT
```

Check delivery:

```bash
ssh vmname ls ~/Maildir/new/
```

## Debugging

Check exed logs for LMTP errors:

```bash
journalctl -u exed | grep -i lmtp
```

Verify MX record:

```bash
dig MX vmname.exe.xyz @ns1.exe.dev
```

Test SMTP connectivity:

```bash
swaks --to test@vmname.exe.xyz --server mail.exe.xyz
```
