# VM Migration: Stream → Unary RPC Implementation Plan

## Motivation

Both the `SendVM` stream (execore↔source) and `ReceiveVM` stream (source↔target)
break on long-running transfers. The gRPC streams carry only discrete
request/response pairs — all bulk data flows over a TCP sideband. The stream
reconnect logic (\~150 lines) is complex and entirely separate from the sideband
resume logic.

Converting to unary RPCs eliminates stream reconnect code and reduces the system
from two recovery domains (gRPC stream + TCP sideband) to one (TCP sideband only).

## Design Principles

1. **Opus's event transport**: batched, sequenced, replayable `PollSendVM`
2. **GPT's lifecycle safeguards**: `AbortSendVM`, `await_seq`, `client_request_id`, capability bits, per-state janitor TTLs
3. **New files, not refactored legacy**: keep `receive_vm.go` and `send_vm.go` working during transition
4. **No flag day**: version detection via `codes.Unimplemented` fallback, capability bits as hint

---

## 1. Proto Changes

All new RPCs and messages are additive. Legacy stream RPCs remain.

### New RPCs

```protobuf
service ComputeService {
  // ... existing RPCs unchanged ...

  // Legacy migration streams — kept during transition.
  rpc SendVM(stream SendVMRequest) returns (stream SendVMResponse);
  rpc ReceiveVM(stream ReceiveVMRequest) returns (stream ReceiveVMResponse);

  // === New: Source-side unary RPCs (called by execore) ===

  // InitSendVM starts a migration on the source exelet.
  // In direct mode, connects to target and begins transfer in background.
  // Returns immediately with a session ID.
  rpc InitSendVM(InitSendVMRequest) returns (InitSendVMResponse);

  // PollSendVM long-polls for migration events.
  // Returns accumulated events since after_seq, or blocks up to max_wait_ms.
  rpc PollSendVM(PollSendVMRequest) returns (PollSendVMResponse);

  // SubmitSendVMControl sends a control signal to the migration goroutine.
  // Used for PROCEED_WITH_PAUSE after IP reconfiguration.
  rpc SubmitSendVMControl(SubmitSendVMControlRequest) returns (SubmitSendVMControlResponse);

  // AbortSendVM cancels a migration session and releases resources.
  rpc AbortSendVM(AbortSendVMRequest) returns (AbortSendVMResponse);

  // === New: Target-side unary RPCs (called by source exelet) ===

  // InitReceiveVM creates a migration session on the target.
  // Acquires migration lock, prepares ZFS, opens sideband listener.
  rpc InitReceiveVM(InitReceiveVMRequest) returns (InitReceiveVMResponse);

  // GetReceiveVMResumeToken returns the ZFS resume token for a broken sideband.
  // Opens a new sideband listener for the resumed transfer.
  rpc GetReceiveVMResumeToken(GetReceiveVMResumeTokenRequest) returns (GetReceiveVMResumeTokenResponse);

  // AdvanceReceiveVMPhase signals that a sideband phase completed.
  // Returns the next sideband address (if not the last phase).
  rpc AdvanceReceiveVMPhase(AdvanceReceiveVMPhaseRequest) returns (AdvanceReceiveVMPhaseResponse);

  // UploadReceiveVMSnapshot sends CH snapshot file chunks.
  // Small volume (~5-25 RPCs for ~20-100MB of snapshot data).
  rpc UploadReceiveVMSnapshot(UploadReceiveVMSnapshotRequest) returns (UploadReceiveVMSnapshotResponse);

  // CompleteReceiveVM sends the checksum and triggers instance finalization.
  // Returns the created instance or error.
  rpc CompleteReceiveVM(CompleteReceiveVMRequest) returns (CompleteReceiveVMResponse);

  // AbortReceiveVM cancels a receive session and triggers rollback.
  rpc AbortReceiveVM(AbortReceiveVMRequest) returns (AbortReceiveVMResponse);
}
```

### New Messages

```protobuf
// --- Capability detection ---

message MigrationCapabilities {
  bool unary_send_vm = 1;
  bool unary_receive_vm = 2;
}

// Add to existing GetSystemInfoResponse:
//   MigrationCapabilities migration_capabilities = 3;

// --- Source-side (InitSendVM / PollSendVM / SubmitSendVMControl / AbortSendVM) ---

message InitSendVMRequest {
  string instance_id = 1;
  bool target_has_base_image = 2;
  bool two_phase = 3;
  bool live = 4;
  string target_address = 5;        // Direct mode: source connects to target
  string target_group_id = 6;
  string client_request_id = 7;     // Idempotency key for retry safety
}

message InitSendVMResponse {
  string session_id = 1;
}

message PollSendVMRequest {
  string session_id = 1;
  uint64 after_seq = 2;             // Return events with seq > after_seq (0 = all)
  uint32 max_wait_ms = 3;           // Long-poll timeout; server clamps to [0, 30000]
}

message PollSendVMResponse {
  repeated SendVMEvent events = 1;  // Empty if timeout with no new events
  bool completed = 2;               // Migration finished; check result event
}

message SendVMEvent {
  uint64 seq = 1;
  oneof type {
    SendVMMetadata metadata = 2;
    SendVMTargetReady target_ready = 3;
    SendVMStatus status = 4;
    SendVMProgress progress = 5;
    SendVMAwaitControl await_control = 6;
    SendVMResult result = 7;
  }
}

message SubmitSendVMControlRequest {
  string session_id = 1;
  uint64 await_seq = 2;             // Must match the seq of the pending AwaitControl event
  SendVMControl control = 3;
}

message SubmitSendVMControlResponse {}

message AbortSendVMRequest {
  string session_id = 1;
  string reason = 2;
}

message AbortSendVMResponse {}

// --- Target-side (InitReceiveVM / GetResumeToken / AdvancePhase / etc.) ---

message InitReceiveVMRequest {
  string instance_id = 1;
  Instance source_instance = 2;
  string base_image_id = 3;
  bool encrypted = 4;
  bytes encryption_key = 5;
  string group_id = 6;
  bool live = 7;
  bool discard_orphan = 8;          // Force-delete orphaned dataset from prior migration
  string client_request_id = 9;     // Idempotency key for retry safety
}

message InitReceiveVMResponse {
  string session_id = 1;
  bool has_base_image = 2;
  NetworkInterface target_network = 3;
  string sideband_addr = 4;
  bool resumable = 5;
  string resume_token = 6;          // Non-empty if orphan dataset found with resume state
  bool skip_ip_reconfig = 7;
}

message GetReceiveVMResumeTokenRequest {
  string session_id = 1;
}

message GetReceiveVMResumeTokenResponse {
  string token = 1;                 // ZFS receive_resume_token; empty if no resumable state
  string sideband_addr = 2;         // New TCP listener for resumed transfer
}

message AdvanceReceiveVMPhaseRequest {
  string session_id = 1;
  bool last = 2;                    // No more sideband phases follow
}

message AdvanceReceiveVMPhaseResponse {
  string sideband_addr = 1;         // Next phase TCP listener; empty if last=true
}

message UploadReceiveVMSnapshotRequest {
  string session_id = 1;
  string filename = 2;
  bytes data = 3;
  bool compressed = 4;
  bool is_last_chunk = 5;
}

message UploadReceiveVMSnapshotResponse {}

message CompleteReceiveVMRequest {
  string session_id = 1;
  string checksum = 2;
}

message CompleteReceiveVMResponse {
  Instance instance = 1;
  string error = 2;
  bool cold_booted = 3;
}

message AbortReceiveVMRequest {
  string session_id = 1;
  string reason = 2;
}

message AbortReceiveVMResponse {}
```

---

## 2. Deployment Phases

Each phase is independently deployable and backward compatible.

### Phase 0: Proto + Capability Bits

- Add all new RPCs and messages to `compute.proto`
- Add `MigrationCapabilities` to `GetSystemInfoResponse`
- Regenerate protobuf/gRPC stubs
- Populate capability bits in `GetSystemInfo` handler (both false initially)
- **No behavior change**. Unary RPCs return `Unimplemented`.

### Phase 1: Target-Side Unary ReceiveVM

- Implement `receiveVMSessionManager` and all 6 target-side unary RPCs
- Set `migration_capabilities.unary_receive_vm = true`
- Legacy `ReceiveVM(stream)` remains untouched
- **No callers yet** — old source exelets still use the stream

### Phase 2: Source Switches to Unary ReceiveVM

- Replace `directMigrationTarget` stream usage with unary `receiveVMTarget` interface
- Try `InitReceiveVM` first; fall back to legacy stream on `Unimplemented`
- **Delete reconnect code**: `reconnect()`, `reconnectFresh()`, stream-based `requestResumeToken()`
- Delete stream death detection branching in `streamViaSideband()`

### Phase 3: Source-Side Unary SendVM

- Implement `sendVMSessionManager` and all 4 source-side unary RPCs
- Introduce `migrationSender` interface so `sendVMCold`/`sendVMTwoPhase`/`sendVMLive` work with both stream and session backends
- Set `migration_capabilities.unary_send_vm = true`
- Legacy `SendVM(stream)` remains untouched
- **No callers yet** — old execore still uses the stream

### Phase 4: Execore Switches to Unary SendVM

- Update `migrateVM` and `migrateVMLive` in `execore/debugsrv.go`
- Use `InitSendVM` + `PollSendVM` + `SubmitSendVMControl` + `AbortSendVM`
- Fall back to legacy `SendVM(stream)` on `Unimplemented`

### Phase 5: Cleanup

- Remove stream reconnect code from `send_vm.go` (any remnants)
- Keep legacy `ReceiveVM(stream)` for `exelet-ctl` relay/debug
- Keep legacy `SendVM(stream)` for at least one release window
- Eventually remove both legacy stream RPCs

---

## 3. Server-Side Session Design

### 3.1 Receive Session Manager (Target Exelet)

New file: `exelet/services/compute/receive_vm_session.go`

```go
type receiveVMSessionManager struct {
    log      *slog.Logger
    service  *Service

    mu       sync.Mutex
    sessions map[string]*receiveVMSession           // session_id -> session
    byReqKey map[string]string                      // "instanceID:clientRequestID" -> session_id
}

type receiveVMSessionState int

const (
    recvStateInit         receiveVMSessionState = iota
    recvStateTransferring // sideband active
    recvStateCompleting   // finalization in progress
    recvStateDone         // terminal success
    recvStateFailed       // terminal failure
)

type receiveVMSession struct {
    id            string
    instanceID    string
    reqKey        string // for idempotency dedup
    state         receiveVMSessionState
    startReq      *api.InitReceiveVMRequest
    createdAt     time.Time
    lastActivity  time.Time

    // Lifecycle
    ctx           context.Context
    cancel        context.CancelFunc
    unlockOnce    sync.Once
    unlockFn      func() // releases migration lock

    // Migration state (same as current receive_vm.go stack variables)
    ready         *api.InitReceiveVMResponse
    rollback      *receiveVMRollback
    hasher        hash.Hash
    totalBytes    uint64
    targetNetwork *api.NetworkInterface
    snapshotDir   string
    encrypted     bool
    encryptionKey []byte
    zstdDec       *zstd.Decoder

    // Sideband
    mu            sync.Mutex  // protects sideband state
    sbLn          net.Listener
    sbConn        net.Conn
    sbDone        chan error  // result from sideband goroutine
    sbLocalHost   string     // sideband bind address

    // Result (set once terminal)
    result        *api.CompleteReceiveVMResponse
    terminalAt    time.Time
}
```

**Lifecycle:**
- `InitReceiveVM` → creates session, acquires migration lock, does all current
  preflight (replication suspend, memory check, orphan detection, base image check,
  network allocation, sideband listener, encryption key store, rollback init).
  Returns `session_id` + ready info.
- `GetReceiveVMResumeToken` → aborts current sideband, gets ZFS resume token,
  opens new listener. Requires `state == recvStateTransferring`.
- `AdvanceReceiveVMPhase` → waits for sideband goroutine to finish, opens next
  listener (if not last). Transitions to next phase.
- `UploadReceiveVMSnapshot` → writes CH snapshot chunks. Requires `state == recvStateTransferring`.
- `CompleteReceiveVM` → waits for final sideband, verifies checksum, creates
  instance config, starts VM (if live). Sets `state = recvStateDone`.
  Generous deadline (5 minutes) to accommodate CH restore.
- `AbortReceiveVM` → cancels context, triggers rollback, releases lock.
  Idempotent.

**Each RPC refreshes `lastActivity`.**

### 3.2 Send Session Manager (Source Exelet)

New file: `exelet/services/compute/send_vm_session.go`

```go
type sendVMSessionManager struct {
    log      *slog.Logger
    service  *Service

    mu       sync.Mutex
    sessions map[string]*sendVMSession
    byReqKey map[string]string // "instanceID:clientRequestID" -> session_id
}

type sendVMSession struct {
    id           string
    instanceID   string
    reqKey       string
    createdAt    time.Time

    // Lifecycle
    ctx          context.Context
    cancel       context.CancelCauseFunc
    unlockOnce   sync.Once
    unlockFn     func() // releases migration lock

    // Event buffer (append-only, never mutated after append)
    mu           sync.Mutex
    events       []*api.SendVMEvent // retained history, indexed by seq-1
    nextSeq      uint64             // next seq to assign
    completed    bool               // terminal event emitted
    lastActivity time.Time
    terminalAt   time.Time

    // Long-poll wake notification
    // Close-and-replace pattern: close waitCh to wake all blocked polls,
    // then replace with a fresh channel.
    waitCh       chan struct{}

    // Control channel for AwaitControl → SubmitSendVMControl
    pendingAwaitSeq uint64             // seq of pending AwaitControl; 0 = none
    controlCh       chan *api.SendVMControl // capacity 1
}
```

**Lifecycle:**
- `InitSendVM` → acquires migration lock, suspends replication, loads instance,
  starts background goroutine that runs the migration engine.
  Returns immediately with `session_id`.
  The goroutine uses a `sessionMigrationSender` (implements `migrationSender`)
  to emit events.
- `PollSendVM` → returns events with `seq > after_seq`. If none available,
  blocks up to `max_wait_ms`. Returns empty events list on timeout (not an RPC error).
  Multiple concurrent polls are safe — all see the same retained events.
- `SubmitSendVMControl` → validates `await_seq` matches `pendingAwaitSeq`,
  delivers control to goroutine via `controlCh`. Returns `FailedPrecondition`
  if seq mismatch or no pending await.
- `AbortSendVM` → cancels session context with cause, which propagates to
  the migration goroutine. Goroutine handles cleanup (unpause VM, destroy
  snapshots, etc.) via existing defer chains.

**The background goroutine must check for poll activity.** If `lastActivity` hasn't
been refreshed within 2 minutes of the first event being emitted, the goroutine
cancels itself. This prevents orphaned migrations when no one is polling.

### 3.3 Janitor

A single background goroutine per manager, started in `Service.Start()`,
stopped in `Service.Stop()`.

```go
func (m *receiveVMSessionManager) janitor(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            m.reap()
        }
    }
}
```

**TTLs (receive sessions):**
- Idle (no active sideband): 2 minutes since `lastActivity`
- Transfer-active (sideband goroutine running): 10 minutes since `lastActivity`
  — the sideband TCP keepalive provides liveness; this is the backstop
- Terminal: 10 minutes since `terminalAt` (retained for result retrieval)

**TTLs (send sessions):**
- Idle (no poll in progress): 2 minutes since `lastActivity`
- Terminal: 10 minutes since `terminalAt` (retained for final poll)

**Expiry action:** call `abort()` which cancels context, triggers rollback/cleanup,
releases migration lock.

### 3.4 Process Restart

If the exelet process restarts, all sessions are lost.

**Target restart (mid-receive):** Source's next unary RPC returns `NotFound`.
Source calls `InitReceiveVM` again. Target's orphan detection finds the partial
ZFS dataset and returns a `resume_token` in the response — same as current
stream reconnect behavior.

**Source restart (mid-send):** Execore's next `PollSendVM` returns `NotFound`.
Execore reports migration failure. Orchestrator retries the entire migration.
Target may retain the orphan dataset for the next attempt.

### 3.5 Idempotent Init

Both `InitSendVM` and `InitReceiveVM` accept a `client_request_id`. The session
manager maintains `byReqKey` mapping `"instanceID:clientRequestID" → sessionID`.

If a request arrives with a `client_request_id` that matches an existing session:
- Return the existing session's response (same `session_id` + ready info)
- Do not re-acquire the migration lock or re-do preflight

This handles the case where the response was lost and the client retries.

### 3.6 Concurrent Migration Protection

- `InitReceiveVM` acquires `lockForMigration(instanceID)` — same as today
- The session **owns** the lock until completion, abort, or janitor expiry
- A second `InitReceiveVM` for the same instance (different `client_request_id`)
  returns `FailedPrecondition` — same as today
- `InitSendVM` acquires the same lock on the source side

---

## 4. Long-Poll Design

### Contract

- Client starts with `after_seq = 0`
- Server returns all events with `seq > after_seq`
- If no matching events exist, block up to `max_wait_ms` (clamped to 30s)
- On timeout, return empty `events` list (not an RPC error)
- After processing events, client sets `after_seq` to the highest `seq` received
- If response is lost, client retries with the same `after_seq` and gets the
  same events again (replay-safe)
- `completed = true` signals the client to stop polling; the terminal
  `SendVMResult` event contains the outcome

### Implementation

```go
func (s *sendVMSession) poll(ctx context.Context, afterSeq uint64, maxWait time.Duration) (*api.PollSendVMResponse, error) {
    if maxWait > 30*time.Second {
        maxWait = 30 * time.Second
    }
    timer := time.NewTimer(maxWait)
    defer timer.Stop()

    for {
        s.mu.Lock()
        s.lastActivity = time.Now()
        events := s.eventsSinceLocked(afterSeq)
        if len(events) > 0 {
            completed := s.completed
            s.mu.Unlock()
            return &api.PollSendVMResponse{
                Events:    events,
                Completed: completed,
            }, nil
        }
        waitCh := s.waitCh
        s.mu.Unlock()

        select {
        case <-ctx.Done():
            return nil, status.FromContextError(ctx.Err()).Err()
        case <-timer.C:
            return &api.PollSendVMResponse{}, nil
        case <-waitCh:
            // New event available, loop to collect
        }
    }
}

// emit appends an event and wakes all blocked polls.
func (s *sendVMSession) emit(event *api.SendVMEvent) {
    s.mu.Lock()
    event.Seq = s.nextSeq
    s.nextSeq++
    s.events = append(s.events, event)
    if event.GetResult() != nil {
        s.completed = true
        s.terminalAt = time.Now()
    }
    ch := s.waitCh
    s.waitCh = make(chan struct{})
    s.mu.Unlock()
    close(ch) // wake all blocked polls
}
```

### Retention Bounds

- Max 1000 events retained per session
- When limit is reached, coalesce progress events: keep only the latest
  `SendVMProgress` before each non-progress event
- If client requests `after_seq` older than the oldest retained event,
  return events from the oldest available (skip gap)
- Terminal result event is never pruned

### AwaitControl Latency

The AwaitControl → Control exchange is on the critical path of live migration
downtime. With long-poll:

1. Migration goroutine emits `AwaitControl` event → wakes blocked poll immediately
2. Execore receives event, does IP reconfig, calls `SubmitSendVMControl`
3. Migration goroutine unblocks from `controlCh`

Extra latency vs stream: **1 additional RPC round-trip** for `SubmitSendVMControl`
(\~1-5ms on LAN). Negligible compared to the 2-10 second IP reconfiguration.

---

## 5. Version Detection / Fallback

### Capability Bits (Hint)

`GetSystemInfoResponse.migration_capabilities` tells the caller what the target/source
supports without making a migration-specific RPC. Useful for:
- Operator diagnostics (`exelet-ctl system info`)
- Pre-flight checks in execore before starting migration
- Avoiding unnecessary `Unimplemented` round-trips

### `Unimplemented` Fallback (Truth)

The actual compatibility check:

```go
// Source trying to reach target:
resp, err := target.InitReceiveVM(ctx, req)
if status.Code(err) == codes.Unimplemented {
    // Fall back to legacy ReceiveVM stream
    return s.initDirectMigrationTargetLegacy(ctx, targetAddress)
}

// Execore trying to reach source:
resp, err := source.InitSendVM(ctx, req)
if status.Code(err) == codes.Unimplemented {
    // Fall back to legacy SendVM stream
    return s.migrateVMLegacy(ctx, source, ...)
}
```

### Rules

- Fallback decision is **startup-only**: once a session starts via unary or stream,
  it stays on that transport for its entire lifetime
- Never fall back mid-session
- All 4 mixed-version combinations work:
  - new execore → old source: falls back to `SendVM(stream)`
  - old execore → new source: uses `SendVM(stream)` (no change)
  - new source → old target: falls back to `ReceiveVM(stream)`
  - old source → new target: uses `ReceiveVM(stream)` (no change)

---

## 6. Code Changes by File

### Phase 0

| File | Change |
|------|--------|
| `api/exe/compute/v1/compute.proto` | Add all new RPCs and messages |
| `pkg/api/exe/compute/v1/*.go` | Regenerate |
| `exelet/services/compute/system_info.go` | Add `MigrationCapabilities` to response |

### Phase 1: Target Unary ReceiveVM

| File | Change |
|------|--------|
| `exelet/services/compute/receive_vm_session.go` | **New.** `receiveVMSessionManager`, `receiveVMSession`, session lifecycle, janitor |
| `exelet/services/compute/receive_vm_unary.go` | **New.** Unary RPC handlers: `InitReceiveVM`, `GetReceiveVMResumeToken`, `AdvanceReceiveVMPhase`, `UploadReceiveVMSnapshot`, `CompleteReceiveVM`, `AbortReceiveVM` |
| `exelet/services/compute/compute.go` | Add `receiveVMSessions` field, init in `New()`, start janitor in `Start()`, abort all in `Stop()` |
| `exelet/services/compute/receive_vm.go` | **Unchanged.** Legacy stream handler stays as-is. |

The unary handlers in `receive_vm_unary.go` extract logic from `receive_vm.go` into
reusable session methods. The legacy stream handler is not modified — it continues
to work for `exelet-ctl` relay and old source exelets.

### Phase 2: Source Switches Direct Target to Unary

| File | Change |
|------|--------|
| `exelet/services/compute/receive_vm_target.go` | **New.** `receiveVMTarget` interface + `unaryReceiveVMTarget` (unary) + `streamReceiveVMTarget` (legacy) implementations |
| `exelet/services/compute/send_vm.go` | Replace `directMigrationTarget` creation with `receiveVMTarget` factory that tries unary first. Remove `reconnect()`, `reconnectFresh()`, stream-based `requestResumeToken()`. Simplify `streamViaSideband()` — remove gRPC-dead branching. |

The `receiveVMTarget` interface:

```go
type receiveVMTarget interface {
    // Init sends the start request and returns target readiness.
    Init(ctx context.Context, req *api.InitReceiveVMRequest) (*api.InitReceiveVMResponse, error)
    // ResumeToken returns the ZFS resume token and new sideband address.
    ResumeToken(ctx context.Context) (token, sidebandAddr string, err error)
    // AdvancePhase signals phase completion and returns the next sideband address.
    AdvancePhase(ctx context.Context, last bool) (sidebandAddr string, err error)
    // UploadSnapshot sends a CH snapshot chunk.
    UploadSnapshot(ctx context.Context, filename string, data []byte, compressed, lastChunk bool) error
    // Complete sends the checksum and returns the finalization result.
    Complete(ctx context.Context, checksum string) (*api.CompleteReceiveVMResponse, error)
    // Abort cancels the receive session.
    Abort(ctx context.Context, reason string) error
    // Close releases the underlying connection.
    Close()

    // SidebandAddr returns the current sideband TCP address.
    SidebandAddr() string
    // Resumable returns whether the target supports sideband resume.
    Resumable() bool
}
```

The `unaryReceiveVMTarget` implementation is trivial — each method is a single
unary RPC call. The `streamReceiveVMTarget` wraps the legacy stream for fallback.

### Phase 3: Source Unary SendVM

| File | Change |
|------|--------|
| `exelet/services/compute/send_vm_session.go` | **New.** `sendVMSessionManager`, `sendVMSession`, event buffer, long-poll, janitor |
| `exelet/services/compute/send_vm_unary.go` | **New.** Unary RPC handlers: `InitSendVM`, `PollSendVM`, `SubmitSendVMControl`, `AbortSendVM` |
| `exelet/services/compute/migration_sender.go` | **New.** `migrationSender` interface + `streamMigrationSender` (legacy) + `sessionMigrationSender` (unary) |
| `exelet/services/compute/send_vm.go` | Refactor `sendVMCold`, `sendVMTwoPhase`, `sendVMLive` to accept `migrationSender` interface instead of raw `stream`. Keep `SendVM` stream handler as thin adapter using `streamMigrationSender`. |
| `exelet/services/compute/compute.go` | Add `sendVMSessions` field, init, janitor, shutdown |

The `migrationSender` interface:

```go
type migrationSender interface {
    // EmitMetadata sends instance metadata to the orchestrator.
    EmitMetadata(m *api.SendVMMetadata) error
    // EmitTargetReady sends target readiness info.
    EmitTargetReady(tr *api.SendVMTargetReady) error
    // EmitStatus sends an informational status message.
    EmitStatus(msg string) error
    // EmitProgress sends a progress update.
    EmitProgress(bytesSent uint64) error
    // EmitAwaitControl sends an AwaitControl and blocks until control is received.
    // Returns the control response.
    EmitAwaitControl(ac *api.SendVMAwaitControl) (*api.SendVMControl, error)
    // EmitResult sends the terminal result.
    EmitResult(r *api.SendVMResult) error
    // Context returns the sender's context (cancelled on abort).
    Context() context.Context
}
```

`streamMigrationSender` wraps the existing `api.ComputeService_SendVMServer`.
`sessionMigrationSender` emits to the session's event buffer and blocks on
`controlCh` for `EmitAwaitControl`.

### Phase 4: Execore Switches to Unary SendVM

| File | Change |
|------|--------|
| `execore/debugsrv.go` | Update `migrateVM` and `migrateVMLive`: try `InitSendVM` → `PollSendVM` loop → `SubmitSendVMControl` → `AbortSendVM`. Fall back to legacy `SendVM(stream)` on `Unimplemented`. |
| `exelet/client/client.go` | Add thin wrappers for new unary RPCs |

Execore poll loop:

```go
var afterSeq uint64
for {
    resp, err := source.PollSendVM(ctx, &api.PollSendVMRequest{
        SessionId:  sessionID,
        AfterSeq:   afterSeq,
        MaxWaitMs:  10000,
    })
    if err != nil {
        // Network blip — retry
        continue
    }
    for _, event := range resp.Events {
        afterSeq = event.Seq
        switch v := event.Type.(type) {
        case *api.SendVMEvent_Status:
            progress("Source: %s", v.Status.Message)
        case *api.SendVMEvent_Progress:
            progress("Transferred %d MB...", v.Progress.BytesSent/(1024*1024))
        case *api.SendVMEvent_AwaitControl:
            // Do IP reconfig...
            source.SubmitSendVMControl(ctx, &api.SubmitSendVMControlRequest{
                SessionId: sessionID,
                AwaitSeq:  event.Seq,
                Control:   &api.SendVMControl{Action: api.SendVMControl_PROCEED_WITH_PAUSE},
            })
        case *api.SendVMEvent_Result:
            // Handle result...
            return
        }
    }
    if resp.Completed {
        break
    }
}
```

### Phase 5: Cleanup

| File | Change |
|------|--------|
| `exelet/services/compute/send_vm.go` | Remove any remaining stream reconnect code |
| `cmd/exelet-ctl/compute/instances/migrate.go` | Keep using legacy streams (relay path) |

---

## 7. Testing Strategy

### Unit Tests

**Send session tests** (`send_vm_session_test.go`):
- `TestPollReplay` — same `after_seq` returns same events
- `TestPollBatch` — multiple events returned in one poll
- `TestPollTimeout` — returns empty on timeout
- `TestPollWake` — blocked poll returns immediately when event emitted
- `TestAwaitControlMatching` — `SubmitSendVMControl` with correct `await_seq` unblocks goroutine
- `TestAwaitControlWrongSeq` — wrong `await_seq` returns `FailedPrecondition`
- `TestAbortCancelsGoroutine` — abort propagates cancellation
- `TestJanitorExpiry` — idle session gets reaped, lock released
- `TestIdempotentInit` — same `client_request_id` returns same session
- `TestProgressCoalescing` — retention bounds work correctly

**Receive session tests** (`receive_vm_session_test.go`):
- `TestInitReceiveDuplicateInstance` — second init fails with migration lock
- `TestIdempotentInit` — same `client_request_id` returns existing session
- `TestResumeToken` — returns ZFS token and new sideband address
- `TestAdvancePhase` — waits for sideband, opens next listener
- `TestAdvancePhaseLast` — no next listener when `last=true`
- `TestSnapshotUpload` — chunks written to correct files
- `TestCompleteReceive` — checksum verified, instance created
- `TestAbortRollback` — abort triggers rollback, lock released
- `TestJanitorTransferActive` — active sideband prevents premature reap
- `TestJanitorIdle` — idle session reaped at 2 minutes

### Integration Tests (e1e)

Add to `e1e/exelets/two_test.go`:
- `TestUnaryReceiveVMResume` — sideband fault injection + resume via `GetReceiveVMResumeToken`
- `TestUnaryReceiveVMStaleToken` — stale token triggers discard_orphan + fresh transfer
- `TestUnarySendVMLive` — full live migration via unary RPCs
- `TestUnarySendVMCold` — cold migration via unary RPCs
- `TestMixedVersionFallback` — verify legacy stream fallback when target returns `Unimplemented`

### Manual Testing Checklist

- [ ] Cold migration: new source → new target (unary)
- [ ] Cold migration: new source → old target (stream fallback)
- [ ] Live migration: new execore → new source → new target (full unary)
- [ ] Live migration: new execore → old source (stream fallback)
- [ ] Sideband resume: kill TCP mid-transfer, verify resume via unary RPC
- [ ] Abort: cancel migration from execore, verify source VM resumes
- [ ] Target restart: restart target exelet mid-sideband, verify resume works
- [ ] Long transfer: 50GB+ transfer across regions, verify no timeouts

---

## 8. Rollback Plan

### Per-Phase Rollback

Each phase is independently revertible:

- **Phase 4 rollback**: Revert execore binary. It goes back to `SendVM(stream)`. Source sessions never get created since no one calls `InitSendVM`.
- **Phase 3 rollback**: Revert source binary. Unary SendVM RPCs disappear, execore falls back to stream.
- **Phase 2 rollback**: Revert source binary. Source goes back to `ReceiveVM(stream)` with reconnect logic.
- **Phase 1 rollback**: Revert target binary. Unary ReceiveVM RPCs disappear, callers get `Unimplemented` and fall back.

### Emergency Rollback

If something goes wrong after Phase 2+4 (both sides on unary):
1. Revert execore to stream-based binary → immediately stops using unary SendVM
2. Revert source exelets → immediately stops using unary ReceiveVM
3. No data loss — the sideband TCP channel is independent of the control channel

### Graceful Rollback

Capability bits allow pre-flight detection. An operator can verify a target
supports unary before routing migrations to it. If capability bits are missing,
the system falls back automatically.

---

## 9. Code Eliminated

After Phase 5, the following are removed from `send_vm.go`:

| Item | Lines | Why eliminated |
|------|-------|----------------|
| `reconnect()` | ~25 | Unary RPCs auto-reconnect |
| `reconnectFresh()` | ~30 | Just call `InitReceiveVM(discard_orphan=true)` |
| Stream-based `requestResumeToken()` | ~15 | Replaced by `GetReceiveVMResumeToken` unary RPC |
| gRPC-dead branch in `streamViaSideband()` | ~30 | No stream to die |
| `directMigrationTarget.stream` / `cancelFunc` / `reconnReady` | ~20 | Replaced by `receiveVMTarget` interface |
| `preserveForResume` logic in `receive_vm.go` | ~15 | Session expiry handles cleanup |
| **Total** | **~135** | |

New code added (estimated):

| Item | Lines | Purpose |
|------|-------|--------|
| `receive_vm_session.go` | ~300 | Session manager, session struct, janitor |
| `receive_vm_unary.go` | ~250 | 6 unary RPC handlers |
| `send_vm_session.go` | ~250 | Session manager, event buffer, long-poll |
| `send_vm_unary.go` | ~150 | 4 unary RPC handlers |
| `migration_sender.go` | ~150 | Interface + 2 implementations |
| `receive_vm_target.go` | ~150 | Interface + 2 implementations |
| **Total** | **~1250** | |

Net: +1115 lines. The value is not in line count reduction — it's in eliminating
an entire recovery domain and making the system easier to reason about during
long-distance transfer failures.
