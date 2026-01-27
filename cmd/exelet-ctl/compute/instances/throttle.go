package instances

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/resource/v1"
)

var throttleInstanceCommand = &cli.Command{
	Name:  "throttle",
	Usage: "apply resource throttling to compute instances via cgroup v2 controls",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "cpu",
			Usage: "CPU limit as percentage (e.g., '5%')",
		},
		&cli.StringFlag{
			Name:  "memory",
			Usage: "memory.high threshold as percentage of allocated (e.g., '50%')",
		},
		&cli.StringFlag{
			Name:  "io-read",
			Usage: "IO read bandwidth limit (e.g., '10M', '1G', '500K', or bytes)",
		},
		&cli.StringFlag{
			Name:  "io-write",
			Usage: "IO write bandwidth limit (e.g., '10M', '1G', '500K', or bytes)",
		},
		&cli.BoolFlag{
			Name:  "clear",
			Usage: "remove all throttling",
		},
	},
	ArgsUsage: "[ID...]",
	Action:    throttleAction,
}

func throttleAction(clix *cli.Context) error {
	if clix.NArg() == 0 {
		return fmt.Errorf("at least one instance ID is required")
	}

	c, err := helpers.GetClient(clix)
	if err != nil {
		return err
	}
	defer c.Close()

	// Parse flags
	clear := clix.Bool("clear")

	var cpuPercent *uint32
	if cpuStr := clix.String("cpu"); cpuStr != "" {
		pct, err := parseCPUPercent(cpuStr)
		if err != nil {
			return fmt.Errorf("invalid --cpu value: %w", err)
		}
		cpuPercent = &pct
	}

	var memoryPercent *uint32
	if memStr := clix.String("memory"); memStr != "" {
		pct, err := parseMemoryPercent(memStr)
		if err != nil {
			return fmt.Errorf("invalid --memory value: %w", err)
		}
		memoryPercent = &pct
	}

	var ioReadBps *uint64
	if ioReadStr := clix.String("io-read"); ioReadStr != "" {
		bps, err := parseBandwidth(ioReadStr)
		if err != nil {
			return fmt.Errorf("invalid --io-read value: %w", err)
		}
		ioReadBps = &bps
	}

	var ioWriteBps *uint64
	if ioWriteStr := clix.String("io-write"); ioWriteStr != "" {
		bps, err := parseBandwidth(ioWriteStr)
		if err != nil {
			return fmt.Errorf("invalid --io-write value: %w", err)
		}
		ioWriteBps = &bps
	}

	// Validate that at least one option is specified
	if !clear && cpuPercent == nil && memoryPercent == nil && ioReadBps == nil && ioWriteBps == nil {
		return fmt.Errorf("at least one throttle option (--cpu, --memory, --io-read, --io-write) or --clear is required")
	}

	ctx := context.WithoutCancel(clix.Context)
	wg := &sync.WaitGroup{}
	var failCount atomic.Int32

	for _, id := range clix.Args().Slice() {
		wg.Add(1)

		go func(id string, wg *sync.WaitGroup) {
			defer wg.Done()

			req := &api.ThrottleVMRequest{
				VmID:          id,
				Clear:         clear,
				CpuPercent:    cpuPercent,
				MemoryPercent: memoryPercent,
				IoReadBps:     ioReadBps,
				IoWriteBps:    ioWriteBps,
			}

			if _, err := c.ThrottleVM(ctx, req); err != nil {
				fmt.Printf("ERR: error throttling %s: %s\n", id, err)
				failCount.Add(1)
				return
			}

			if clear {
				fmt.Printf("%s throttle cleared\n", id)
			} else {
				fmt.Printf("%s throttle applied\n", id)
			}
		}(id, wg)
	}

	wg.Wait()

	if n := failCount.Load(); n > 0 {
		return fmt.Errorf("failed to throttle %d instance(s)", n)
	}

	return nil
}

// parseCPUPercent parses a CPU percentage string like "5%" or "200".
// Values above 100% are allowed (e.g., 200% = 2 cores worth of CPU time).
func parseCPUPercent(s string) (uint32, error) {
	s = strings.TrimSuffix(s, "%")
	pct, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid percentage: %s", s)
	}
	if pct == 0 {
		return 0, fmt.Errorf("CPU percentage must be greater than 0")
	}
	return uint32(pct), nil
}

// parseMemoryPercent parses a memory percentage string like "50%" or "80".
// Values must be between 1 and 100.
func parseMemoryPercent(s string) (uint32, error) {
	s = strings.TrimSuffix(s, "%")
	pct, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid percentage: %s", s)
	}
	if pct == 0 || pct > 100 {
		return 0, fmt.Errorf("memory percentage must be between 1 and 100, got %d", pct)
	}
	return uint32(pct), nil
}

// parseBandwidth parses a bandwidth string like "10M", "1G", "500K", or plain bytes.
// Supported suffixes: K/k (1024), M/m (1024^2), G/g (1024^3).
func parseBandwidth(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty bandwidth value")
	}

	var multiplier uint64 = 1
	lastChar := s[len(s)-1]

	switch lastChar {
	case 'k', 'K':
		multiplier = 1024
		s = s[:len(s)-1]
	case 'm', 'M':
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case 'g', 'G':
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}

	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid bandwidth value: %s", s)
	}

	// Check for overflow before multiplication
	const maxUint64 = ^uint64(0)
	if val > maxUint64/multiplier {
		return 0, fmt.Errorf("bandwidth value too large: %s (would overflow)", s)
	}

	return val * multiplier, nil
}
