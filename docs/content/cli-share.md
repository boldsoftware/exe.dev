---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "share"
description: "Share HTTPS VM access with others"
subheading: "8. CLI Reference"
suborder: 8
---

# share

Share HTTPS VM access with others

## Usage

```
share <subcommand> <vm> [args...]
```

## Options

- `--json`: output in JSON format

## Subcommands

### share show

Show current shares for a VM

**Usage:**
```
share show <vm>
```

**Options:**
- `--json`: output in JSON format
- `--qr`: show QR code for the URL

### share port

Set the HTTP proxy port for a VM

**Usage:**
```
share port <vm> [port]
```

**Options:**
- `--json`: output in JSON format

**Examples:**
```
share port mybox 8080
```

### share set-public

Make the HTTP proxy publicly accessible

**Usage:**
```
share set-public <vm>
```

**Options:**
- `--json`: output in JSON format

### share set-private

Restrict the HTTP proxy to authenticated users

**Usage:**
```
share set-private <vm>
```

**Options:**
- `--json`: output in JSON format

### share add

Share VM with a user via email

**Usage:**
```
share add <vm> <email> [--message='...']
```

**Options:**
- `--json`: output in JSON format
- `--message`: message to include in share invitation
- `--qr`: show QR code for the share URL

**Examples:**
```
share add mybox user@example.com
share add mybox user@example.com --message='Check this out'
```

### share remove

Revoke a user's access to a VM

**Usage:**
```
share remove <vm> <email>
```

**Options:**
- `--json`: output in JSON format

### share add-link

Create a shareable link for a VM

**Usage:**
```
share add-link <vm>
```

**Aliases:** add-share-link

**Options:**
- `--json`: output in JSON format
- `--qr`: show QR code for the URL

### share remove-link

Revoke a shareable link

**Usage:**
```
share remove-link <vm> <token>
```

**Aliases:** remove-share-link

**Options:**
- `--json`: output in JSON format

