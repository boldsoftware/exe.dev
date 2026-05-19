---
{ "category": "sandbox", "website": "https://exe.dev", "aliases": ["ssh2"] }
---

# Add a Flue Connector: exe.dev

You are an AI coding agent installing the exe.dev sandbox connector for a
Flue project. Follow the steps. Confirm with the user only on genuine
ambiguity.

## What this connector does

Wraps an exe.dev VM into Flue's `SandboxFactory` over:

- **SSH + SFTP** to `<vmname>.exe.xyz` for shell + file I/O
- **HTTPS** (`POST https://exe.dev/exec`) for VM lifecycle (`new`, `cp`, `rm`)

VMs are persistent. `cleanup: true` only closes SSH and deletes
auto-created VMs; it never touches VMs passed via `host`.

For `createVm` and `cloneFrom`, the connector waits up to 90s after the
HTTPS API returns for DNS to propagate and sshd to come up — no manual
sleep needed in your agent code.

## Choose a mode

Ask the user which one. Default to **Existing VM**.

| Mode            | Option             | Use when                                               |
| --------------- | ------------------ | ------------------------------------------------------ |
| **Existing VM** | `host: '...'`      | Long-running assistant, dev/build agent. **Default.**  |
| **Cloned VM**   | `cloneFrom: '...'` | Ephemeral per-run isolation off a pre-configured base. |
| **Fresh VM**    | `createVm: true`   | Clean slate every time. Rarely the right choice.       |

## Where to write the file

- `.flue/` layout → `./.flue/connectors/exedev.ts`
- Root layout → `./connectors/exedev.ts`

Create parent dirs as needed.

## Fetch the connector source

```bash
curl -fsSL {{BASE_URL}}/exedev.ts > <PATH-FROM-PREVIOUS-STEP>
```

Don't modify the file.

## Install dependencies

```bash
npm install ssh2
npm install -D @types/ssh2
```

Use the user's package manager (`pnpm`, `yarn`, etc.) per their lockfile.

## Authentication

**SSH (always required):** key-based auth or SSH agent. Auto-detected in order:

1. `privateKey` option (raw PEM)
2. `agent` option (socket path)
3. `privateKeyPath` option (file path)
4. `$EXE_SSH_KEY` env var (file path)
5. `~/.ssh/id_ed25519`
6. `~/.ssh/id_rsa`
7. `$SSH_AUTH_SOCK` env var (last-resort agent fallback)

Step 7 covers 1Password / Yubikey users whose private keys never touch
disk — set nothing and the connector picks up the running agent.

Same keys you registered when first running `ssh exe.dev`.

**HTTPS API token (only for `createVm` / `cloneFrom`):** generate with:

```bash
ssh-keygen -t ed25519 -C api -f ~/.ssh/exe_dev_api
cat ~/.ssh/exe_dev_api.pub | ssh exe.dev ssh-key add

b64url() { tr -d '\n=' | tr '+/' '-_'; }
PERMISSIONS='{"cmds":["new","rm","cp","ls","whoami"]}'
PAYLOAD=$(printf '%s' "$PERMISSIONS" | base64 | b64url)
SIG=$(printf '%s' "$PERMISSIONS" | ssh-keygen -Y sign -f ~/.ssh/exe_dev_api -n v0@exe.dev)
SIGBLOB=$(echo "$SIG" | sed '1d;$d' | b64url)
TOKEN="exe0.$PAYLOAD.$SIGBLOB"

curl -X POST https://exe.dev/exec -H "Authorization: Bearer $TOKEN" -d 'whoami'
```

Default token `cmds` includes `new` but not `rm`/`cp` — override as
above when you need lifecycle management.

Never invent a token value. It must come from the user.

## Wiring

### Existing VM

```ts
import type { FlueContext } from "@flue/sdk/client";
import { exedev } from "../connectors/exedev";

export const triggers = { webhook: true };

export default async function ({ init, env }: FlueContext) {
  const agent = await init({
    sandbox: exedev({ host: env.EXE_VM_HOST, cleanup: true }),
    model: "anthropic/claude-sonnet-4-6",
  });
  return await (await agent.session()).shell("uname -a");
}
```

### Fresh VM

```ts
const agent = await init({
  sandbox: exedev({
    apiToken: env.EXE_API_TOKEN,
    createVm: true,
    cleanup: true,
  }),
  model: "anthropic/claude-sonnet-4-6",
});
```

### Cloned VM

```ts
const agent = await init({
  sandbox: exedev({
    apiToken: env.EXE_API_TOKEN,
    cloneFrom: "my-dev-vm",
    cleanup: true,
  }),
  model: "anthropic/claude-sonnet-4-6",
});
```

## All connector options

| Option           | Type                             | Default  | Notes                                                                      |
| ---------------- | -------------------------------- | -------- | -------------------------------------------------------------------------- |
| `host`           | `string`                         | —        | VM hostname. Required unless `createVm` or `cloneFrom` is set.             |
| `username`       | `string`                         | `"user"` | SSH username on the VM (exeuntu default).                                  |
| `port`           | `number`                         | `22`     | SSH port.                                                                  |
| `privateKey`     | `string \| Buffer`               | —        | Raw PEM. Highest precedence in the SSH-key resolution chain.               |
| `privateKeyPath` | `string`                         | —        | Path to a private-key file. Beats `$EXE_SSH_KEY` and `~/.ssh/*`.           |
| `agent`          | `string`                         | —        | SSH agent socket path. Beats file lookups; falls back to `$SSH_AUTH_SOCK`. |
| `apiToken`       | `string`                         | —        | exe.dev HTTPS bearer token. Required for `createVm` / `cloneFrom`.         |
| `createVm`       | `boolean`                        | `false`  | Create a fresh VM via `new`. Needs `apiToken`.                             |
| `vmName`         | `string`                         | random   | VM name when `createVm: true`. Omit to let exe.dev generate one.           |
| `cloneFrom`      | `string`                         | —        | Clone a VM via `cp <name>`. Needs `apiToken`.                              |
| `cleanup`        | `boolean \| () => Promise<void>` | `false`  | See cleanup behavior below.                                                |

### Cleanup behavior

- `false` (default) — no cleanup. exe.dev VMs are persistent; user manages lifecycle via `ssh exe.dev` → `rm`.
- `true` — closes SSH. Also `rm`s the VM when it was auto-created via `createVm` / `cloneFrom`. Never deletes a VM passed via `host`.
- function — runs the user function, then closes SSH (and deletes auto-created VMs).

## Environment variables

| Variable        | Required                     | Description                                                                                      |
| --------------- | ---------------------------- | ------------------------------------------------------------------------------------------------ |
| `EXE_VM_HOST`   | for existing VM              | e.g. `maple-dune.exe.xyz`. Convention only; passed by user.                                      |
| `EXE_API_TOKEN` | for `createVm` / `cloneFrom` | bearer token (`exe0.*` / `exe1.*`). Convention only.                                             |
| `EXE_SSH_KEY`   | optional                     | Path to SSH private key. Used as a fallback by the connector.                                    |
| `SSH_AUTH_SOCK` | optional                     | SSH agent socket. Picked up automatically when no key file resolves (1Password, ssh-agent, ...). |

Place vars per project conventions (`.env`, `.dev.vars`, `AGENTS.md`).
Ask if unclear.

## Verify

1. `npx tsc --noEmit` — no type errors
2. `ssh user@<vm-host> echo hello` — SSH works
3. Tell the user: deps installed, env set, run `flue dev` or `flue run <agent>`
