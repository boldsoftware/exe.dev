// memwatch wires the exelet/guestmetrics Pool into the resource manager's
// poll lifecycle. v0 is observability-only: no targeting policy, no
// actuator. The pool starts with the RM, scrapes per-tier cadence, and
// exposes data through Prom and the debug page.

package resourcemanager

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"exe.dev/exelet/guestmetrics"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
)

// EnvMemwatchDisable, when set to a non-empty value, disables the guest
// memory scraper and the host-pressure classifier. The kill switch is
// honoured at start time only — a running RM will not pick up a flip.
const EnvMemwatchDisable = "EXELET_MEMWATCH_DISABLE"

// memwatchEnabled reports whether the kill switch is off. Both the
// EXELET_MEMWATCH_DISABLE env var and the equivalent ExeletConfig field
// are honoured.
func (m *ResourceManager) memwatchEnabled() bool {
	if m.config != nil && m.config.MemwatchDisable {
		return false
	}
	return !memwatchDisabledByEnv(os.Getenv)
}

func memwatchDisabledByEnv(getenv func(string) string) bool {
	v := strings.TrimSpace(getenv(EnvMemwatchDisable))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// runtimeDataDirFromAddress parses a runtime URL like
// "cloudhypervisor:///var/tmp/exelet/runtime" and returns the path
// component. Returns "" on parse failure.
func runtimeDataDirFromAddress(addr string) string {
	if addr == "" {
		return ""
	}
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	return u.Path
}

// memdSocketPath mirrors VMM.OperatorSSHSocketPath — the same CH
// hybrid-vsock socket carries both op-ssh (port 2222) and memd (port
// 2223) under different CONNECT requests.
func memdSocketPath(runtimeDataDir, instanceID string) string {
	if runtimeDataDir == "" {
		return ""
	}
	return filepath.Join(runtimeDataDir, instanceID, "opssh.sock")
}

// updatePoolMembership reconciles the guestmetrics pool with the current
// running-instance set. Called from poll().
func (m *ResourceManager) updatePoolMembership(instances []*computeapi.Instance) {
	if m.guestPool == nil {
		return
	}
	runDir := runtimeDataDirFromAddress(m.config.RuntimeAddress)
	seen := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		// Only RUNNING. STARTING VMs have no memd up yet; PAUSED VMs
		// can't service vsock traffic; both would just emit scrape
		// failures. STOPPED→RUNNING transitions still re-add here
		// because Pool.Add is idempotent.
		if inst.GetState() != computeapi.VMState_RUNNING {
			continue
		}
		seen[inst.GetID()] = struct{}{}
		m.guestPool.Add(guestmetrics.VMInfo{
			ID:         inst.GetID(),
			Name:       inst.GetName(),
			SocketPath: memdSocketPath(runDir, inst.GetID()),
		})
	}
	// Drop entries whose VMs are gone or stopped.
	for _, e := range m.guestPool.Snapshot().Entries {
		if _, ok := seen[e.ID]; !ok {
			m.guestPool.Remove(e.ID)
		}
	}
}
