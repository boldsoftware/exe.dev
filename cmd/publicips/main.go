// The publicips tool is a small helper to print the public IPs of an EC2 VM.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"exe.dev/publicips"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	domain := flag.String("domain", "exe.xyz", "Box domain hosting s### records (e.g. exe.xyz)")
	flag.Parse()

	boxDomain := strings.TrimSpace(*domain)
	if boxDomain == "" {
		return fmt.Errorf("-domain must not be empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mapping, err := publicips.EC2IPs(ctx, boxDomain)
	if err != nil {
		return err
	}

	privateAddrs := make([]netip.Addr, 0, len(mapping))
	for addr := range mapping {
		privateAddrs = append(privateAddrs, addr)
	}
	slices.SortFunc(privateAddrs, func(a, b netip.Addr) int {
		switch {
		case a == b:
			return 0
		case a.Less(b):
			return -1
		default:
			return 1
		}
	})

	for _, privateAddr := range privateAddrs {
		info := mapping[privateAddr]
		fmt.Printf("%s -> %s (%s)\n", privateAddr, info.IP, info.Domain)
	}

	return nil
}
