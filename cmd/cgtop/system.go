//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type LoadAvg struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

type CPUUsage struct {
	UserPct   float64 `json:"user_pct"`
	SysPct    float64 `json:"sys_pct"`
	IdlePct   float64 `json:"idle_pct"`
	IOWaitPct float64 `json:"iowait_pct"`
}

type PSILine struct {
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`
}

type PSI struct {
	Some PSILine `json:"some"`
	Full PSILine `json:"full"`
}

type SystemStats struct {
	Hostname string   `json:"hostname"`
	LoadAvg  LoadAvg  `json:"loadavg"`
	CPU      CPUUsage `json:"cpu"`
	PSICPU   PSI      `json:"psi_cpu"`
	PSIMem   PSI      `json:"psi_memory"`
	PSIIO    PSI      `json:"psi_io"`
}

func readLoadAvg() (LoadAvg, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return LoadAvg{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return LoadAvg{}, fmt.Errorf("unexpected /proc/loadavg format")
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return LoadAvg{Load1: l1, Load5: l5, Load15: l15}, nil
}

// cpuJiffies holds raw jiffy values from /proc/stat.
type cpuJiffies struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (j cpuJiffies) total() uint64 {
	return j.user + j.nice + j.system + j.idle + j.iowait + j.irq + j.softirq + j.steal
}

func readCPUJiffies() (cpuJiffies, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuJiffies{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			return cpuJiffies{}, fmt.Errorf("unexpected /proc/stat cpu line")
		}
		parse := func(s string) uint64 {
			v, _ := strconv.ParseUint(s, 10, 64)
			return v
		}
		return cpuJiffies{
			user:    parse(fields[1]),
			nice:    parse(fields[2]),
			system:  parse(fields[3]),
			idle:    parse(fields[4]),
			iowait:  parse(fields[5]),
			irq:     parse(fields[6]),
			softirq: parse(fields[7]),
			steal:   parse(fields[8]),
		}, nil
	}
	return cpuJiffies{}, fmt.Errorf("/proc/stat: no cpu line")
}

func computeCPUUsage(prev, cur cpuJiffies) CPUUsage {
	dt := float64(cur.total() - prev.total())
	if dt == 0 {
		return CPUUsage{IdlePct: 100}
	}
	return CPUUsage{
		UserPct:   float64(cur.user+cur.nice-prev.user-prev.nice) / dt * 100,
		SysPct:    float64(cur.system+cur.irq+cur.softirq-prev.system-prev.irq-prev.softirq) / dt * 100,
		IdlePct:   float64(cur.idle-prev.idle) / dt * 100,
		IOWaitPct: float64(cur.iowait-prev.iowait) / dt * 100,
	}
}

func readPSI(path string) (PSI, error) {
	f, err := os.Open(path)
	if err != nil {
		return PSI{}, err
	}
	defer f.Close()

	var psi PSI
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		var target *PSILine
		if strings.HasPrefix(line, "some ") {
			target = &psi.Some
		} else if strings.HasPrefix(line, "full ") {
			target = &psi.Full
		} else {
			continue
		}
		for _, field := range strings.Fields(line)[1:] {
			parts := strings.SplitN(field, "=", 2)
			if len(parts) != 2 {
				continue
			}
			val, _ := strconv.ParseFloat(parts[1], 64)
			switch parts[0] {
			case "avg10":
				target.Avg10 = val
			case "avg60":
				target.Avg60 = val
			case "avg300":
				target.Avg300 = val
			}
		}
	}
	return psi, nil
}

func readSystemStats(prevJiffies cpuJiffies) (SystemStats, cpuJiffies, error) {
	var stats SystemStats
	var err error

	stats.Hostname, _ = os.Hostname()

	stats.LoadAvg, err = readLoadAvg()
	if err != nil {
		return stats, prevJiffies, fmt.Errorf("loadavg: %w", err)
	}

	curJiffies, err := readCPUJiffies()
	if err != nil {
		return stats, prevJiffies, fmt.Errorf("cpu jiffies: %w", err)
	}
	stats.CPU = computeCPUUsage(prevJiffies, curJiffies)

	stats.PSICPU, _ = readPSI("/proc/pressure/cpu")
	stats.PSIMem, _ = readPSI("/proc/pressure/memory")
	stats.PSIIO, _ = readPSI("/proc/pressure/io")

	return stats, curJiffies, nil
}
