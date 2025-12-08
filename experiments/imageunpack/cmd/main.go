package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/dustin/go-humanize"
	cli "github.com/urfave/cli/v2"

	"exe.dev/experiments/imageunpack"
	"exe.dev/version"
)

func main() {
	app := cli.NewApp()
	app.Name = "imageunpack"
	app.Version = version.BuildVersion()
	app.Usage = "Fast parallel unpacking of OCI container images"
	app.Authors = []*cli.Author{
		{Name: "exe.dev"},
	}

	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:    "debug",
			Aliases: []string{"D"},
			Usage:   "enable debug logging",
			EnvVars: []string{"IMAGEUNPACK_DEBUG"},
		},
		&cli.IntFlag{
			Name:    "concurrency",
			Aliases: []string{"c"},
			Usage:   "number of parallel network transfers",
			Value:   10,
			EnvVars: []string{"IMAGEUNPACK_CONCURRENCY"},
		},
		&cli.StringFlag{
			Name:    "chunk-size",
			Aliases: []string{"s"},
			Usage:   "size of each download chunk (e.g., 8MB, 16MB)",
			Value:   "8MB",
			EnvVars: []string{"IMAGEUNPACK_CHUNK_SIZE"},
		},
		&cli.StringFlag{
			Name:    "platform",
			Aliases: []string{"p"},
			Usage:   "platform to pull (e.g., linux/amd64)",
			Value:   imageunpack.Platform(),
			EnvVars: []string{"IMAGEUNPACK_PLATFORM"},
		},
		&cli.StringFlag{
			Name:    "username",
			Aliases: []string{"u"},
			Usage:   "registry username",
			EnvVars: []string{"IMAGEUNPACK_USERNAME", "DOCKER_USERNAME"},
		},
		&cli.StringFlag{
			Name:    "password",
			Usage:   "registry password",
			EnvVars: []string{"IMAGEUNPACK_PASSWORD", "DOCKER_PASSWORD"},
		},
		&cli.BoolFlag{
			Name:    "insecure",
			Usage:   "allow insecure TLS connections",
			EnvVars: []string{"IMAGEUNPACK_INSECURE"},
		},
		&cli.BoolFlag{
			Name:    "http",
			Usage:   "use HTTP instead of HTTPS",
			EnvVars: []string{"IMAGEUNPACK_HTTP"},
		},
		&cli.BoolFlag{
			Name:    "no-same-owner",
			Usage:   "don't set file ownership (required for non-root)",
			EnvVars: []string{"IMAGEUNPACK_NO_SAME_OWNER"},
		},
	}

	app.ArgsUsage = "<image> <destination>"

	app.Action = func(c *cli.Context) error {
		if c.NArg() < 2 {
			return fmt.Errorf("usage: imageunpack <image> <destination>")
		}

		imageRef := c.Args().Get(0)
		destDir := c.Args().Get(1)

		// Setup logging
		opts := &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}
		if c.Bool("debug") {
			opts.Level = slog.LevelDebug
		}
		log := slog.New(slog.NewTextHandler(os.Stderr, opts))

		// Parse chunk size
		chunkSize, err := humanize.ParseBytes(c.String("chunk-size"))
		if err != nil {
			return fmt.Errorf("invalid chunk size: %w", err)
		}

		// Create config
		cfg := &imageunpack.Config{
			Concurrency: c.Int("concurrency"),
			ChunkSize:   int64(chunkSize),
			Username:    c.String("username"),
			Password:    c.String("password"),
			Insecure:    c.Bool("insecure"),
			UseHTTP:     c.Bool("http"),
			NoSameOwner: c.Bool("no-same-owner"),
		}

		// Create destination directory
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return fmt.Errorf("create destination directory: %w", err)
		}

		// Create unpacker
		unpacker := imageunpack.NewUnpacker(cfg, log)

		// Run unpack
		start := time.Now()
		platform := c.String("platform")

		log.Info("starting unpack",
			"image", imageRef,
			"platform", platform,
			"destination", destDir,
			"concurrency", cfg.Concurrency,
			"chunkSize", humanize.Bytes(uint64(cfg.ChunkSize)))

		result, err := unpacker.UnpackWithPlatform(c.Context, imageRef, platform, destDir)
		if err != nil {
			return fmt.Errorf("unpack failed: %w", err)
		}

		elapsed := time.Since(start)

		// Print results
		fmt.Printf("Digest: %s\n", result.Digest)
		fmt.Printf("Layers: %d\n", result.LayerCount)
		fmt.Printf("Compressed size: %s\n", humanize.Bytes(uint64(result.CompressedSize)))
		fmt.Printf("Unpacked size: %s\n", humanize.Bytes(uint64(result.UnpackedSize)))
		fmt.Printf("Time: %s\n", elapsed.Round(time.Millisecond))

		if elapsed.Seconds() > 0 {
			speed := float64(result.CompressedSize) / elapsed.Seconds()
			fmt.Printf("Download speed: %s/s\n", humanize.Bytes(uint64(speed)))
		}

		return nil
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
