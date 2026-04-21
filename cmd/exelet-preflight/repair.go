package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"exe.dev/pkg/ipam"
)

// RepairResult summarizes what the repair step did (or would do, if dry).
type RepairResult struct {
	DryRun  bool     `json:"dryRun"`
	Added   []string `json:"added,omitempty"`   // "ip mac" strings, one per added lease
	Skipped []string `json:"skipped,omitempty"` // leases skipped (already present, empty MAC, etc.)
	Backup  string   `json:"backup,omitempty"`  // path to the .bak file written
}

// repairMissingLeases backfills each MissingLease from the report into the
// on-disk IPAM leases.json. The exelet MUST be stopped first — this function
// writes directly to leases.json, and the running exelet keeps an in-memory
// cache that would clobber our additions on its next IPAM operation.
//
// If dryRun is true, no files are modified; the returned RepairResult still
// lists the changes that would have been made.
func repairMissingLeases(ipamDir string, report *Report, dryRun bool) (*RepairResult, error) {
	res := &RepairResult{DryRun: dryRun}
	if len(report.MissingLeases) == 0 {
		return res, nil
	}

	leasesPath := filepath.Join(ipamDir, "leases.json")

	// Re-read the DB rather than reusing what scan loaded — the scan read
	// may be stale, and we want to merge into whatever is currently on disk.
	db, err := loadLeases(leasesPath)
	if err != nil {
		return nil, fmt.Errorf("load leases for repair: %w", err)
	}

	// Apply additions. Skip entries without a MAC or that already exist —
	// we never overwrite an existing lease, matching IPAM's own semantics.
	for _, m := range report.MissingLeases {
		if m.MACAddress == "" {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (instance %s has no MAC in config)", m.IP, m.InstanceID))
			continue
		}
		if _, exists := db.IPs[m.IP]; exists {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (IP already leased)", m.IP))
			continue
		}
		if _, exists := db.Hosts[m.MACAddress]; exists {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (MAC %s already leased)", m.IP, m.MACAddress))
			continue
		}
		lease := &ipam.Lease{IP: m.IP, MACAddress: m.MACAddress}
		db.IPs[m.IP] = lease
		db.Hosts[m.MACAddress] = lease
		res.Added = append(res.Added, fmt.Sprintf("%s %s", m.IP, m.MACAddress))
	}

	if dryRun || len(res.Added) == 0 {
		return res, nil
	}

	// Backup the existing file before overwriting. If leases.json does not
	// yet exist (unusual but possible), skip the backup.
	if _, err := os.Stat(leasesPath); err == nil {
		backup := leasesPath + ".bak"
		if err := copyFile(leasesPath, backup); err != nil {
			return nil, fmt.Errorf("backup leases.json: %w", err)
		}
		res.Backup = backup
	}

	if err := saveLeases(leasesPath, db); err != nil {
		return nil, fmt.Errorf("write leases.json: %w", err)
	}
	return res, nil
}

// saveLeases writes the DB using the same tmp-file + rename + directory fsync
// pattern as pkg/ipam.Datastore so crash-safety is equivalent.
func saveLeases(path string, db *ipam.LeaseDB) error {
	data, err := json.Marshal(db)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o660)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o660)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
