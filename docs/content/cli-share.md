---
# GENERATED; rebuild with go run ./cmd/gencmddocs
title: "share"
description: "Share HTTPS box access with others"
subheading: "4. CLI Reference"
suborder: 6
---

# share

Share HTTPS box access with others

## Usage

```
share <subcommand> <box> [args...]
```

## Options

- `--json`: output in JSON format

## Subcommands

### share show

Show current shares for a box

**Usage:**
```
share show <box>
```

**Options:**
- `--json`: output in JSON format

### share port

Set the HTTP proxy port for a box

**Usage:**
```
share port <box> [port]
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
share set-public <box>
```

**Options:**
- `--json`: output in JSON format

### share set-private

Restrict the HTTP proxy to authenticated users

**Usage:**
```
share set-private <box>
```

**Options:**
- `--json`: output in JSON format

### share add

Share box with a user via email

**Usage:**
```
share add <box> <email> [--message='...']
```

**Options:**
- `--json`: output in JSON format
- `--message`: message to include in share invitation

**Examples:**
```
share add mybox user@example.com
share add mybox user@example.com --message='Check this out'
```

### share remove

Revoke a user's access to a box

**Usage:**
```
share remove <box> <email>
```

**Options:**
- `--json`: output in JSON format

### share add-link

Create a shareable link for a box

**Usage:**
```
share add-link <box>
```

**Aliases:** add-share-link

**Options:**
- `--json`: output in JSON format

### share remove-link

Revoke a shareable link

**Usage:**
```
share remove-link <box> <token>
```

**Aliases:** remove-share-link

**Options:**
- `--json`: output in JSON format

