# Inbound Email Deployment

## Prerequisites

### DNS (Automatic)

Served automatically by exens:
- A record for `mail.exe.xyz` (and `mail.exe-staging.xyz`) -> lobby IP
- MX records for `<box>.exe.xyz` -> `mail.exe.xyz` (only for boxes with email enabled)

### AWS Security Groups

Open on exed hosts:
- Port 25 (SMTP with STARTTLS)
- Port 465 (SMTP with implicit TLS)

### TLS Certificates (Automatic)

exed obtains and renews `*.exe.xyz` (and `*.exe-staging.xyz`) certificates via DNS-01 challenges through exens, stored at `/home/ubuntu/certs/<domain>`. maddy reads the same cert files -- no separate ACME setup needed.

## Deployment

### 1. Deploy exed

```bash
./ops/deploy/deploy-exed-staging.sh  # test on staging first
./ops/deploy/deploy-exed-prod.sh
```

This starts the LMTP server on `/var/run/exed/lmtp.sock` and serves MX/A records for `mail.{domain}`.

### 2. Deploy maddy

```bash
./ops/deploy/deploy-maddy-staging.sh  # staging: exed-staging-01, exe-staging.xyz
./ops/deploy/deploy-maddy-prod.sh     # prod: exed-02, exe.xyz
```

Installs maddy 0.8.2 from GitHub releases, deploys config from `ops/maddy/maddy.conf` (with `{BOX_DOMAIN}` substituted), and starts the systemd service.

## Verification

```bash
# DNS resolution
dig MX testvm.exe.xyz @ns1.exe.dev
dig A mail.exe.xyz @ns1.exe.dev

# Service status
ssh ubuntu@exed-02 sudo systemctl status maddy
ssh ubuntu@exed-02 ls -la /var/run/exed/lmtp.sock

# Logs
ssh ubuntu@exed-02 journalctl -fu maddy
ssh ubuntu@exed-02 journalctl -fu exed | grep -i lmtp

# Test delivery
swaks --to test@vmname.exe.xyz --server mail.exe.xyz

# Verify TLS
openssl s_client -connect mail.exe.xyz:465 -quiet

# Check delivery
ssh testvm ls ~/Maildir/new/
```

## Troubleshooting

| Issue | Check |
|-------|-------|
| LMTP connection refused | `ls -la /var/run/exed/lmtp.sock` -- exed must be running. maddy user must be in ubuntu group. |
| TLS certificate issues | `ls -la /home/ubuntu/certs/` -- exed manages certs. `journalctl -u maddy \| grep -i tls` for maddy errors. `openssl x509 -in /home/ubuntu/certs/exe.xyz -text -noout \| grep -A2 Validity` for expiry. |
| SPF/DKIM failures | `journalctl -u maddy \| grep -i spf` and `grep -i dkim` |

## Configuration Files

| Path | Purpose |
|------|---------|
| `/etc/maddy/maddy.conf` | Main configuration |
| `/etc/default/maddy` | Environment variables (`BOX_DOMAIN`) |
| `/var/lib/maddy/` | State and queue storage |
| `ops/maddy/maddy.conf` | Source template (repo) |
| `ops/maddy/maddy.service` | Systemd unit (repo) |
