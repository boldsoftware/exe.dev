---
name: using-exe-dev
description: Guides working with exe.dev VMs, including first-time signup. Use when the user mentions exe.dev, exe VMs, *.exe.xyz, or tasks involving exe.dev infrastructure.
---

# About

exe.dev provides Linux VMs with persistent disks, instant HTTPS, and built-in auth. All management is via SSH.

## Documentation

- Docs index: https://exe.dev/docs.md
- All docs in one page (big!): https://exe.dev/docs/all.md

The index is organized for progressive discovery: start there and follow links as needed.

## Quick reference

```
ssh exe.dev help             # show commands
ssh exe.dev help <command>   # show command details
ssh exe.dev new --json       # create VM
ssh exe.dev ls --json        # list VMs
ssh exe.dev rm <vm>          # delete VM
ssh <vm>.exe.xyz             # connect to VM
scp file.txt <vm>.exe.xyz:~/ # transfer file
```

Every VM gets `https://<vm>.exe.xyz/` with automatic TLS.

## First-time signup

These outputs are not hangs or bugs:

- `{"error":"Registration is required.","action":{"type":"command","command":"ssh -t ..."}}` (exit 1): the SSH key has no account. Run `action.command` in a PTY after getting the human's email and consent.
- `{"error":"Choose a plan to start creating VMs.` ..., `"action":{"type":"command","command":"ssh ... billing activate --json"}}`: the account has no plan. Run `action.command` and see Recovery.

**Rules**

- Before running `ssh exe.dev`, ask the human for the email and confirm they want an exe.dev account. Never guess the email. If the email prompt is pre-filled, confirm it or clear it with Ctrl-U.
- Never read the user's mailbox, enter card details, or do the ChatGPT sign-in. The human verifies, authorizes, and pays.
- Opening a link only logs the browser in. It is not consent to subscribe.
- Links are bearer credentials. Put them only in your reply to the human, never in files, logs, commits, or issues. If one leaks, tell the human not to use it.

**Run it in a PTY.** Closed stdin or EOF cancels registration. Each wait lasts up to 10 minutes, and Ctrl-C ends it. Start it detached and poll every 15-30 s with short commands (or use `expect` or Python `pty`):

```
tmux new-session -d -s exe-signup -x 120 -y 40 'ssh exe.dev'
tmux capture-pane -p -J -t exe-signup
tmux send-keys -t exe-signup '<email>' Enter
```

**Tell the human**

- `Verification email sent to:`: click the link in the email from exe.dev.
- `Verify with Google:` or `Verify with <provider>:`: open that URL.
- `Choose a plan to start creating VMs:`: ask Personal (one person), Work (a team), or Later. Type `personal`, `work`, or `later`.
- `Open this link to activate`: give the URL. It works once and expires in 15 minutes. Personal offers a free 7-day trial by linking a paid ChatGPT subscription (no card), or a Stripe subscription. Work is a Stripe team subscription.
- `Connect a paid ChatGPT subscription or subscribe with Stripe:` in the welcome tips: a Personal link. Hand it over the same way.

**Outcomes**

- `✓ Personal plan active.`, `✓ Work plan active.`, `✓ ChatGPT connected.`: ready. A ChatGPT trial starts with the first VM.
- `Skipped.`, `Skipped in the browser.`, `Timed out waiting for activation.`, `Could not create an activation link.`, or `later`: `new` still fails. See Recovery.

**Continue.** Send `exit`, kill the tmux session if it remains, and rerun the original `ssh exe.dev new --json`.

**Recovery**

- No plan or link expired: run the error's `action.command`. `billing activate --json` returns `{"url":...}`, the same Personal link as the welcome tip. Hand it over the same way, then rerun `ssh exe.dev new --json` once the human confirms. If it returns `{"error":...}` (exit 1), activation is unavailable for this account; `ssh exe.dev billing manage --json` returns a Stripe-only `{"url":...}` instead.
- `Verification still pending.` or `Email verification timed out.`: the email link works for 15 minutes after sending. Once the human clicks it, rerun `ssh exe.dev new --json`. If it expired or never came, rerun the PTY session.
- Your turn ends mid-wait: tell the human the link lasts 15 minutes and to tell you when done. Then rerun `ssh exe.dev new --json`.
- No PTY tool: after the human agrees, `printf '%s\n' "$EMAIL" | ssh exe.dev` sends the email and exits with `Verification still pending.` It skips the plan prompt, so expect the no-plan error.

## A tale of two SSH destinations

- **`ssh exe.dev <command>`** — the exe.dev lobby. A REPL for VM lifecycle, sharing, and configuration. Does not support scp, sftp, or arbitrary shell commands.
- **`ssh <vm>.exe.xyz`** — a direct connection to a VM. Full SSH: shell, scp, sftp, port forwarding, everything.

## Working in non-interactive and sandboxed environments

Coding agents often run SSH in non-interactive shells or sandboxes. Common issues and workarounds:

**scp/sftp failures**: Ensure you're targeting `<vm>.exe.xyz` rather than `exe.dev`. Use ssh-based workarounds.

**Hung connections**: Non-interactive SSH can block on host key prompts with no visible output. Use `-o StrictHostKeyChecking=accept-new` on first connection to a new VM.

**SSH authentication**: Use the user's existing SSH agent or host configuration. If authentication fails, ask the user to configure the appropriate identity for both `exe.dev` and `*.exe.xyz`. Never inspect, copy, or transmit a private key.
