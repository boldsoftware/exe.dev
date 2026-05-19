# Package Purpose

The exe.dev Flue connector and the Go handler that serves it. `exedev.ts` and
`exedev.md` are the public artifacts users `curl` from `https://exe.dev/flue/`
when installing the connector into a Flue project. `serve.go` embeds them and
serves them under `/flue/` from `exed` (mounted in `execore/exe-web.go`).

See `README.md` for the install flow and layout.

# General Rules

## The .ts and .md are public artifacts

`exedev.ts` and `exedev.md` are downloaded by user coding agents (claude,
codex, opencode, ...) at install time. Treat them like a public API:

- The install doc tells the agent **not to modify** `exedev.ts` after fetching
  it. If you change the public surface (option name, default, behavior), bump
  the doc to match — the two are versioned together via `//go:embed`.
- `exedev.md` must stay agent-readable: short, prescriptive, no ambiguity
  ("ask the user only on genuine ambiguity"). Don't add prose for humans;
  put that in `README.md`.
- `exedev.md` references sibling assets via `{{BASE_URL}}/exedev.ts`. The
  placeholder is substituted at request time in `serve.go`. Don't hardcode
  `https://exe.dev` — it breaks local dev and any future canonical host.

## Keep options docs in sync

Every option exported from `ExeDevConnectorOptions` in `exedev.ts` should
appear in the "All connector options" table in `exedev.md`. When you add or
rename an option, update both files in the same commit.

## The connector has no build step

`exedev.ts` is shipped as source. The user's project compiles it via their
own tsconfig. Don't introduce non-portable TypeScript (decorators, project
references, path aliases) or non-Node imports. Stick to what works under a
plain `tsc --noEmit` against `ES2022` / `ESNext` modules.

Dependencies are limited to `@flue/sdk` and `ssh2`. Don't add more without
weighing the cost — every dep is one the user has to install.

## Tests

- `go test ./...` — serve handler (content type, `{{BASE_URL}}`, 404 paths).
  Always runs.
- `pnpm typecheck` — `tsc --noEmit` on `exedev.ts`. Always runs.
- `pnpm test` — smoke test against a real exe.dev VM. Skipped unless
  `EXE_VM_HOST` is set. Run locally before changing connector behavior:
  ```bash
  EXE_VM_HOST=terminus.exe.xyz pnpm test
  ```

The smoke test exercises every `SandboxApi` method end-to-end. If you change
`exedev.ts`, run it. CI cannot.

## Errors

User-facing errors from the connector (`ExeDevError`) should tell the user
**how to fix the problem**, not just what went wrong. Look at the existing
"Couldn't find an SSH private key" and "`createVm` needs an `apiToken`"
messages for the pattern: what failed → what to try.

## exe.dev HTTPS API response contract

`new` and `cp` return JSON with `vm_name`, `ssh_dest`, `ssh_port`, plus
URL fields. Parse via `parseVmResponse` and prefer `ssh_dest` over
re-deriving `${name}.exe.xyz` — the API is authoritative for hostname
mapping and we don't want to drift if it ever changes.

Auto-created/cloned VMs go through `sshConnectWithRetry` because DNS and
sshd take a few seconds after the API returns. If you add another path
that connects to a freshly-provisioned VM, use the retry wrapper.

## SFTP is lazy

`ExeDevSandboxApi` does not open SFTP at construction time — it opens on
first file op and caches the wrapper. If you add a new SandboxApi method
that needs SFTP, call `await this.getSftp()` at the top.

Two reasons this matters:

- Shell-only flows (the most common usage) never open SFTP, so they can't
  trip server-side idle-channel termination ("Received unexpected SFTP
  session termination").
- The cached wrapper has `error`/`close`/`end` listeners that drop the
  cache, so an unexpected termination is swallowed and the next file op
  re-opens cleanly.

Don't bypass `getSftp` and store an SFTP reference yourself — you'll lose
the recovery behavior.
