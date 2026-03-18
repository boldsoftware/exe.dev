import React, { useState, useEffect, useCallback } from "react";
import { PatchDiff } from "@pierre/diffs/react";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function splitPatch(patch: string): string[] {
  const parts = patch.split(/(?=^diff --git )/m);
  return parts.filter((p) => p.trim().length > 0);
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface Commit {
  id: string;
  issue_number: number;
  sha: string;
  subject: string;
  body: string;
  diff: string;
  github_url: string;
  status: "ready" | "approving" | "rejecting" | "failed";
  ci_run_url: string | null;
  error: string | null;
  labels: string[];
}

interface ApiState {
  commit: Commit | null;
  user: {
    email: string;
    id: string;
  };
  queue_depth: number;
  reservation_expires_at: number | null;
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

async function fetchState(): Promise<ApiState> {
  const res = await fetch("/api/state");
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

async function approveCommit(id: string): Promise<void> {
  const res = await fetch(`/api/approve/${id}`, { method: "POST" });
  if (!res.ok) throw new Error(await res.text());
}

async function rejectCommit(id: string, reason: string): Promise<void> {
  const res = await fetch(`/api/reject/${id}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ reason }),
  });
  if (!res.ok) throw new Error(await res.text());
}

async function skipCommit(id: string): Promise<void> {
  const res = await fetch(`/api/skip/${id}`, { method: "POST" });
  if (!res.ok) throw new Error(await res.text());
}

// ---------------------------------------------------------------------------
// Reservation Timer
// ---------------------------------------------------------------------------

function useReservationTimer(expiresAt: number | null): { display: string | null; expired: boolean } {
  const [remaining, setRemaining] = useState<string | null>(null);
  const [expired, setExpired] = useState(false);

  useEffect(() => {
    if (!expiresAt) {
      setRemaining(null);
      setExpired(false);
      return;
    }

    function tick() {
      const left = Math.max(0, Math.ceil(expiresAt! - Date.now() / 1000));
      if (left <= 0) {
        setRemaining("0:00");
        setExpired(true);
      } else {
        const m = Math.floor(left / 60);
        const s = left % 60;
        setRemaining(`${m}:${s.toString().padStart(2, "0")}`);
        setExpired(false);
      }
    }

    tick();
    const interval = setInterval(tick, 1000);
    return () => clearInterval(interval);
  }, [expiresAt]);

  return { display: remaining, expired };
}

// ---------------------------------------------------------------------------
// Commit Card
// ---------------------------------------------------------------------------

function CommitCard({
  commit,
  onSkip,
}: {
  commit: Commit;
  onSkip: () => void;
}) {
  const [showReject, setShowReject] = useState(false);
  const [rejectReason, setRejectReason] = useState("");
  const [acting, setActing] = useState(false);

  // Reset acting when server status changes (e.g. approving → failed)
  useEffect(() => {
    setActing(false);
  }, [commit.status]);

  const handleApprove = useCallback(async () => {
    setActing(true);
    try {
      await approveCommit(commit.id);
    } catch (e) {
      console.error("approve failed:", e);
      setActing(false);
    }
    // State will update via polling
  }, [commit.id]);

  const handleReject = useCallback(async () => {
    if (!rejectReason.trim()) return;
    setActing(true);
    try {
      await rejectCommit(commit.id, rejectReason);
    } catch (e) {
      console.error("reject failed:", e);
      setActing(false);
    }
  }, [commit.id, rejectReason]);

  const handleSkip = useCallback(async () => {
    setActing(true);
    try {
      await skipCommit(commit.id);
      onSkip();
    } catch (e) {
      console.error("skip failed:", e);
      setActing(false);
    }
  }, [commit.id, onSkip]);

  const isBusy = ["approving", "rejecting"].includes(commit.status) || acting;
  const isFailed = commit.status === "failed";

  const fetchInstructions = `git fetch origin refs/bored/${commit.id}\ngit cherry-pick FETCH_HEAD`;

  const issueUrl = `https://github.com/boldsoftware/bots/issues/${commit.issue_number}`;

  return (
    <div className={`commit-card ${isFailed ? "commit-card--failed" : ""}`}>
      <div className="commit-actions">
        <button
          className="btn btn-approve"
          onClick={handleApprove}
          disabled={isBusy}
        >
          {isBusy ? "Working..." : isFailed ? "Retry" : "Approve"}
        </button>

        <button
          className="btn btn-reject"
          onClick={() => setShowReject(!showReject)}
          disabled={isBusy}
        >
          Reject{showReject ? "" : "..."}
        </button>
        <button
          className="btn btn-skip"
          onClick={handleSkip}
          disabled={isBusy}
        >
          Leave for someone else
        </button>
      </div>

      {showReject && (
        <div className="reject-form">
          <textarea
            className="reject-reason"
            placeholder="Why are you rejecting this commit?"
            value={rejectReason}
            onChange={(e) => setRejectReason(e.target.value)}
            rows={3}
            autoFocus
          />
          <div className="reject-buttons">
            <button
              className="btn btn-reject-confirm"
              onClick={handleReject}
              disabled={!rejectReason.trim() || acting}
            >
              Reject
            </button>
            <button
              className="btn btn-cancel"
              onClick={() => { setShowReject(false); setRejectReason(""); }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      {isFailed && commit.error && (
        <div className="commit-error">
          <span className="commit-error-label">CI Failed</span>
          <span className="commit-error-text">{commit.error}</span>
          {commit.ci_run_url && (
            <a href={commit.ci_run_url} target="_blank" rel="noopener noreferrer" className="commit-ci-link">
              View CI Run
            </a>
          )}
        </div>
      )}

      <div className="commit-meta">
        <a href={commit.github_url} target="_blank" rel="noopener noreferrer" className="commit-github-link">
          View Commit on GitHub
        </a>
        <a href={issueUrl} target="_blank" rel="noopener noreferrer" className="commit-issue-link">
          Bots #{commit.issue_number}
        </a>
      </div>

      <h3 className="commit-subject">{commit.subject}</h3>

      {commit.body && (
        <pre className="commit-body">{commit.body}</pre>
      )}

      {commit.diff && (
        <div className="commit-diff">
          {splitPatch(commit.diff).map((filePatch, i) => (
            <PatchDiff
              key={i}
              patch={filePatch}
              options={{
                diffStyle: window.innerWidth >= 768 ? "split" : "unified",
                theme: { dark: "github-dark", light: "github-light" },
                themeType: window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light",
              }}
            />
          ))}
        </div>
      )}

      <div className="commit-fetch-instructions">
        <pre className="commit-fetch-code">{fetchInstructions}</pre>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Empty State
// ---------------------------------------------------------------------------

function EmptyState({ queueDepth }: { queueDepth: number }) {
  return (
    <div className="empty-state">
      <p>
        {queueDepth > 0
          ? "All items are currently reserved by others. Check back soon."
          : "Nothing ready yet. The worker is generating commits."}
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

export default function App() {
  const [state, setState] = useState<ApiState | null>(null);
  const [error, setError] = useState<string | null>(null);

  const poll = useCallback(async () => {
    try {
      const s = await fetchState();
      setState(s);
      setError(null);
    } catch (e: any) {
      setError(`Failed to fetch state: ${e?.message ?? e}`);
    }
  }, []);

  useEffect(() => {
    let active = true;

    async function loop() {
      while (active) {
        await poll();
        await new Promise((r) => setTimeout(r, 5000));
      }
    }

    loop();
    return () => { active = false; };
  }, [poll]);

  const { display: leaseDisplay, expired: leaseExpired } = useReservationTimer(state?.reservation_expires_at ?? null);
  const [dismissedExpiry, setDismissedExpiry] = useState(false);

  // Reset dismissal when we get a new commit
  useEffect(() => {
    setDismissedExpiry(false);
  }, [state?.commit?.id]);

  if (error && !state) {
    return (
      <div className="app">
        <div className="error-banner">{error}</div>
      </div>
    );
  }

  return (
    <div className="app">
      {leaseExpired && !dismissedExpiry && state?.commit && (
        <div className="modal-overlay">
          <div className="modal">
            <p>Your lease expired. Someone else may be reviewing this.</p>
            <div className="modal-buttons">
              <button className="btn btn-approve" onClick={() => window.location.reload()}>
                Reload
              </button>
              <button className="btn btn-skip" onClick={() => setDismissedExpiry(true)}>
                Continue anyway
              </button>
            </div>
          </div>
        </div>
      )}

      {state?.commit ? (
        <CommitCard key={state.commit.id} commit={state.commit} onSkip={poll} />
      ) : (
        <EmptyState queueDepth={state?.queue_depth ?? 0} />
      )}

      <footer className="footer">
        <span className="footer-path">scripts/agents/bored</span>
        {leaseDisplay && <span className="footer-lease">Lease: {leaseDisplay}</span>}
      </footer>
    </div>
  );
}
