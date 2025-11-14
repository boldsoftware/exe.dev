# slogcontext linter

A Go static analysis tool that detects usage of `slog` logging functions without context when a `ctx` variable is in scope.

## Problem

When using Go's `log/slog` package, it's important to use the Context-aware variants (like `slog.InfoContext`) when a `context.Context` is available. This ensures proper trace propagation and structured logging context.

## What it detects

The linter finds calls to these `slog` functions when a `ctx` variable is in scope:

- `slog.Debug` → should be `slog.DebugContext`
- `slog.Info` → should be `slog.InfoContext`
- `slog.Warn` → should be `slog.WarnContext`
- `slog.Error` → should be `slog.ErrorContext`
- `slog.Log` → should be `slog.LogContext`

## Installation

Build the linter:

```bash
cd tracing/linter/cmd/slogcontext
go build
```

Or install it:

```bash
go install exe.dev/tracing/linter/cmd/slogcontext@latest
```

## Usage

### Quick start

Run with `go run` to check and fix issues in a package:

```bash
go run exe.dev/tracing/linter/cmd/slogcontext -fix ./packagename/...
```

### Check files for issues

```bash
# Check a single file
./slogcontext path/to/file.go

# Check a package
./slogcontext ./...

# Check specific packages
./slogcontext ./pkg/...
```

### Automatically fix issues

The linter can automatically fix detected issues:

```bash
# Fix a single file
./slogcontext -fix path/to/file.go

# Fix all files in a package
./slogcontext -fix ./...
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

## Integration with golangci-lint

To use this linter with golangci-lint, you can configure it as a custom linter in `.golangci.yml`:

```yaml
linters-settings:
  custom:
    slogcontext:
      path: ./tracing/linter/cmd/slogcontext/slogcontext
      description: Checks that slog calls use Context variants when ctx is in scope
      original-url: exe.dev/tracing/linter
```

## Testing

Run the linter tests:

```bash
cd tracing/linter
go test -v
```

## How it works

The linter uses Go's `go/analysis` framework to:

1. Find all calls to `slog` functions
2. Check if a variable named `ctx` of type `context.Context` is in scope
3. Suggest using the Context variant with the ctx parameter
4. Optionally apply the fix automatically with `-fix`

The suggested fixes properly insert the `ctx` parameter as the first argument to the Context variant of the function.
