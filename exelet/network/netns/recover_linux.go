//go:build linux

package netns

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/vishvananda/netlink"
	uns "github.com/vishvananda/netns"
)

const recoverWorkers = 8

type recoveredInfo struct {
	id     string
	ip     string
	bridge string
}

// RecoverExtIPs rebuilds the in-memory extIPs map by reading the ext-veth
// IP from each instance's network namespace.
func (m *Manager) RecoverExtIPs(ctx context.Context, instanceIDs []string) error {
	if len(instanceIDs) == 0 {
		m.log.InfoContext(ctx, "ext IP recovery complete", "recovered", 0, "total_instances", 0)
		return nil
	}

	ids := make(chan string)
	var wg sync.WaitGroup

	nWorkers := recoverWorkers
	if len(instanceIDs) < nWorkers {
		nWorkers = len(instanceIDs)
	}

	results := make(chan recoveredInfo, len(instanceIDs))

	for range nWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				ns := nsName(id)
				vExt := vethXNS(id)

				ip, err := readAddrInNS(ns, vExt)
				if err != nil {
					m.log.WarnContext(ctx, "failed to recover ext IP (netns or veth missing)",
						"instance", id, "ns", ns, "error", err)
					continue
				}

				bridge := getVethMasterBridge(vethXHost(id))

				results <- recoveredInfo{id: id, ip: ip, bridge: bridge}
			}
		}()
	}

	for _, id := range instanceIDs {
		ids <- id
	}
	close(ids)

	wg.Wait()
	close(results)

	m.mu.Lock()
	defer m.mu.Unlock()

	var recovered int
	for r := range results {
		m.extIPs[r.id] = r.ip
		if r.bridge != "" {
			m.vmBridge[r.id] = r.bridge
		}
		recovered++
		m.log.InfoContext(ctx, "recovered ext IP from netns",
			"instance", r.id, "ns", nsName(r.id), "ext_ip", r.ip, "shared_bridge", r.bridge)
	}

	if recovered > 0 {
		m.advanceAllocatorPastUsed()
	}

	m.log.InfoContext(ctx, "ext IP recovery complete",
		"recovered", recovered, "total_instances", len(instanceIDs),
		"workers", nWorkers)
	return nil
}

// advanceAllocatorPastUsed sets nextOctet3/4 past the highest used IP.
// Must be called with m.mu held.
func (m *Manager) advanceAllocatorPastUsed() {
	var maxO3, maxO4 byte
	for _, ip := range m.extIPs {
		var o3, o4 int
		if _, err := fmt.Sscanf(ip, "10.99.%d.%d", &o3, &o4); err != nil {
			continue
		}
		if byte(o3) > maxO3 || (byte(o3) == maxO3 && byte(o4) > maxO4) {
			maxO3 = byte(o3)
			maxO4 = byte(o4)
		}
	}
	next4 := maxO4 + 1
	next3 := maxO3
	if next4 == 0 {
		next3++
		next4 = 1
	}
	m.nextOctet3 = next3
	m.nextOctet4 = next4
}

func getVethMasterBridge(vethName string) string {
	link, err := netlink.LinkByName(vethName)
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

func readAddrInNS(nsName, ifName string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNS, err := uns.Get()
	if err != nil {
		return "", fmt.Errorf("get current netns: %w", err)
	}
	defer origNS.Close()

	targetNS, err := uns.GetFromName(nsName)
	if err != nil {
		return "", fmt.Errorf("get netns %s: %w", nsName, err)
	}
	defer targetNS.Close()

	if err := uns.Set(targetNS); err != nil {
		return "", fmt.Errorf("enter netns %s: %w", nsName, err)
	}
	defer uns.Set(origNS) //nolint:errcheck

	link, err := netlink.LinkByName(ifName)
	if err != nil {
		return "", fmt.Errorf("get %s in %s: %w", ifName, nsName, err)
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return "", fmt.Errorf("list addrs on %s in %s: %w", ifName, nsName, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no IPv4 addr on %s in %s", ifName, nsName)
	}

	return addrs[0].IP.String(), nil
}
