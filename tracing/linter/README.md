# slogcontext linter

A Go static analysis tool that detects usage of `slog` logging functions without context when a `ctx` variable is in scope.

To implement poor man's tracing, we shove a trace_id in the context, and we want
it logged wherever there's logging. This means we have to use `slog.WarnContext()` instead
of `slog.Warn()`. So this was born.

VIBE CODE WARNING: this is a checked in sed-script. Caveat emptor.


## Installation

Build the linter:

```bash
cd tracing/linter/cmd/slogcontext
go build
```

## Usage

### Use with go vet

```bash
# Build the linter
go build -o /tmp/slogcontext ./tracing/linter/cmd/slogcontext

# Run with go vet
go vet -vettool=/tmp/slogcontext ./exelet/... ./execore/... ./sshpool/... ./sshpool2/...
```

### Direct invocation

To automatically fix issues, run the linter directly:

```bash
# Build the linter
go build -o /tmp/slogcontext ./tracing/linter/cmd/slogcontext

# Fix issues in packages
GOOS=linux /tmp/slogcontext -fix ./exelet/... ./execore/... ./sshpool/... ./sshpool2/...
```

**Important:** When running on macOS/darwin and analyzing Linux-specific code (files with `//go:build linux`), you must set `GOOS=linux` to ensure the linter analyzes all files. The `go vet` approach handles this automatically when run on Linux (e.g., in CI).

### Quick start (single platform)

For checking code on your current platform:

```bash
go run exe.dev/tracing/linter/cmd/slogcontext ./packagename/...
```

For fixing code on your current platform:

```bash
go run exe.dev/tracing/linter/cmd/slogcontext -fix ./packagename/...
```

### Example

Before:
```go
func handleRequest(ctx context.Context, req *Request) error {
    slog.Info("handling request", "id", req.ID)
    // ...
}
```

After running `slogcontext -fix`:
```go
func handleRequest(ctx context.Context, req *Request) error {
    slog.InfoContext(ctx, "handling request", "id", req.ID)
    // ...
}
```

## How it works

The linter uses Go's `go/analysis` framework to:

1. Find all calls to `slog` functions
2. Check if a variable named `ctx` of type `context.Context` is in scope
3. Suggest using the Context variant with the ctx parameter
4. Optionally apply the fix automatically with `-fix`

The suggested fixes properly insert the `ctx` parameter as the first argument to the Context variant of the function.
