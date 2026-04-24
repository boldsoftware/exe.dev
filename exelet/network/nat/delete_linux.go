//go:build linux

package nat

import (
	"context"
	"errors"
	"fmt"
	"net"

	api "exe.dev/pkg/api/exe/compute/v1"

	"github.com/vishvananda/netlink"
)

// DeleteInterface tears down the per-VM network resources and releases the
// IPAM lease. It walks every step best-effort even if an earlier step
// fails: returning early on a TAP-delete error (the previous behavior) left
// the IPAM lease stranded, which is exactly the orphan-lease state the
// reconciler then confuses for a legitimate release and hands the IP to
// another VM — causing duplicate-IP conflicts. The MAC-scoped release is
// safe on a stale/reassigned IP (see ipam.Datastore.Release), so attempting
// it on the failure path is strictly better than leaving it unreleased.
func (n *NAT) DeleteInterface(ctx context.Context, id, ip, mac string) error {
	tapName := getTapID(id)

	// Find which bridge this TAP belongs to before deleting it
	bridgeName := n.getTapBridge(tapName)

	// Remove bandwidth limit before deleting TAP (cleanup, ignore errors)
	if err := n.removeBandwidthLimit(ctx, tapName); err != nil {
		n.log.WarnContext(ctx, "failed to remove bandwidth limit", "tap", tapName, "error", err)
	}

	var errs []error
	tapDeleted := true
	if err := n.deleteTapInterface(tapName); err != nil {
		// Don't abort: we still need to release the IPAM lease and other
		// per-VM state, or we strand an orphan that the reconciler will
		// later mis-handle.
		n.log.WarnContext(ctx, "failed to delete TAP interface; continuing cleanup to release lease",
			"tap", tapName, "error", err)
		errs = append(errs, fmt.Errorf("delete tap %s: %w", tapName, err))
		tapDeleted = false
	}

	// Only decrement the bridge port count if the TAP was actually removed;
	// otherwise the counter would drift below the real port count.
	if tapDeleted && bridgeName != "" {
		n.decrementBridgePort(bridgeName)
	}

	// Remove connection limit rule for this IP
	if ip != "" {
		if err := n.removeConnLimit(ctx, ip); err != nil {
			n.log.WarnContext(ctx, "failed to remove connection limit", "ip", ip, "error", err)
		}
		// Remove the per-TAP source-IP filter rule.
		n.removeSourceIPFilter(ctx, tapName, ip)
	}

	// Release the IP lease, but only when we know which MAC owns it.
	// Releasing by bare IP is unsafe: if the IP has been reassigned to a
	// different MAC since this delete path was entered, we would remove
	// the new owner's lease and create a duplicate-IP conflict.
	if ip != "" && mac != "" {
		if err := n.ipam.Release(mac, ip); err != nil {
			n.log.WarnContext(ctx, "failed to release IP lease", "mac", mac, "ip", ip, "error", err)
		} else {
			n.log.DebugContext(ctx, "released IP lease", "tap", tapName, "mac", mac, "ip", ip)
		}
	} else if ip != "" {
		n.log.WarnContext(ctx, "skipping IP lease release: no MAC known for instance, reconciler will recover",
			"tap", tapName, "ip", ip, "instance", id)
	}

	return errors.Join(errs...)
}

// ReconcileLeases releases any IPAM leases whose IPs are not associated with
// the given instances. This cleans up orphaned leases from failed migrations
// or incomplete deletions.
func (n *NAT) ReconcileLeases(ctx context.Context, instances []*api.Instance) ([]string, error) {
	validIPs := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		if inst.VMConfig != nil && inst.VMConfig.NetworkInterface != nil && inst.VMConfig.NetworkInterface.IP != nil {
			ip, _, err := net.ParseCIDR(inst.VMConfig.NetworkInterface.IP.IPV4)
			if err != nil {
				n.log.WarnContext(ctx, "instance has unparseable IP, its lease will be released",
					"instance", inst.ID,
					"ip", inst.VMConfig.NetworkInterface.IP.IPV4,
					"error", err,
				)
				continue
			}
			validIPs[ip.String()] = struct{}{}
		}
	}

	leases, err := n.ipam.ListLeases()
	if err != nil {
		return nil, err
	}

	// Safety bounds: refuse to release leases if the instance list looks
	// suspiciously incomplete. A partial list (e.g. from a transient
	// read failure) combined with this function's destructive behavior
	// can mass-release live leases whose IPs then get reassigned,
	// producing duplicate-IP conflicts.
	if len(leases) > 10 && len(validIPs) == 0 {
		n.log.ErrorContext(ctx,
			"aborting IPAM reconciliation: no valid instance IPs but leases exist — instance list is likely incomplete",
			"leases", len(leases),
		)
		return nil, nil
	}
	var wouldRelease int
	for _, lease := range leases {
		if _, ok := validIPs[lease.IP]; !ok {
			wouldRelease++
		}
	}
	if len(leases) > 10 && wouldRelease > len(leases)/2 {
		n.log.ErrorContext(ctx,
			"aborting IPAM reconciliation: would release more than half of all leases — instance list is likely incomplete",
			"leases", len(leases),
			"valid_instance_ips", len(validIPs),
			"would_release", wouldRelease,
		)
		return nil, nil
	}

	var released []string
	for _, lease := range leases {
		if _, ok := validIPs[lease.IP]; !ok {
			n.log.WarnContext(ctx, "releasing orphaned IP lease", "ip", lease.IP, "mac", lease.MACAddress)
			if err := n.ipam.Release(lease.MACAddress, lease.IP); err != nil {
				n.log.WarnContext(ctx, "failed to release orphaned IP lease", "ip", lease.IP, "mac", lease.MACAddress, "error", err)
				continue
			}
			// Explicit per-lease success log so post-deploy we can count
			// how often the reconciler actually releases leases (vs.
			// identifies candidates) and correlate by mac/ip with
			// duplicate-IP incidents.
			n.log.InfoContext(ctx, "reconciler released orphaned IP lease", "ip", lease.IP, "mac", lease.MACAddress)
			released = append(released, lease.IP)
		}
	}

	return released, nil
}

// getTapBridge returns the bridge name that a TAP interface belongs to
func (n *NAT) getTapBridge(tapName string) string {
	link, err := netlink.LinkByName(tapName)
	if err != nil {
		return ""
	}

	masterIndex := link.Attrs().MasterIndex
	if masterIndex == 0 {
		return ""
	}

	master, err := netlink.LinkByIndex(masterIndex)
	if err != nil {
		return ""
	}

	return master.Attrs().Name
}
