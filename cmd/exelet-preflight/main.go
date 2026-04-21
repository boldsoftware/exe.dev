// exelet-preflight reads an exelet's on-disk state and reports whether the
// next startup would fail, whether reconciliation would release live-looking
// IP leases, and whether the nat safety bounds would trip. Read-only.
//
// Usage:
//
//	exelet-preflight [--data-dir /data/exelet] [--ipam-dir /data/exelet/network] [--json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	dataDir := flag.String("data-dir", "/data/exelet", "exelet data directory (contains instances/<id>/config.json)")
	ipamDir := flag.String("ipam-dir", "", "IPAM directory (contains leases.json). Defaults to <data-dir>/network")
	jsonOut := flag.Bool("json", false, "emit machine-readable JSON")
	repair := flag.Bool("repair-missing-leases", false, "backfill missing leases into leases.json (REQUIRES EXELET STOPPED)")
	dryRun := flag.Bool("dry-run", false, "with --repair-missing-leases, print the changes that would be made without writing")
	flag.Parse()

	if *ipamDir == "" {
		*ipamDir = *dataDir + "/network"
	}

	report, err := scan(*dataDir, *ipamDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
		os.Exit(64)
	}

	var repairRes *RepairResult
	if *repair {
		if !*dryRun {
			fmt.Fprintln(os.Stderr, "WARNING: writing leases.json directly. If the exelet is running, your changes will be clobbered by its in-memory IPAM cache on the next allocation. Stop the exelet first.")
		}
		repairRes, err = repairMissingLeases(*ipamDir, report, *dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "repair failed: %v\n", err)
			os.Exit(65)
		}
	}

	if *jsonOut {
		out := struct {
			Report *Report       `json:"report"`
			Repair *RepairResult `json:"repair,omitempty"`
		}{Report: report, Repair: repairRes}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else {
		printReport(os.Stdout, report)
		if repairRes != nil {
			printRepair(os.Stdout, repairRes)
		}
	}

	os.Exit(report.ExitCode())
}

func printRepair(w *os.File, r *RepairResult) {
	fmt.Fprintln(w)
	if r.DryRun {
		fmt.Fprintf(w, "repair (dry-run): %d entries would be added, %d skipped\n", len(r.Added), len(r.Skipped))
	} else {
		fmt.Fprintf(w, "repair: %d entries added, %d skipped\n", len(r.Added), len(r.Skipped))
	}
	for _, a := range r.Added {
		if r.DryRun {
			fmt.Fprintf(w, "  would add: %s\n", a)
		} else {
			fmt.Fprintf(w, "  added:     %s\n", a)
		}
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(w, "  skipped:   %s\n", s)
	}
	if r.Backup != "" {
		fmt.Fprintf(w, "  backup:    %s\n", r.Backup)
	}
}

func printReport(w *os.File, r *Report) {
	fmt.Fprintf(w, "preflight reconcile — data-dir=%s ipam-dir=%s\n", r.DataDir, r.IPAMDir)
	fmt.Fprintf(w, "  instances:       %d readable, %d unreadable\n", r.InstancesReadable, r.InstancesUnreadable)
	fmt.Fprintf(w, "  leases:          %d total\n", r.LeasesTotal)
	fmt.Fprintf(w, "  orphan leases:   %d\n", len(r.OrphanLeases))
	fmt.Fprintf(w, "  missing leases:  %d\n", len(r.MissingLeases))
	fmt.Fprintf(w, "  duplicate IPs:   %d\n", len(r.DuplicateIPs))
	fmt.Fprintf(w, "  mac mismatches:  %d\n", len(r.MACMismatches))
	if r.SafetyBound.WouldTrip {
		fmt.Fprintf(w, "  safety bound:    WOULD TRIP (%s)\n", r.SafetyBound.Reason)
	} else {
		fmt.Fprintf(w, "  safety bound:    would allow reconcile\n")
	}

	if len(r.UnreadableConfigs) > 0 {
		fmt.Fprintln(w, "\nunreadable configs:")
		for _, u := range r.UnreadableConfigs {
			fmt.Fprintf(w, "  %s — %s\n", u.InstanceID, u.Error)
		}
	}
	if len(r.DuplicateIPs) > 0 {
		fmt.Fprintln(w, "\nduplicate IPs:")
		for _, d := range r.DuplicateIPs {
			if d.LeaseMAC != "" {
				fmt.Fprintf(w, "  %s — lease MAC %s\n", d.IP, d.LeaseMAC)
			} else {
				fmt.Fprintf(w, "  %s — no lease (all claimants are squatters)\n", d.IP)
			}
			for _, c := range d.Claimants {
				label := "squatter"
				if c.OwnsLease {
					label = "owner   "
				}
				fmt.Fprintf(w, "    %s: %s (mac %s)\n", label, c.InstanceID, c.MACAddress)
			}
		}
	}
	if len(r.OrphanLeases) > 0 {
		fmt.Fprintln(w, "\norphan leases (would be released by reconcile):")
		for _, o := range r.OrphanLeases {
			fmt.Fprintf(w, "  %s (mac %s)\n", o.IP, o.MACAddress)
		}
	}
	if len(r.MissingLeases) > 0 {
		fmt.Fprintln(w, "\nmissing leases (instance has IP but no lease):")
		for _, m := range r.MissingLeases {
			fmt.Fprintf(w, "  %s → %s\n", m.InstanceID, m.IP)
		}
	}
	if len(r.MACMismatches) > 0 {
		fmt.Fprintln(w, "\nmac mismatches (instance MAC != lease MAC for same IP):")
		for _, m := range r.MACMismatches {
			fmt.Fprintf(w, "  %s (%s): instance=%s lease=%s\n", m.IP, m.InstanceID, m.InstanceMAC, m.LeaseMAC)
		}
	}

	fmt.Fprintln(w)
	switch r.ExitCode() {
	case 0:
		fmt.Fprintln(w, "OK")
	case 1:
		fmt.Fprintln(w, "INFO: safety bound would self-protect against reconcile (step 1+2 working as intended)")
	case 2:
		fmt.Fprintln(w, "ERROR: startup would fail at listInstances due to unreadable configs")
	case 3:
		fmt.Fprintln(w, "ERROR: orphan leases would be released and safety bound would not trip")
	case 4:
		fmt.Fprintln(w, "ERROR: duplicate IPs detected across instances (active collision)")
	}
}
