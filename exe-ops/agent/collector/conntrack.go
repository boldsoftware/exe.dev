package collector

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// Conntrack collects connection tracking table usage from
// /proc/sys/net/netfilter/nf_conntrack_count and nf_conntrack_max.
// Fields are nil if conntrack is not loaded.
type Conntrack struct {
	Count *int64
	Max   *int64

	countPath string
	maxPath   string
}

func NewConntrack() *Conntrack {
	return &Conntrack{
		countPath: "/proc/sys/net/netfilter/nf_conntrack_count",
		maxPath:   "/proc/sys/net/netfilter/nf_conntrack_max",
	}
}

func (c *Conntrack) Name() string { return "conntrack" }

func (c *Conntrack) Collect(_ context.Context) error {
	c.Count = nil
	c.Max = nil

	count, err := readIntFile(c.countPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // conntrack not loaded
		}
		return err
	}
	c.Count = &count

	max, err := readIntFile(c.maxPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	c.Max = &max

	return nil
}

func readIntFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}
