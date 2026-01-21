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

	// Validate that at least one option is specified
	if !clear && cpuPercent == nil && memoryPercent == nil {
		return fmt.Errorf("at least one throttle option (--cpu, --memory) or --clear is required")
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
