---
title: How do I set up tab completion for VM names?
description: Autocomplete exe.xyz VM hostnames in your favorite shell
subheading: "5. FAQ"
suborder: 9
published: true
---

Two options

- dynamic completion that queries your VM list on each tab press
- static SSH config approach

Dynamic completion never goes out of date, but has a lag. Works best with zsh.

Static completion is more general purpose, but requires updating when your VM list changes.

## Dynamic completion

The trick is to query `ssh exe.dev ls --json` on each tab press. Requires `jq`.

### Zsh

Add to `~/.zshrc`:

```zsh
_exe_hosts() {
    reply=(${(f)"$(ssh exe.dev ls --json 2>/dev/null | jq -r '.vms[].ssh_dest')"})
}
zstyle -e ':completion:*:(ssh|scp|rsync):*' hosts '_exe_hosts'
```

The `zstyle` approach only overrides the hosts list. Flag and path completion still work normally.

### Bash

See the caveat below before using!

Add to `~/.bashrc`:

```bash
_exe_hosts() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local hosts
    hosts=$(ssh exe.dev ls --json 2>/dev/null | jq -r '.vms[].ssh_dest')
    COMPREPLY=($(compgen -W "$hosts" -- "$cur"))
}
complete -F _exe_hosts -o default ssh scp rsync
```

Caveat: `complete -F` replaces the default completion function for these commands, so you lose flag completion (`ssh -<TAB>`). If that hurts, consider using the static SSH config approach instead.

### Fish

Add to `~/.config/fish/config.fish`:

```fish
complete -c ssh -fa '(ssh exe.dev ls --json 2>/dev/null | jq -r ".vms[].ssh_dest")'
complete -c scp -fa '(ssh exe.dev ls --json 2>/dev/null | jq -r ".vms[].ssh_dest")'
complete -c rsync -fa '(ssh exe.dev ls --json 2>/dev/null | jq -r ".vms[].ssh_dest")'
```

## SSH config approach (any shell)

Add your VM names to `~/.ssh/config`, so shells pick them up automatically.

One time, add to `~/.ssh/config`:

```
Include ~/.ssh/exe-hosts
```

Every time you create, delete, or rename a VM, regenerate the config:

```
ssh exe.dev ls --json | jq -r '.vms[].ssh_dest' | sed 's/^/Host /' > ~/.ssh/exe-hosts
```
