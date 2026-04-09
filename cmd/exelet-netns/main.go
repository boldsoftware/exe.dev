//go:build linux

// exelet-netns prints diagnostic info for netns-managed VM network stacks.
//
// Usage:
//
//	exelet-netns <instance-id>          # diagnose one instance by full ID
//	exelet-netns <vmid>                  # diagnose by vmid (e.g. vm000003)
//	exelet-netns --all                   # diagnose all exe-* namespaces
//	exelet-netns --live <instance-id>    # stream live connections
//	exelet-netns --live <vmid>           # stream live connections by vmid
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"exe.dev/exelet/network/netns"
)

func main() {
	if err := run(); err != nil {
		if err != context.Canceled {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: exelet-netns <instance-id|vmid>\n")
	fmt.Fprintf(os.Stderr, "       exelet-netns --all\n")
	fmt.Fprintf(os.Stderr, "       exelet-netns --live <instance-id|vmid>\n")
	os.Exit(1)
}

// isVMID returns true if s looks like a bare vmid (e.g. "vm000003").
func isVMID(s string) bool {
	return strings.HasPrefix(s, "vm") && !strings.Contains(s, "-")
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if len(os.Args) < 2 {
		usage()
	}

	switch {
	case os.Args[1] == "--all":
		results, err := netns.DiagnoseAll(ctx)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Println("no exe-* network namespaces found")
			return nil
		}
		for i, d := range results {
			if i > 0 {
				fmt.Println(strings.Repeat("\u2501", 60))
			}
			netns.FormatDiag(os.Stdout, d)
		}

	case os.Args[1] == "--live":
		if len(os.Args) < 3 {
			return fmt.Errorf("--live requires an instance ID or vmid")
		}
		arg := os.Args[2]
		if isVMID(arg) {
			return netns.LiveStreamByVMID(ctx, os.Stdout, arg)
		}
		return netns.LiveStream(ctx, os.Stdout, arg)

	default:
		arg := os.Args[1]
		if isVMID(arg) {
			d := netns.DiagnoseByVMID(ctx, arg)
			netns.FormatDiag(os.Stdout, d)
		} else {
			d, err := netns.Diagnose(ctx, arg)
			if err != nil {
				return err
			}
			netns.FormatDiag(os.Stdout, d)
		}
	}

	return nil
}
