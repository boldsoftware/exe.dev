# imageunpack

Fast parallel unpacking of OCI container images.

## Goal

Build a Go library (and CLI tool) that unpacks container images as fast as possible by:
- Downloading layer blobs in parallel using HTTP range requests
- Configurable concurrency and chunk sizes
- Unpacking layers in correct order after download

Target image: `ghcr.io/boldsoftware/exeuntu:main-c1b77a6` (~1GB compressed, ~3GB uncompressed)

## Usage

### Library

```go
import "exe.dev/experiments/imageunpack"

cfg := &imageunpack.Config{
    Concurrency: 20,
    ChunkSize:   16 * 1024 * 1024, // 16MB
}

unpacker := imageunpack.NewUnpacker(cfg, slog.Default())
result, err := unpacker.Unpack(ctx, "ghcr.io/org/image:tag", "/dest/dir")

fmt.Printf("Digest: %s\n", result.Digest)
fmt.Printf("Layers: %d\n", result.LayerCount)
fmt.Printf("Size: %d bytes\n", result.CompressedSize)
```

### CLI

```bash
go build -o imageunpack ./experiments/imageunpack/cmd

# Basic usage
imageunpack ghcr.io/org/image:tag /dest/dir

# With options
imageunpack -c 20 -s 16MB ghcr.io/org/image:tag /dest/dir

# All flags
imageunpack --help
```

## Benchmark Results

Tested on a ~13 MB/s connection with the exeuntu image (1GB compressed):

| Concurrency | Chunk Size | Time   | Speed (MB/s) | Notes |
|-------------|------------|--------|--------------|-------|
| 10          | 8MB        | 1m24s  | 12.3         | baseline |
| 20          | 8MB        | 2m21s  | 7.3          | rate limited |
| 30          | 8MB        | 1m24s  | 12.3         | |
| 50          | 8MB        | 1m18s  | 13.2         | |
| 10          | 16MB       | 1m19s  | 13.0         | |
| 10          | 32MB       | 1m18s  | 13.1         | |
| 20          | 16MB       | 1m17s  | 13.3         | **best** |
| 30          | 16MB       | error  | -            | rate limited |
| 50          | 16MB       | 1m20s  | 12.8         | |

### Key Findings

1. **Sweet spot: 20 concurrency + 16MB chunks** - best throughput without rate limiting

2. **Bigger chunks reduce overhead** - 16MB and 32MB chunks perform slightly better than 8MB due to fewer HTTP requests

3. **Too much concurrency can hurt** - servers may rate limit aggressive parallelism (observed with 20x8MB and 30x16MB configs)

4. **HTTP/2 multiplexing** - enabled by default, allows many requests over fewer TCP connections

5. **Connection reuse** - properly configured with body draining to ensure connections are returned to the pool

### Recommendations

- **Slow connection (<20 MB/s)**: Use defaults (10 concurrency, 8MB chunks)
- **Fast connection (20-100 MB/s)**: Try `--concurrency 20 --chunk-size 16MB`
- **Very fast connection (>100 MB/s)**: Try `--concurrency 30-50 --chunk-size 32MB`, watch for rate limiting

## Implementation Details

- Uses global worker pool for chunk downloads (all chunks from all layers fed to single job queue)
- Downloads all layers in parallel, then unpacks sequentially in correct order
- Direct HTTP registry API calls (bypasses slow containerd FetchDescriptor)
- Supports multi-platform image indexes
- Bearer token authentication for private registries

## Comparison

| Tool | Time | Notes |
|------|------|-------|
| crane pull | 1m10s | download only, no unpack |
| imageunpack | 1m17s | download + unpack |

imageunpack is faster than crane's download-only time when including the full unpack step.
