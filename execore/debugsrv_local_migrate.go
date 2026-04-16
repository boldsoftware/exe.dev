package execore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"exe.dev/email"
	"exe.dev/exedb"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
)

// localMigrateOp tracks a background local-migrate-all operation.
type localMigrateOp struct {
	mu        sync.Mutex
	Hostname  string              `json:"hostname"`
	StartedAt time.Time           `json:"started_at"`
	Total     int                 `json:"total"`
	Done      int                 `json:"done"`
	CurrentID string              `json:"current_id,omitempty"`
	Results   []localMigrateVMRes `json:"results"`
	Finished  bool                `json:"finished"`
}

type localMigrateVMRes struct {
	ID          string `json:"id"`
	OK          bool   `json:"ok"`
	DowntimeMs  int64  `json:"downtime_ms,omitempty"`
	ColdRestart bool   `json:"cold_restart,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (op *localMigrateOp) record(r localMigrateVMRes) {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.Results = append(op.Results, r)
	op.Done++
}

// localMigrateOpSnapshot is a mutex-free copy for JSON serialization.
type localMigrateOpSnapshot struct {
	Hostname  string              `json:"hostname"`
	StartedAt time.Time           `json:"started_at"`
	Total     int                 `json:"total"`
	Done      int                 `json:"done"`
	CurrentID string              `json:"current_id,omitempty"`
	Results   []localMigrateVMRes `json:"results"`
	Finished  bool                `json:"finished"`
}

func (op *localMigrateOp) snapshot() localMigrateOpSnapshot {
	op.mu.Lock()
	defer op.mu.Unlock()
	results := make([]localMigrateVMRes, len(op.Results))
	copy(results, op.Results)
	return localMigrateOpSnapshot{
		Hostname:  op.Hostname,
		StartedAt: op.StartedAt,
		Total:     op.Total,
		Done:      op.Done,
		CurrentID: op.CurrentID,
		Results:   results,
		Finished:  op.Finished,
	}
}

// handleDebugExeletLocalMigrateAll starts a background local live migration
// for all running VMs on an exelet.
func (s *Server) handleDebugExeletLocalMigrateAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hostname := r.PathValue("hostname")

	_, ec := s.resolveExelet(hostname)
	if ec == nil {
		http.Error(w, fmt.Sprintf("unknown exelet: %s", hostname), http.StatusNotFound)
		return
	}

	// Atomically claim the slot for this hostname. Serialized by
	// localMigrateOpsMu so two concurrent requests can't both pass the
	// already-in-progress check.
	op := &localMigrateOp{Hostname: hostname, StartedAt: time.Now()}
	s.localMigrateOpsMu.Lock()
	if val, ok := s.localMigrateOps.Load(hostname); ok {
		if existing := val.(*localMigrateOp); !existing.Finished {
			s.localMigrateOpsMu.Unlock()
			http.Error(w, "local migration already in progress for this exelet", http.StatusConflict)
			return
		}
	}
	s.localMigrateOps.Store(hostname, op)
	s.localMigrateOpsMu.Unlock()

	// If we bail out before starting the background goroutine, release the slot.
	claimReleased := false
	releaseClaim := func() {
		if claimReleased {
			return
		}
		claimReleased = true
		s.localMigrateOpsMu.Lock()
		// Only delete if it's still our op — a later successful request
		// may have replaced it (theoretically impossible while we hold the
		// request, but cheap safety).
		if val, ok := s.localMigrateOps.Load(hostname); ok && val.(*localMigrateOp) == op {
			s.localMigrateOps.Delete(hostname)
		}
		s.localMigrateOpsMu.Unlock()
	}
	defer releaseClaim()

	// List all instances to find running ones.
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := ec.client.ListInstances(listCtx, &computeapi.ListInstancesRequest{})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list instances: %v", err), http.StatusInternalServerError)
		return
	}

	var runningIDs []string
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to list instances: %v", err), http.StatusInternalServerError)
			return
		}
		if resp.Instance.State == computeapi.VMState_RUNNING {
			runningIDs = append(runningIDs, resp.Instance.ID)
		}
	}

	if len(runningIDs) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "no running VMs to migrate"})
		return
	}

	// Populate Total now that we know the count. Claim is kept — the
	// background goroutine takes ownership; skip the deferred release.
	op.mu.Lock()
	op.Total = len(runningIDs)
	op.mu.Unlock()
	claimReleased = true // Skip releaseClaim; the goroutine owns the slot now.

	// Run in background — detach from the request context but tie to the
	// server lifetime so shutdown cancels in-flight polls.
	//
	// VMs are migrated strictly serially. Each migration snapshots guest
	// memory to disk; doing several at once on one host would spike disk
	// I/O and memory pressure while also paralleling the per-VM downtime.
	// For a host with N VMs × ~D seconds each, total rollout time is N*D.
	// If we ever need faster rollouts add a bounded concurrency knob here.
	go func() {
		bgCtx := s.shutdownCtx
		for _, id := range runningIDs {
			if bgCtx.Err() != nil {
				// Server is shutting down — stop rolling out further migrations.
				op.record(localMigrateVMRes{ID: id, Error: "server shutting down"})
				continue
			}

			op.mu.Lock()
			op.CurrentID = id
			op.mu.Unlock()

			migCtx, migCancel := context.WithTimeout(bgCtx, 10*time.Minute)
			resp, err := ec.client.LiveMigrateLocal(migCtx, &computeapi.LiveMigrateLocalRequest{
				InstanceID: id,
			})
			migCancel()

			switch {
			case err != nil:
				// Total failure — even cold restart failed. VM is likely down.
				op.record(localMigrateVMRes{ID: id, Error: err.Error()})
				s.slog().ErrorContext(bgCtx, "local migrate failed", "exelet", hostname, "instance", id, "error", err)
				s.sendLocalMigrateDownEmail(bgCtx, id, err.Error())
			case resp.Outcome == computeapi.LiveMigrateLocalResponse_COLD_RESTARTED:
				// Migration failed but VM was cold-restarted.
				op.record(localMigrateVMRes{ID: id, OK: true, ColdRestart: true, DowntimeMs: resp.DowntimeMs, Error: resp.MigrationError})
				s.slog().WarnContext(bgCtx, "local migrate failed, cold restarted", "exelet", hostname, "instance", id, "error", resp.MigrationError, "downtime_ms", resp.DowntimeMs)
				s.sendLocalMigrateColdRestartEmail(bgCtx, id)
			default:
				op.record(localMigrateVMRes{ID: id, OK: true, DowntimeMs: resp.DowntimeMs})
				s.slog().InfoContext(bgCtx, "local migrate complete", "exelet", hostname, "instance", id, "downtime_ms", resp.DowntimeMs)
			}
		}

		op.mu.Lock()
		op.CurrentID = ""
		op.Finished = true
		op.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "started", "total": len(runningIDs)})
}

// handleDebugExeletLocalMigrateStatus returns the current status of a
// local-migrate-all operation.
func (s *Server) handleDebugExeletLocalMigrateStatus(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")

	val, ok := s.localMigrateOps.Load(hostname)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "none"})
		return
	}

	op := val.(*localMigrateOp)
	snap := op.snapshot()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

// sendLocalMigrateColdRestartEmail notifies the VM owner that their VM was
// restarted because a live migration fell back to a cold restart.
func (s *Server) sendLocalMigrateColdRestartEmail(ctx context.Context, instanceID string) {
	s.sendLocalMigrateOwnerEmail(ctx, instanceID, "cold-restart",
		"was restarted during a VMM upgrade",
		"Your VM %s was restarted as part of a hypervisor upgrade. "+
			"A live migration was attempted but could not complete, so the VM was cold-restarted instead.\n\n"+
			"If you run into any issues please contact support@exe.dev.\n\n"+
			"Thanks!\n\nexe.dev support",
	)
}

// sendLocalMigrateDownEmail notifies the VM owner that their VM is currently
// down because both live migration and cold restart failed. On-call will
// already be paged, but the owner deserves to hear from us too.
func (s *Server) sendLocalMigrateDownEmail(ctx context.Context, instanceID, migrateErr string) {
	s.sendLocalMigrateOwnerEmail(ctx, instanceID, "down",
		"is currently down after a failed VMM upgrade",
		"Your VM %s is currently down. We attempted a hypervisor upgrade "+
			"but both the live migration and an automated recovery failed. "+
			"Our team has been paged and is working to restore the VM.\n\n"+
			"Please contact support@exe.dev for updates.\n\n"+
			"Thanks for your patience,\n\nexe.dev support",
	)
	// Log the underlying migration error once so it lands in the owner-email
	// audit log alongside the send.
	s.slog().ErrorContext(ctx, "local migrate: VM down notification sent", "instance", instanceID, "migrate_error", migrateErr)
}

// sendLocalMigrateOwnerEmail looks up the box + owner for instanceID and
// sends an email. kind is a log-only tag (e.g. "cold-restart", "down").
// subjectSuffix and bodyFmt are formatted with box.Name (bodyFmt must have
// exactly one %s for the VM name).
func (s *Server) sendLocalMigrateOwnerEmail(ctx context.Context, instanceID, kind, subjectSuffix, bodyFmt string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByContainerID, &instanceID)
	if err != nil {
		s.slog().WarnContext(ctx, "local migrate: failed to look up box for owner email", "instance", instanceID, "kind", kind, "error", err)
		return
	}

	owner, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxOwnerByContainerID, &instanceID)
	if err != nil {
		s.slog().WarnContext(ctx, "local migrate: failed to look up owner for owner email", "instance", instanceID, "kind", kind, "error", err)
		return
	}

	subject := fmt.Sprintf("exe.dev: %s %s", box.Name, subjectSuffix)
	body := fmt.Sprintf(bodyFmt, box.Name)

	if err := s.sendEmail(ctx, sendEmailParams{
		emailType:   email.TypeBoxMaintenance,
		to:          owner.Email,
		subject:     subject,
		body:        body,
		htmlBody:    "",
		fromName:    "",
		replyTo:     "",
		attachments: nil,
		attrs:       []slog.Attr{slog.String("user_id", owner.UserID), slog.String("box", box.Name), slog.String("kind", kind)},
	}); err != nil {
		s.slog().WarnContext(ctx, "local migrate: failed to send owner email", "to", owner.Email, "box", box.Name, "kind", kind, "error", err)
	}
}
