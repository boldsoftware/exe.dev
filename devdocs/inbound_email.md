# Inbound Email Architecture

## Overview

```
Internet -> MX (maddy) -> LMTP (exed) -> SCP -> VM Maildir
```

1. MX records for `*.exe.xyz` point to `mail.exe.xyz`
2. maddy accepts mail and forwards to exed via LMTP over a Unix socket
3. exed validates the recipient and delivers to the VM via SCP

## Components

### MX Records (exens)

- Served by the embedded DNS server in exens
- Only returned for boxes with `email_receive_enabled=1`
- Points to `mail.{boxHost}` (e.g., `mail.exe.xyz` for prod, `mail.exe-staging.xyz` for staging)

### maddy (ops/maddy/)

External mail server (v0.8.2) that handles:
- TLS termination using wildcard certs managed by exed (DNS-01 via exens)
- SPF/DKIM validation (SPF failures are rejected)
- Rate limiting (10/s per source, 5 concurrent connections)
- Forwarding to LMTP socket at `/var/run/exed/lmtp.sock`
- Ports: 25 (STARTTLS), 465 (implicit TLS)

All recipient validation is deferred to the LMTP server because maddy 0.8.2 lacks the filtering primitives needed to check box existence at RCPT time. See `ops/maddy/maddy.conf` for details.

### LMTP Server (execore/lmtp.go)

Listens on Unix socket `/var/run/exed/lmtp.sock`. Validates recipients in the `Rcpt()` method (before DATA phase) in this order:

1. Parse email syntax
2. Domain must end with `.{BoxHost}` (e.g., `.exe.xyz`)
3. Only single-level subdomains (rejects `a.b.exe.xyz`)
4. Box must exist in DB
5. Box must have `email_receive_enabled=1`

SMTP error codes:
- `550 5.1.1` -- mailbox not found or email disabled
- `550 5.1.2` -- invalid domain
- `451 4.3.0` -- temporary failure
- `552 5.3.4` -- message too large (>1MB)

### Email Delivery

- Prepends `Delivered-To: <recipient>` header
- Computes SHA256 of full message (header + body) for content-addressable filename
- Delivers via SCP to `{email_maildir_path}/new/{hash}.eml`
- Atomic write prevents duplicates

### Email Limit Enforcement

After each successful delivery, the LMTP server counts files in `~/Maildir/new/`. If the count exceeds `MaxMaildirEmails`:

1. Sets `email_receive_enabled=0` and clears `email_maildir_path` in DB
2. Sends email notification to the VM owner
3. MX records stop being served for the box

| Stage | Limit |
|-------|-------|
| Test | 5 |
| Local | 5 |
| Staging | 1,000 |
| Prod | 1,000 |

## Database Columns (boxes table)

```sql
email_receive_enabled INTEGER NOT NULL DEFAULT 0
email_maildir_path TEXT NOT NULL DEFAULT ''
```

## REPL Command

```
share receive-email <vm> [on|off]
```

When enabling: sets `email_receive_enabled=1`, creates `~/Maildir/new/` on VM, writes welcome email if first-time setup.

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

## Debugging

```bash
# Check exed LMTP logs
journalctl -u exed | grep -i lmtp

# Verify MX record
dig MX vmname.exe.xyz @ns1.exe.dev

# Test SMTP connectivity
swaks --to test@vmname.exe.xyz --server mail.exe.xyz

# Check delivery
ssh vmname ls ~/Maildir/new/
```
