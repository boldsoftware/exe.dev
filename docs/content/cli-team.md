---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "team"
description: "View and manage your team"
subheading: "9. CLI Reference"
suborder: 17
published: true
---

# team

View and manage your team

## Options

- `--json`: output in JSON format

## Subcommands

### team enable

Create a new team

**Usage:**
```
team enable
```

### team disable

Disband your team

**Usage:**
```
team disable
```

### team members

List team members

**Usage:**
```
team members
```

**Aliases:** ls

**Options:**
- `--json`: output in JSON format

### team add

Add a user to the team

**Usage:**
```
team add <email> [<user|admin|billing_owner>]
```

**Options:**
- `--json`: output in JSON format

### team remove

Remove a user from the team

**Usage:**
```
team remove <email>
```

**Options:**
- `--json`: output in JSON format

### team role

Change a team member's role

**Usage:**
```
team role <email> <user|admin|billing_owner>
```

**Options:**
- `--json`: output in JSON format

### team transfer

Transfer a VM to another team member

**Usage:**
```
team transfer <vm_name> <target_email>
```

**Options:**
- `--json`: output in JSON format

### team auth

View and manage team auth settings

**Usage:**
```
team auth
```

**Options:**
- `--json`: output in JSON format

