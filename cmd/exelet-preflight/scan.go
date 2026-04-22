package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/pkg/ipam"
)

// Report is the full result of a preflight scan. Every field is safe to serialize
// as JSON for machine consumption.
type Report struct {
	DataDir string `json:"dataDir"`
	IPAMDir string `json:"ipamDir"`

	InstancesReadable   int `json:"instancesReadable"`
	InstancesUnreadable int `json:"instancesUnreadable"`
	LeasesTotal         int `json:"leasesTotal"`

	UnreadableConfigs []UnreadableConfig `json:"unreadableConfigs,omitempty"`
	OrphanLeases      []OrphanLease      `json:"orphanLeases,omitempty"`
	MissingLeases     []MissingLease     `json:"missingLeases,omitempty"`
	DuplicateIPs      []DuplicateIP      `json:"duplicateIPs,omitempty"`
	MACMismatches     []MACMismatch      `json:"macMismatches,omitempty"`

	// Safety-bound preview: would nat.ReconcileLeases abort?
	SafetyBound SafetyBound `json:"safetyBound"`
}

type UnreadableConfig struct {
	InstanceID string `json:"instanceID"`
	Path       string `json:"path"`
	Error      string `json:"error"`
}

type OrphanLease struct {
	IP         string `json:"ip"`
	MACAddress string `json:"macAddress"`
}

type MissingLease struct {
	InstanceID string `json:"instanceID"`
	IP         string `json:"ip"`
	MACAddress string `json:"macAddress"`
}

type DuplicateIP struct {
	IP          string   `json:"ip"`
	InstanceIDs []string `json:"instanceIDs"`
	// LeaseMAC is the MAC recorded in leases.json for this IP, if any.
	// When set, whichever Claimant has a matching MAC is the rightful
	// owner; the rest are squatters whose configs should be migrated
	// to a new IP or deleted. When empty, no lease exists for this IP
	// and every claimant is effectively a squatter.
	LeaseMAC  string              `json:"leaseMAC,omitempty"`
	Claimants []DuplicateClaimant `json:"claimants"`
}

// DuplicateClaimant is one instance claiming an IP that at least one other
// instance also claims. OwnsLease is true when this instance's MAC matches
// the IPAM lease MAC — that instance is the rightful owner of the IP.
type DuplicateClaimant struct {
	InstanceID string `json:"instanceID"`
	MACAddress string `json:"macAddress"`
	OwnsLease  bool   `json:"ownsLease"`
}

type MACMismatch struct {
	IP          string `json:"ip"`
	InstanceID  string `json:"instanceID"`
	InstanceMAC string `json:"instanceMAC"`
	LeaseMAC    string `json:"leaseMAC"`
}

type SafetyBound struct {
	WouldTrip bool   `json:"wouldTrip"`
	Reason    string `json:"reason,omitempty"`
	// ValidIPs is the number of instance IPs that would be considered valid.
	ValidIPs int `json:"validIPs"`
	// WouldRelease is the orphan count reconcile would try to release absent the bound.
	WouldRelease int `json:"wouldRelease"`
}

// scan reads instance configs under dataDir/instances and leases under
// ipamDir/leases.json, and produces a Report. No network or VMM calls.
func scan(dataDir, ipamDir string) (*Report, error) {
	r := &Report{DataDir: dataDir, IPAMDir: ipamDir}

	// 1. Enumerate instance configs.
	instancesDir := filepath.Join(dataDir, "instances")
	configs, err := filepath.Glob(filepath.Join(instancesDir, "*", "config.json"))
	if err != nil {
		return nil, fmt.Errorf("glob instance configs: %w", err)
	}

	// instanceIPs tracks IP -> []instanceID for duplicate detection.
	instanceIPs := make(map[string][]string)
	// instanceMACs tracks instanceID -> MAC for cross-check.
	instanceMACs := make(map[string]string)
	// instanceIDToIP tracks instanceID -> IP for missing-lease detection.
	instanceIDToIP := make(map[string]string)

	for _, cfgPath := range configs {
		id := filepath.Base(filepath.Dir(cfgPath))
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			r.UnreadableConfigs = append(r.UnreadableConfigs, UnreadableConfig{
				InstanceID: id,
				Path:       cfgPath,
				Error:      err.Error(),
			})
			r.InstancesUnreadable++
			continue
		}
		inst := &api.Instance{}
		if err := inst.Unmarshal(data); err != nil {
			r.UnreadableConfigs = append(r.UnreadableConfigs, UnreadableConfig{
				InstanceID: id,
				Path:       cfgPath,
				Error:      "unmarshal: " + err.Error(),
			})
			r.InstancesUnreadable++
			continue
		}
		r.InstancesReadable++

		ip := extractIP(inst)
		if ip == "" {
			continue
		}
		instanceIPs[ip] = append(instanceIPs[ip], id)
		instanceIDToIP[id] = ip
		if inst.VMConfig != nil && inst.VMConfig.NetworkInterface != nil {
			instanceMACs[id] = inst.VMConfig.NetworkInterface.MACAddress
		}
	}

	// 2. Load the IPAM lease DB.
	leasesPath := filepath.Join(ipamDir, "leases.json")
	leaseDB, err := loadLeases(leasesPath)
	if err != nil {
		return nil, fmt.Errorf("load leases %s: %w", leasesPath, err)
	}
	r.LeasesTotal = len(leaseDB.IPs)

	// 3. Cross-check.
	validIPs := make(map[string]struct{}, len(instanceIDToIP))
	for _, ip := range instanceIDToIP {
		validIPs[ip] = struct{}{}
	}

	// Duplicate IPs across instances. Annotate each claimant with whether
	// it is the rightful owner of the lease (MAC match) so the operator
	// knows which one to keep and which to migrate away.
	for ip, ids := range instanceIPs {
		if len(ids) <= 1 {
			continue
		}
		var leaseMAC string
		if lease, ok := leaseDB.IPs[ip]; ok {
			leaseMAC = lease.MACAddress
		}
		claimants := make([]DuplicateClaimant, 0, len(ids))
		for _, id := range ids {
			mac := instanceMACs[id]
			claimants = append(claimants, DuplicateClaimant{
				InstanceID: id,
				MACAddress: mac,
				OwnsLease:  leaseMAC != "" && mac != "" && macEqual(mac, leaseMAC),
			})
		}
		r.DuplicateIPs = append(r.DuplicateIPs, DuplicateIP{
			IP:          ip,
			InstanceIDs: ids,
			LeaseMAC:    leaseMAC,
			Claimants:   claimants,
		})
	}

	// Orphan leases (would be released by reconcile).
	for ip, lease := range leaseDB.IPs {
		if _, ok := validIPs[ip]; !ok {
			r.OrphanLeases = append(r.OrphanLeases, OrphanLease{
				IP:         ip,
				MACAddress: lease.MACAddress,
			})
		}
	}

	// Missing leases (instance has IP but no lease) and MAC mismatches.
	for id, ip := range instanceIDToIP {
		lease, ok := leaseDB.IPs[ip]
		if !ok {
			r.MissingLeases = append(r.MissingLeases, MissingLease{
				InstanceID: id,
				IP:         ip,
				MACAddress: instanceMACs[id],
			})
			continue
		}
		if instMAC := instanceMACs[id]; instMAC != "" && lease.MACAddress != "" && !macEqual(instMAC, lease.MACAddress) {
			r.MACMismatches = append(r.MACMismatches, MACMismatch{
				IP:          ip,
				InstanceID:  id,
				InstanceMAC: instMAC,
				LeaseMAC:    lease.MACAddress,
			})
		}
	}

	// 4. Safety-bound preview (mirrors nat.ReconcileLeases).
	r.SafetyBound.ValidIPs = len(validIPs)
	r.SafetyBound.WouldRelease = len(r.OrphanLeases)
	switch {
	case r.LeasesTotal > 10 && len(validIPs) == 0:
		r.SafetyBound.WouldTrip = true
		r.SafetyBound.Reason = "no valid instance IPs but leases exist"
	case r.LeasesTotal > 10 && r.SafetyBound.WouldRelease > r.LeasesTotal/2:
		r.SafetyBound.WouldTrip = true
		r.SafetyBound.Reason = fmt.Sprintf("would release %d of %d leases (>50%%)",
			r.SafetyBound.WouldRelease, r.LeasesTotal)
	}

	return r, nil
}

func extractIP(inst *api.Instance) string {
	if inst == nil || inst.VMConfig == nil || inst.VMConfig.NetworkInterface == nil || inst.VMConfig.NetworkInterface.IP == nil {
		return ""
	}
	raw := inst.VMConfig.NetworkInterface.IP.IPV4
	if idx := strings.Index(raw, "/"); idx > 0 {
		return raw[:idx]
	}
	return raw
}

func macEqual(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// loadLeases reads leases.json. A missing file yields an empty DB (clean fresh host).
func loadLeases(path string) (*ipam.LeaseDB, error) {
	db := &ipam.LeaseDB{
		Hosts: map[string]*ipam.Lease{},
		IPs:   map[string]*ipam.Lease{},
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return db, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, db); err != nil {
		return nil, err
	}
	if db.Hosts == nil {
		db.Hosts = map[string]*ipam.Lease{}
	}
	if db.IPs == nil {
		db.IPs = map[string]*ipam.Lease{}
	}
	return db, nil
}

// ExitCode determines the process exit code from the report.
// Higher number = worse problem.
//
//	0 — clean
//	1 — reconcile would self-abort via the nat safety bound (informational;
//	    step 1+2 would protect the host)
//	2 — unreadable configs (startup would fail at listInstances)
//	3 — live-looking leases would be released and safety bound would NOT trip
//	    (concerning: means orphan releases would proceed unchecked)
//	4 — duplicate IPs across instances (active collision on disk)
func (r *Report) ExitCode() int {
	code := 0
	if r.SafetyBound.WouldTrip {
		code = max(code, 1)
	}
	if r.InstancesUnreadable > 0 {
		code = max(code, 2)
	}
	if len(r.OrphanLeases) > 0 && !r.SafetyBound.WouldTrip {
		code = max(code, 3)
	}
	if len(r.DuplicateIPs) > 0 {
		code = max(code, 4)
	}
	return code
}
