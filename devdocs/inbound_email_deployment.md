# Inbound Email Deployment Checklist

This document describes how to deploy and verify inbound email for exe.dev VMs.

## Prerequisites

### DNS (Automatic)

The following DNS records are served automatically by exens:
- A record for `mail.exe.xyz` (and `mail.exe-staging.xyz`) points to the lobby IP
- MX records for `<box>.exe.xyz` point to `mail.exe.xyz` (only for boxes with email enabled)

### AWS Security Groups

Open the following ports on exed hosts:
- Port 25 (SMTP with STARTTLS)
- Port 465 (SMTP with implicit TLS)

### TLS Certificates (Automatic)

maddy uses the existing wildcard certificate managed by exed. No separate ACME setup is required.

exed obtains and renews `*.exe.xyz` (and `*.exe-staging.xyz`) certificates via DNS-01 challenges through exens, storing them at `/home/ubuntu/certs/<domain>`. maddy reads from this same location.

## Deployment

### 1. Deploy exed with new code

```bash
./ops/deploy/deploy-exed-staging.sh  # test on staging first
./ops/deploy/deploy-exed-prod.sh
```

This will:
- Run the database migration (adds `email_receive_enabled` column)
- Start the LMTP server on `/var/run/exed/lmtp.sock`
- Serve MX and A records for `mail.exe.xyz`

### 2. Deploy maddy

```bash
./ops/deploy/deploy-maddy-staging.sh  # test on staging first
./ops/deploy/deploy-maddy-prod.sh
```

## Verification

### Check DNS resolution

```bash
dig MX testvm.exe.xyz @ns1.exe.dev
dig A mail.exe.xyz @ns1.exe.dev
```

### Check services

```bash
ssh ubuntu@exed-02 sudo systemctl status maddy
ssh ubuntu@exed-02 ls -la /var/run/exed/lmtp.sock
```

### View logs

```bash
ssh ubuntu@exed-02 journalctl -fu maddy
ssh ubuntu@exed-02 journalctl -fu exed | grep -i lmtp
```

### Test mail delivery

```bash
# Using swaks (install: apt install swaks)
swaks --to test@vmname.exe.xyz --server mail.exe.xyz

# Or with telnet
telnet mail.exe.xyz 25
EHLO test
MAIL FROM:<test@example.com>
RCPT TO:<test@vmname.exe.xyz>
DATA
Subject: Test

Test message
.
QUIT
```

### Verify TLS certificate

```bash
openssl s_client -connect mail.exe.xyz:465 -quiet
```

### Verify delivery

```bash
ssh testvm ls ~/Maildir/new/
```

## Troubleshooting

### LMTP connection refused

Check that exed is running and the socket exists:

```bash
ls -la /var/run/exed/lmtp.sock
```

Check that maddy has permission to access the socket (maddy user should be in the ubuntu group).

### TLS certificate issues

maddy uses the cert from `/home/ubuntu/certs/<domain>` managed by exed. Check that:

1. exed is running and has obtained a certificate:
   ```bash
   ls -la /home/ubuntu/certs/
   ```

2. maddy can read the cert (check for permission errors):
   ```bash
   journalctl -u maddy | grep -i tls
   ```

3. The cert is valid:
   ```bash
   openssl x509 -in /home/ubuntu/certs/exe.xyz -text -noout | grep -A2 Validity
   ```

### SPF/DKIM failures

Check maddy logs for authentication results:

```bash
journalctl -u maddy | grep -i spf
journalctl -u maddy | grep -i dkim
```

## Configuration Files

- `/etc/maddy/maddy.conf` - Main configuration
- `/etc/default/maddy` - Environment variables
- `/var/lib/maddy/` - State and queue storage
