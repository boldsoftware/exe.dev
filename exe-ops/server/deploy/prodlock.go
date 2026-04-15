package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// prodLockURL and prodLockToken mirror scripts/check-prodlock.sh. The shell
// script is authoritative for the wire contract; if it changes, update here too.
const (
	prodLockBaseURL = "https://prodlock.exe.xyz:8000/api/"
	prodLockToken   = "exe1.XU4JHQFFMXHDMBSNFF5BF3HKS5"

	prodLockTimeout = 10 * time.Second
	prodLockMaxBody = 64 << 10
)

// ProdLockError is returned when a deploy is refused due to prod-lock state.
// It covers both "the environment is locked" and "we couldn't confirm the
// environment is unlocked" — the check fails closed in both cases so an
// unreachable prodlock server cannot silently authorise a deploy.
type ProdLockError struct {
	Stage    string // deploy stage that was being checked
	Env      string // prodlock env name (prod/staging)
	Locked   bool   // true iff the server explicitly reported locked
	LockedBy string
	Since    string
	Reason   string
	Err      error // non-nil on connectivity/parse errors
}

func (e *ProdLockError) Error() string {
	if e == nil {
		return ""
	}
	if e.Locked {
		var b strings.Builder
		fmt.Fprintf(&b, "%s is locked", e.Env)
		if e.LockedBy != "" {
			fmt.Fprintf(&b, " by %s", e.LockedBy)
		}
		if e.Since != "" {
			fmt.Fprintf(&b, " since %s", e.Since)
		}
		if e.Reason != "" {
			fmt.Fprintf(&b, " (%s)", e.Reason)
		}
		return b.String()
	}
	if e.Err != nil {
		return fmt.Sprintf("prodlock check failed for %s (refusing to deploy): %v", e.Env, e.Err)
	}
	return fmt.Sprintf("prodlock check failed for %s", e.Env)
}

func (e *ProdLockError) Unwrap() error { return e.Err }

// prodLockState is the response body from GET /api/{env}.
type prodLockState struct {
	Locked   bool   `json:"locked"`
	LockedBy string `json:"locked_by"`
	Since    string `json:"since"`
	Reason   string `json:"reason"`
}

// prodLockStage maps a deploy stage to the prodlock env name. Prod and global
// share the prod lock (global touches prod hosts). Staging has its own lock.
// Returns "" for stages that are not gated by prod-lock.
func prodLockStage(stage string) string {
	switch stage {
	case "prod", "global":
		return "prod"
	case "staging":
		return "staging"
	}
	return ""
}

// prodlockQuery fetches the current lock state for the given env. Used to
// decorate "already locked" errors with who/since/why details.
func prodlockQuery(ctx context.Context, env string) (prodLockState, error) {
	ctx, cancel := context.WithTimeout(ctx, prodLockTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prodLockBaseURL+env, nil)
	if err != nil {
		return prodLockState{}, err
	}
	req.Header.Set("Authorization", "Bearer "+prodLockToken)

	resp, err := (&http.Client{Timeout: prodLockTimeout}).Do(req)
	if err != nil {
		return prodLockState{}, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, prodLockMaxBody))
	if resp.StatusCode != http.StatusOK {
		return prodLockState{}, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var state prodLockState
	if err := json.Unmarshal(body, &state); err != nil {
		return prodLockState{}, fmt.Errorf("decode response: %w", err)
	}
	return state, nil
}

// prodlockAction POSTs a lock or unlock action with the given reason. Returns
// (true, nil) on success, (false, nil) if the env was already in the requested
// state (server returns 409), and (false, err) on any other failure.
func prodlockAction(ctx context.Context, env, action, reason string) (bool, error) {
	if action != "lock" && action != "unlock" {
		return false, fmt.Errorf("invalid action %q", action)
	}

	ctx, cancel := context.WithTimeout(ctx, prodLockTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prodLockBaseURL+env+"/"+action, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+prodLockToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: prodLockTimeout}).Do(req)
	if err != nil {
		return false, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, prodLockMaxBody))
		return false, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return true, nil
}

// defaultProdLockAcquire is the Manager's default prod-lock acquirer. It
// POSTs to /api/{env}/lock with a reason that identifies this deploy, and
// returns a release function that POSTs /unlock on call.
//
// Semantics:
//   - nil, nil              → no lock was needed (staging without a configured
//     stage mapping, or DEPLOY_SKIP_PRODLOCK=1 bypass)
//   - release, nil          → we acquired the lock; caller must call release
//   - nil, *ProdLockError   → refused: either the env was already locked by
//     someone (Locked=true) or the check failed
//     closed on a connectivity error (Locked=false)
//
// The release function is safe to call once and uses a fresh context so that
// a cancelled request context does not prevent cleanup.
func (m *Manager) defaultProdLockAcquire(ctx context.Context, stage, reason string) (func(), error) {
	env := prodLockStage(stage)
	if env == "" {
		return nil, nil
	}
	if os.Getenv("DEPLOY_SKIP_PRODLOCK") == "1" {
		return nil, nil
	}

	acquired, err := prodlockAction(ctx, env, "lock", reason)
	if err != nil {
		return nil, &ProdLockError{Stage: stage, Env: env, Err: err}
	}
	if !acquired {
		// 409 — already locked. Best-effort query for a nicer error message.
		state, qerr := prodlockQuery(ctx, env)
		if qerr != nil {
			return nil, &ProdLockError{
				Stage:  stage,
				Env:    env,
				Locked: true,
				Reason: "already locked (could not fetch lock state: " + qerr.Error() + ")",
			}
		}
		return nil, &ProdLockError{
			Stage:    stage,
			Env:      env,
			Locked:   true,
			LockedBy: state.LockedBy,
			Since:    state.Since,
			Reason:   state.Reason,
		}
	}

	// We took the lock. Return a release that best-effort unlocks.
	release := func() {
		rctx, cancel := context.WithTimeout(context.Background(), prodLockTimeout)
		defer cancel()
		if _, err := prodlockAction(rctx, env, "unlock", reason+" complete"); err != nil {
			m.log.Warn("prodlock unlock failed", "env", env, "stage", stage, "err", err)
		} else {
			m.log.Info("prodlock released", "env", env, "stage", stage)
		}
	}
	m.log.Info("prodlock acquired", "env", env, "stage", stage, "reason", reason)
	return release, nil
}

// prodLockReasonDeploy formats a reason string for a single-host deploy.
func prodLockReasonDeploy(req Request) string {
	reason := fmt.Sprintf("exe-ops: deploying %s to %s on %s", req.Process, req.Stage, req.Host)
	if req.InitiatedBy != "" {
		reason += " (by " + req.InitiatedBy + ")"
	}
	return reason
}

// prodLockReasonRollout formats a reason string for a rollout, scoped to a
// single stage (rollouts are nearly always single-stage, but we lock per
// distinct stage to be safe).
func prodLockReasonRollout(req RolloutRequest, stage string, nHosts int) string {
	process := ""
	if len(req.Targets) > 0 {
		process = req.Targets[0].Process
	}
	reason := fmt.Sprintf("exe-ops: rolling out %s to %s (%d host", process, stage, nHosts)
	if nHosts != 1 {
		reason += "s"
	}
	reason += ")"
	if req.InitiatedBy != "" {
		reason += " (by " + req.InitiatedBy + ")"
	}
	return reason
}
