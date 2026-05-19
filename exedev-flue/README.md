# exedev-flue

The official exe.dev sandbox connector for [Flue](https://flue.dev).

This package contains the TypeScript connector that lets Flue agents use an
exe.dev VM as their sandbox, plus the small Go HTTP handler that serves the
install prompt and connector source from exe.dev. It is maintained here for the
[Flue connectors](https://github.com/withastro/flue/tree/main/connectors)
ecosystem.

## Install

Use the official Flue connector docs at
[github.com/withastro/flue/tree/main/connectors](https://github.com/withastro/flue/tree/main/connectors)
when adding exe.dev to a Flue project.

For agent-assisted setup, pipe the exe.dev install prompt into a coding agent
from a Flue project:

```bash
curl -fsSL https://exe.dev/flue/exedev.md | claude
```

The prompt tells the agent how to fetch `exedev.ts`, place it in the project,
install dependencies, and wire it into a `FlueContext`.

## Files

| File                  | Purpose                                                                    |
| --------------------- | -------------------------------------------------------------------------- |
| `exedev.ts`           | The Flue sandbox connector source that user projects download.             |
| `exedev.md`           | The agent-readable install prompt served from `/flue/exedev.md`.           |
| `serve.go`            | Go handler for `/flue/exedev.md` and `/flue/exedev.ts`.                    |
| `serve_test.go`       | Handler tests for content type, `{{BASE_URL}}` substitution, and 404s.     |
| `tests/unit.test.ts`  | Local TypeScript unit tests for connector helpers.                         |
| `tests/smoke.test.ts` | End-to-end smoke test against a real exe.dev VM when `EXE_VM_HOST` is set. |
| `bin/flue-init`       | Temporary helper to bootstrap a Flue project until `flue init` exists.     |

## How serving works

`exedev.md` references the sibling connector source as
`{{BASE_URL}}/exedev.ts`. `serve.go` replaces `{{BASE_URL}}` at request time
with the request scheme, host, and `/flue` prefix, so the same markdown works
in local development, staging, and production.

The private exe.dev server imports this package and mounts `Handle` under
`/flue/`. The public install URL remains:

```text
https://exe.dev/flue/exedev.md
```

## Development

```bash
pnpm install
pnpm typecheck
pnpm format
go test ./...
```

`pnpm test` runs the TypeScript unit tests and then the smoke test. The smoke
test exits without doing network work unless `EXE_VM_HOST` is set:

```bash
EXE_VM_HOST=terminus.exe.xyz pnpm test
```

The connector is shipped as TypeScript source. Keep dependencies small and
portable; user projects install them when they install the connector.

## Public mirror workflow

This directory is published in the public `github.com/boldsoftware/exe.dev`
repo as `exedev-flue/`. In the private monorepo it lives under `oss/`, which is
the subtree mirrored to that public repo by the merge queue.

Changes can flow both ways:

- Private changes under `oss/exedev-flue/` are pushed to the public repo by the
  `oss` subrepo sync.
- Public commits to `github.com/boldsoftware/exe.dev` are mirrored back into the
  private repo's `oss/` subtree by the scheduled sync workflow.

Commit messages for changes in this directory become public with the mirrored
commits, so write them as public-facing messages.
