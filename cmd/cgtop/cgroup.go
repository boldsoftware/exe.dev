//go:build linux

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CgroupNode represents a cgroup directory in the tree.
type CgroupNode struct {
	Name     string             `json:"name"`
	Path     string             `json:"path"`
	Stats    map[string]float64 `json:"stats"`
	Rates    map[string]float64 `json:"rates,omitempty"`
	Config   map[string]any     `json:"config"`
	Children []*CgroupNode      `json:"children"`
}

// cumulativeStats are monotonically increasing counters for which we compute per-second rates.
var cumulativeStats = map[string]bool{
	"cpu.usage_usec":               true,
	"cpu.user_usec":                true,
	"cpu.system_usec":              true,
	"cpu.nr_periods":               true,
	"cpu.nr_throttled":             true,
	"cpu.throttled_usec":           true,
	"cpu.local.usage_usec":         true,
	"cpu.local.user_usec":          true,
	"cpu.local.system_usec":        true,
	"io.rbytes":                    true,
	"io.wbytes":                    true,
	"io.rios":                      true,
	"io.wios":                      true,
	"io.dbytes":                    true,
	"io.dios":                      true,
	"memory.events.low":            true,
	"memory.events.high":           true,
	"memory.events.max":            true,
	"memory.events.oom":            true,
	"memory.events.oom_kill":       true,
	"memory.events.oom_group_kill": true,
	"memory.events.local.low":      true,
	"memory.events.local.high":     true,
	"memory.events.local.max":      true,
	"memory.events.local.oom":      true,
	"memory.events.local.oom_kill": true,
	"memory.swap.events.high":      true,
	"memory.swap.events.max":       true,
	"memory.swap.events.fail":      true,
	"memory.stat.pgfault":          true,
	"memory.stat.pgmajfault":       true,
}

func walkCgroupTree(fsRoot, relPath string) (*CgroupNode, error) {
	absPath := filepath.Join(fsRoot, relPath)
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, err
	}

	name := filepath.Base(relPath)
	if relPath == "" || relPath == "." {
		name = "/"
	}

	node := &CgroupNode{
		Name:   name,
		Path:   relPath,
		Stats:  make(map[string]float64),
		Config: make(map[string]any),
	}

	readAllStats(absPath, node)
	readAllConfig(absPath, node)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child, err := walkCgroupTree(fsRoot, filepath.Join(relPath, e.Name()))
		if err != nil {
			continue
		}
		node.Children = append(node.Children, child)
	}

	return node, nil
}

func readAllStats(dir string, node *CgroupNode) {
	// cpu.stat
	if m, err := readKeyValueFile(filepath.Join(dir, "cpu.stat")); err == nil {
		for key, val := range m {
			node.Stats["cpu."+key] = float64(val)
		}
	}

	// cpu.stat.local
	if m, err := readKeyValueFile(filepath.Join(dir, "cpu.stat.local")); err == nil {
		for key, val := range m {
			node.Stats["cpu.local."+key] = float64(val)
		}
	}

	// memory single-value gauges
	for _, name := range []string{
		"memory.current", "memory.peak",
		"memory.swap.current", "memory.swap.peak",
		"memory.zswap.current",
	} {
		if v, err := readSingleUint(filepath.Join(dir, name)); err == nil {
			node.Stats[name] = float64(v)
		}
	}

	// memory.stat (selected keys)
	if m, err := readKeyValueFile(filepath.Join(dir, "memory.stat")); err == nil {
		for _, key := range []string{
			"anon", "file", "kernel", "shmem", "slab", "sock",
			"pgfault", "pgmajfault",
		} {
			if v, ok := m[key]; ok {
				node.Stats["memory.stat."+key] = float64(v)
			}
		}
	}

	// memory.events
	if m, err := readKeyValueFile(filepath.Join(dir, "memory.events")); err == nil {
		for key, val := range m {
			node.Stats["memory.events."+key] = float64(val)
		}
	}

	// memory.events.local
	if m, err := readKeyValueFile(filepath.Join(dir, "memory.events.local")); err == nil {
		for key, val := range m {
			node.Stats["memory.events.local."+key] = float64(val)
		}
	}

	// memory.swap.events
	if m, err := readKeyValueFile(filepath.Join(dir, "memory.swap.events")); err == nil {
		for key, val := range m {
			node.Stats["memory.swap.events."+key] = float64(val)
		}
	}

	// io.stat (aggregate across devices)
	readIOStat(filepath.Join(dir, "io.stat"), node)

	// pids.current
	if v, err := readSingleUint(filepath.Join(dir, "pids.current")); err == nil {
		node.Stats["pids.current"] = float64(v)
	}

	// PSI files (per-cgroup)
	for _, res := range []string{"cpu", "memory", "io"} {
		if psi, err := readPSI(filepath.Join(dir, res+".pressure")); err == nil {
			node.Stats["psi."+res+".some.avg10"] = psi.Some.Avg10
			node.Stats["psi."+res+".some.avg60"] = psi.Some.Avg60
			node.Stats["psi."+res+".some.avg300"] = psi.Some.Avg300
			node.Stats["psi."+res+".full.avg10"] = psi.Full.Avg10
			node.Stats["psi."+res+".full.avg60"] = psi.Full.Avg60
			node.Stats["psi."+res+".full.avg300"] = psi.Full.Avg300
		}
	}
}

func readAllConfig(dir string, node *CgroupNode) {
	// CPU
	readConfigUint(dir, "cpu.weight", node)
	readConfigUint(dir, "cpu.weight.nice", node)
	readConfigStr(dir, "cpu.max", node)
	readConfigUint(dir, "cpu.max.burst", node)
	readConfigUint(dir, "cpu.idle", node)
	readConfigStr(dir, "cpu.uclamp.min", node)
	readConfigStr(dir, "cpu.uclamp.max", node)

	// Memory
	readConfigMaxOrUint(dir, "memory.min", node)
	readConfigMaxOrUint(dir, "memory.low", node)
	readConfigMaxOrUint(dir, "memory.high", node)
	readConfigMaxOrUint(dir, "memory.max", node)
	readConfigUint(dir, "memory.oom.group", node)
	readConfigMaxOrUint(dir, "memory.swap.high", node)
	readConfigMaxOrUint(dir, "memory.swap.max", node)
	readConfigMaxOrUint(dir, "memory.zswap.max", node)
	readConfigUint(dir, "memory.zswap.writeback", node)

	// IO
	readConfigStr(dir, "io.weight", node)
	readConfigStr(dir, "io.max", node)
	readConfigStr(dir, "io.prio.class", node)

	// PIDs
	readConfigMaxOrUint(dir, "pids.max", node)
}

func readConfigUint(dir, name string, node *CgroupNode) {
	v, err := readSingleUint(filepath.Join(dir, name))
	if err != nil {
		return
	}
	node.Config[name] = v
}

func readConfigStr(dir, name string, node *CgroupNode) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return
	}
	s := strings.TrimSpace(string(data))
	if s != "" {
		node.Config[name] = s
	}
}

func readConfigMaxOrUint(dir, name string, node *CgroupNode) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		node.Config[name] = "max"
	} else if v, err := strconv.ParseUint(s, 10, 64); err == nil {
		node.Config[name] = v
	}
}

func readIOStat(path string, node *CgroupNode) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	var rbytes, wbytes, rios, wios, dbytes, dios uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		for _, field := range fields[1:] {
			parts := strings.SplitN(field, "=", 2)
			if len(parts) != 2 {
				continue
			}
			val, _ := strconv.ParseUint(parts[1], 10, 64)
			switch parts[0] {
			case "rbytes":
				rbytes += val
			case "wbytes":
				wbytes += val
			case "rios":
				rios += val
			case "wios":
				wios += val
			case "dbytes":
				dbytes += val
			case "dios":
				dios += val
			}
		}
	}
	node.Stats["io.rbytes"] = float64(rbytes)
	node.Stats["io.wbytes"] = float64(wbytes)
	node.Stats["io.rios"] = float64(rios)
	node.Stats["io.wios"] = float64(wios)
	node.Stats["io.dbytes"] = float64(dbytes)
	node.Stats["io.dios"] = float64(dios)
}

func readKeyValueFile(path string) (map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := make(map[string]uint64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		m[fields[0]] = v
	}
	return m, nil
}

func readSingleUint(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func findCgroupRoot() string {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "/sys/fs/cgroup"
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 3 && fields[2] == "cgroup2" {
			return fields[1]
		}
	}
	return "/sys/fs/cgroup"
}
