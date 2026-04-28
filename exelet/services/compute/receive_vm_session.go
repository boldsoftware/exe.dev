package compute

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/network"
	"exe.dev/exelet/storage"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	receiveVMIdleTTL     = 2 * time.Minute
	receiveVMActiveTTL   = 10 * time.Minute
	receiveVMTerminalTTL = 10 * time.Minute
	receiveVMJanitorTick = 30 * time.Second

	// sidebandKeepaliveInterval bounds how often the sideband copy loop
	// refreshes lastActivity. It must be well under receiveVMActiveTTL so
	// that a multi-minute io.Copy does not look like inactivity to reap().
	sidebandKeepaliveInterval = 30 * time.Second
)

type receiveVMSessionState int

const (
	recvStateInit receiveVMSessionState = iota
	recvStateTransferring
	recvStateCompleting
	recvStateDone
	recvStateFailed
)

func (s receiveVMSessionState) String() string {
	switch s {
	case recvStateInit:
		return "init"
	case recvStateTransferring:
		return "transferring"
	case recvStateCompleting:
		return "completing"
	case recvStateDone:
		return "done"
	case recvStateFailed:
		return "failed"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

type receiveVMSessionManager struct {
	log     *slog.Logger
	service *Service

	mu       sync.Mutex
	sessions map[string]*receiveVMSession
	byReqKey map[string]string
}

type receiveVMSession struct {
	id         string
	instanceID string
	reqKey     string
	createdAt  time.Time

	ctx    context.Context
	cancel context.CancelFunc

	unlockOnce sync.Once
	unlockFn   func()

	startReq       *api.InitReceiveVMRequest
	ready          *api.InitReceiveVMResponse
	rollback       *receiveVMRollback
	storageManager storage.StorageManager
	hasher         hash.Hash
	totalBytes     uint64
	targetNetwork  *api.NetworkInterface
	snapshotDir    string
	zstdDec        *zstd.Decoder
	sbLocalHost    string

	zfsDatasetCreated bool

	mu           sync.Mutex
	state        receiveVMSessionState
	lastActivity time.Time
	terminalAt   time.Time
	result       *api.CompleteReceiveVMResponse

	sbLn   net.Listener
	sbConn net.Conn
	sbDone chan error
}

func newReceiveVMSessionManager(log *slog.Logger, service *Service) *receiveVMSessionManager {
	return &receiveVMSessionManager{
		log:      log,
		service:  service,
		sessions: make(map[string]*receiveVMSession),
		byReqKey: make(map[string]string),
	}
}

func generateMigrationSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (m *receiveVMSessionManager) create(_ context.Context, req *api.InitReceiveVMRequest) (*receiveVMSession, bool, error) {
	reqKey := ""
	if req.ClientRequestID != "" {
		reqKey = req.InstanceID + ":" + req.ClientRequestID
	}

	m.mu.Lock()
	if reqKey != "" {
		if existingID, ok := m.byReqKey[reqKey]; ok {
			if existing, ok := m.sessions[existingID]; ok {
				m.mu.Unlock()
				existing.mu.Lock()
				existing.touchLocked()
				existing.mu.Unlock()
				return existing, true, nil
			}
			delete(m.byReqKey, reqKey)
		}
	}

	sessionID, err := generateMigrationSessionID()
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}

	// Reserve the reqKey slot immediately to prevent TOCTOU races where
	// concurrent InitReceiveVM calls with the same clientRequestID both
	// pass the dedup check.
	if reqKey != "" {
		m.byReqKey[reqKey] = sessionID
	}
	m.mu.Unlock()

	sessCtx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	zstdDec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(2*4*1024*1024))
	if err != nil {
		cancel()
		m.unreserveReqKey(reqKey)
		return nil, false, fmt.Errorf("create zstd decoder: %w", err)
	}

	sess := &receiveVMSession{
		id:             sessionID,
		instanceID:     req.InstanceID,
		reqKey:         reqKey,
		createdAt:      now,
		ctx:            sessCtx,
		cancel:         cancel,
		storageManager: m.service.context.StorageManager,
		hasher:         sha256.New(),
		zstdDec:        zstdDec,
		state:          recvStateInit,
		lastActivity:   now,
	}
	sess.rollback = &receiveVMRollback{
		ctx:            context.Background(),
		log:            m.log,
		storageManager: m.service.context.StorageManager,
		networkManager: m.service.context.NetworkManager,
		cgroupPreparer: m.service.context.CgroupPreparer,
		instanceID:     req.InstanceID,
		instanceDir:    m.service.getInstanceDir(req.InstanceID),
		baseImageID:    req.BaseImageID,
		groupID:        req.GroupID,
	}
	return sess, false, nil
}

// unreserveReqKey removes a reqKey placeholder inserted by create() if init
// fails before publish() is called.
func (m *receiveVMSessionManager) unreserveReqKey(reqKey string) {
	if reqKey == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byReqKey, reqKey)
}

func (m *receiveVMSessionManager) publish(sess *receiveVMSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sess.id] = sess
	// byReqKey already reserved by create(); no need to re-insert.
}

func (m *receiveVMSessionManager) get(sessionID string) *receiveVMSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

func (m *receiveVMSessionManager) getAndTouch(sessionID string) *receiveVMSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[sessionID]
	if sess != nil {
		sess.mu.Lock()
		sess.touchLocked()
		sess.mu.Unlock()
	}
	return sess
}

func (m *receiveVMSessionManager) remove(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		if sess.reqKey != "" {
			delete(m.byReqKey, sess.reqKey)
		}
		delete(m.sessions, sessionID)
	}
}

func (m *receiveVMSessionManager) abortAll() {
	m.mu.Lock()
	sessions := make([]*receiveVMSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		sessions = append(sessions, sess)
	}
	m.mu.Unlock()

	for _, sess := range sessions {
		sess.abort("service shutting down")
		sess.mu.Lock()
		if sess.state != recvStateDone && sess.state != recvStateFailed {
			sess.state = recvStateFailed
			sess.terminalAt = time.Now()
			if sess.rollback != nil {
				sess.rollback.Rollback()
			}
		}
		sess.mu.Unlock()
	}
}

func (m *receiveVMSessionManager) janitor(ctx context.Context) {
	ticker := time.NewTicker(receiveVMJanitorTick)
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

func (m *receiveVMSessionManager) reap() {
	now := time.Now()

	m.mu.Lock()
	toReap := make([]string, 0)
	for id, sess := range m.sessions {
		sess.mu.Lock()
		expired := false
		switch sess.state {
		case recvStateDone, recvStateFailed:
			expired = !sess.terminalAt.IsZero() && now.Sub(sess.terminalAt) > receiveVMTerminalTTL
		case recvStateTransferring:
			expired = now.Sub(sess.lastActivity) > receiveVMActiveTTL
		default:
			expired = now.Sub(sess.lastActivity) > receiveVMIdleTTL
		}
		sess.mu.Unlock()
		if expired {
			toReap = append(toReap, id)
		}
	}
	m.mu.Unlock()

	for _, id := range toReap {
		sess := m.get(id)
		if sess == nil {
			continue
		}
		m.log.Warn("reaping expired receive session", "session", id, "instance", sess.instanceID)
		sess.abort("session expired")
		sess.mu.Lock()
		if sess.state != recvStateDone && sess.state != recvStateFailed {
			sess.state = recvStateFailed
			sess.terminalAt = time.Now()
			if sess.rollback != nil {
				sess.rollback.Rollback()
			}
		}
		sess.mu.Unlock()
		m.remove(id)
	}
}

func (sess *receiveVMSession) touchLocked() {
	sess.lastActivity = time.Now()
}

func (sess *receiveVMSession) touch() {
	sess.mu.Lock()
	sess.touchLocked()
	sess.mu.Unlock()
}

// sidebandKeepaliveReader wraps an io.Reader and refreshes the receive
// session's lastActivity timestamp while bytes are flowing, so the janitor
// does not reap a session that is actively transferring data through a
// long-running io.Copy.
type sidebandKeepaliveReader struct {
	r         io.Reader
	sess      *receiveVMSession
	interval  time.Duration
	lastTouch time.Time
}

func (k *sidebandKeepaliveReader) Read(p []byte) (int, error) {
	n, err := k.r.Read(p)
	if n > 0 {
		now := time.Now()
		if k.lastTouch.IsZero() || now.Sub(k.lastTouch) >= k.interval {
			k.sess.touch()
			k.lastTouch = now
		}
	}
	return n, err
}

func (sess *receiveVMSession) abort(reason string) {
	slog.Warn("receive VM session aborted", "session", sess.id, "instance", sess.instanceID, "reason", reason)
	sess.cancel()
	sess.unlockOnce.Do(func() {
		if sess.unlockFn != nil {
			sess.unlockFn()
		}
	})
}

func (sess *receiveVMSession) openSidebandListener(ctx context.Context) (string, error) {
	if p, ok := peer.FromContext(ctx); ok {
		if conn, err := net.Dial("udp", p.Addr.String()); err == nil {
			sess.sbLocalHost = conn.LocalAddr().(*net.UDPAddr).IP.String()
			conn.Close()
		}
	}
	if sess.sbLocalHost == "" {
		return "", nil
	}

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return "", fmt.Errorf("open sideband listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	addr := net.JoinHostPort(sess.sbLocalHost, strconv.Itoa(port))

	sess.mu.Lock()
	sess.sbLn = ln
	sess.sbDone = make(chan error, 1)
	sess.touchLocked()
	sess.mu.Unlock()

	sbDone := sess.sbDone
	go func() {
		sbDone <- sess.runSidebandPhase(ln)
	}()
	return addr, nil
}

func (sess *receiveVMSession) runSidebandPhase(ln net.Listener) error {
	defer ln.Close()
	go func() { <-sess.ctx.Done(); ln.Close() }()

	conn, err := ln.Accept()
	if err != nil {
		return fmt.Errorf("sideband accept: %w", err)
	}
	defer conn.Close()

	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(10 * time.Second)
	}
	go func() { <-sess.ctx.Done(); conn.Close() }()

	sess.mu.Lock()
	sess.sbConn = conn
	sess.touchLocked()
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		if sess.sbConn == conn {
			sess.sbConn = nil
		}
		sess.mu.Unlock()
	}()

	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- sess.storageManager.ReceiveSnapshotResumable(sess.ctx, sess.instanceID, pr)
		pr.Close()
	}()
	sess.rollback.trackRecvWriter(pw)

	tee := io.TeeReader(conn, sess.hasher)
	ka := &sidebandKeepaliveReader{r: tee, sess: sess, interval: sidebandKeepaliveInterval}
	n, copyErr := io.Copy(pw, ka)
	sess.totalBytes += uint64(n)
	_ = pw.Close()
	zfsErr := <-errCh
	if copyErr != nil && !errors.Is(copyErr, net.ErrClosed) {
		return fmt.Errorf("sideband copy: %w", copyErr)
	}
	if zfsErr != nil {
		return fmt.Errorf("zfs recv: %w", zfsErr)
	}
	return nil
}

func (sess *receiveVMSession) abortSideband() {
	sess.mu.Lock()
	ln := sess.sbLn
	conn := sess.sbConn
	sess.sbLn = nil
	sess.sbConn = nil
	sess.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

func (sess *receiveVMSession) waitSideband() error {
	sess.mu.Lock()
	sbDone := sess.sbDone
	sess.mu.Unlock()
	if sbDone == nil {
		return nil
	}

	select {
	case err := <-sbDone:
		sess.mu.Lock()
		sess.sbDone = nil
		sess.sbLn = nil
		sess.sbConn = nil
		sess.touchLocked()
		sess.mu.Unlock()
		return err
	case <-sess.ctx.Done():
		return sess.ctx.Err()
	}
}

func (sess *receiveVMSession) writeSnapshotChunk(filename string, data []byte, compressed bool) error {
	if sess.snapshotDir == "" {
		var suffix [4]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return fmt.Errorf("generate snapshot dir suffix: %w", err)
		}
		sess.snapshotDir = filepath.Join(sess.rollback.instanceDir, "snapshot-"+hex.EncodeToString(suffix[:]))
		if err := os.MkdirAll(sess.snapshotDir, 0o700); err != nil {
			return fmt.Errorf("create snapshot dir: %w", err)
		}
		sess.rollback.snapshotDirCreated = true
	}

	if compressed {
		var err error
		data, err = sess.zstdDec.DecodeAll(data, nil)
		if err != nil {
			return fmt.Errorf("decompress snapshot chunk %s: %w", filename, err)
		}
		if len(data) > sendVMChunkSize {
			return status.Errorf(codes.InvalidArgument, "decompressed snapshot chunk %s too large: %d bytes (max %d)", filename, len(data), sendVMChunkSize)
		}
	}

	f, err := os.OpenFile(filepath.Join(sess.snapshotDir, filename), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open snapshot file %s: %w", filename, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write snapshot file %s: %w", filename, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close snapshot file %s: %w", filename, err)
	}
	return nil
}

func supportsIPIsolation(nm any) bool {
	_, ok := nm.(network.ExtIPLookup)
	return ok
}
