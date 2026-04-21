//go:build linux

package netns

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// DeleteInterface tears down the network namespace and all associated kernel
// resources (veth pairs, TAP device, per-VM bridge) for the given instance.
// The ip and mac parameters are unused: unlike NAT, the netns manager tracks
// external IPs by instance ID in memory, so releaseExtIP only needs the id.
// They exist to satisfy the NetworkManager interface.
func (m *Manager) DeleteInterface(ctx context.Context, id, ip, mac string) error {
	ns := nsName(id)
	br := brName(id)
	tap := tapName(id)
	vBr := vethBrName(id)
	vXH := vethXHost(id)

	sharedBridgeName := m.getVMBridge(id)

	m.log.InfoContext(ctx, "deleting netns network interface",
		"instance", id, "netns", ns, "bridge", br, "shared_bridge", sharedBridgeName,
	)

	var errs []error

	if err := exec.CommandContext(ctx, "ip", "netns", "delete", ns).Run(); err != nil {
		// Check if the namespace simply doesn't exist (already cleaned up).
		if nsExists(ns) {
			errs = append(errs, fmt.Errorf("delete netns %s: %w", ns, err))
		}
	}

	for _, link := range []string{vBr, vXH, tap, br} {
		if err := delLinkByNameErr(link); err != nil {
			errs = append(errs, err)
		}
	}

	if sharedBridgeName != "" {
		m.decrementBridgePort(sharedBridgeName)
	}
	m.removeVMBridge(id)

	m.releaseExtIP(id)
	return errors.Join(errs...)
}

// ReconcileLeases cleans up orphaned kernel resources (namespaces, bridges,
// veth pairs, TAP devices) that belong to instances no longer known to the
// control plane. This handles the case where an instance was deleted but
// the exelet crashed before DeleteInterface completed.
func (m *Manager) ReconcileLeases(ctx context.Context, instances []*api.Instance) ([]string, error) {
	validInstanceIDs := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		validInstanceIDs[inst.ID] = struct{}{}
	}
	orphanNS, err := listOrphanNamespaces(validInstanceIDs)
	if err != nil {
		return nil, fmt.Errorf("list orphan namespaces: %w", err)
	}
	if len(orphanNS) == 0 {
		return nil, nil
	}

	var cleaned []string
	for _, ns := range orphanNS {
		// Derive the instance ID prefix from the namespace name (exe-vm000003 -> vm000003).
		vid := strings.TrimPrefix(ns, "exe-")

		m.log.WarnContext(ctx, "cleaning orphaned netns resources", "ns", ns, "vmid", vid)

		// Delete the namespace (this also destroys in-namespace interfaces).
		if err := exec.CommandContext(ctx, "ip", "netns", "delete", ns).Run(); err != nil {
			m.log.WarnContext(ctx, "failed to delete orphaned netns", "ns", ns, "error", err)
		}

		// Clean up root-namespace devices using the same naming convention
		// as CreateInterface/DeleteInterface.
		delLinkByName("vb-" + vid)
		delLinkByName("vx-" + vid)
		delLinkByName("tap-" + vid)
		delLinkByName("br-" + vid)

		cleaned = append(cleaned, ns)
	}

	return cleaned, nil
}

// listOrphanNamespaces returns exe-* namespaces that don't correspond to any
// instance in validInstanceIDs.
func listOrphanNamespaces(validInstanceIDs map[string]struct{}) ([]string, error) {
	out, err := exec.Command("ip", "netns", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("ip netns list: %w", err)
	}

	// Build a set of valid vmid prefixes (e.g. "vm000003") for matching
	// against namespace names (e.g. "exe-vm000003").
	validVMIDs := make(map[string]struct{}, len(validInstanceIDs))
	for id := range validInstanceIDs {
		idx := strings.Index(id, "-")
		if idx > 0 {
			validVMIDs[id[:idx]] = struct{}{}
		}
	}

	var orphans []string
	for _, line := range strings.Split(string(out), "\n") {
		// Lines are like "exe-vm000003" or "exe-vm000003 (id: 42)".
		name := strings.Fields(line)
		if len(name) == 0 {
			continue
		}
		ns := name[0]
		if !strings.HasPrefix(ns, "exe-") {
			continue
		}
		vid := strings.TrimPrefix(ns, "exe-")
		if _, ok := validVMIDs[vid]; ok {
			continue
		}
		orphans = append(orphans, ns)
	}
	return orphans, nil
}
