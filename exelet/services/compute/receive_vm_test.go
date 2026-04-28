package compute

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestSidebandKeepaliveReaderTouchesDuringSlowCopy verifies that a long
// io.Copy through sidebandKeepaliveReader refreshes lastActivity on the
// receive session at least once per keepalive interval — so the janitor
// does not reap an actively-transferring session.
func TestSidebandKeepaliveReaderTouchesDuringSlowCopy(t *testing.T) {
	t.Parallel()

	// A reader that dribbles bytes once per tick, so the copy runs long
	// enough to cross several keepalive intervals.
	const (
		chunks        = 6
		chunkInterval = 20 * time.Millisecond
		keepalive     = 25 * time.Millisecond
	)

	sess := &receiveVMSession{lastActivity: time.Now()}
	initial := sess.lastActivity

	ka := &sidebandKeepaliveReader{
		r:        &trickleReader{chunks: chunks, interval: chunkInterval},
		sess:     sess,
		interval: keepalive,
	}
	if _, err := io.Copy(io.Discard, ka); err != nil {
		t.Fatalf("copy: %v", err)
	}

	sess.mu.Lock()
	got := sess.lastActivity
	sess.mu.Unlock()

	// The copy ran for ~chunks*chunkInterval ≈ 120ms with a 25ms keepalive
	// interval, so lastActivity must have been refreshed. Any forward
	// movement proves the wiring is correct; stronger bounds would be
	// timing-flaky.
	if !got.After(initial) {
		t.Fatalf("lastActivity not refreshed: initial=%v got=%v", initial, got)
	}
	if elapsed := got.Sub(initial); elapsed < chunkInterval {
		t.Fatalf("lastActivity advanced too little: %v (expected >= %v)", elapsed, chunkInterval)
	}
}

// trickleReader emits a fixed number of 1-byte reads with a delay between
// each, simulating a slow sideband stream. It is used to drive the
// sidebandKeepaliveReader across multiple keepalive intervals.
type trickleReader struct {
	chunks   int
	interval time.Duration
	emitted  int
}

func (r *trickleReader) Read(p []byte) (int, error) {
	if r.emitted >= r.chunks {
		return 0, io.EOF
	}
	if r.emitted > 0 {
		time.Sleep(r.interval)
	}
	p[0] = 'x'
	r.emitted++
	return 1, nil
}

// busyDatasetStorage simulates a storage manager whose Delete blocks until
// all in-flight zfs recv processes have exited — matching real zfs destroy
// behavior on a busy dataset. The test passes a readerDone channel that
// closes when the simulated zfs recv exits; Delete blocks on it.
type busyDatasetStorage struct {
	readerDone    <-chan struct{} // Delete blocks until this is closed (simulates busy dataset)
	deleteEntered chan struct{}   // closed when Delete is entered
}

func (m *busyDatasetStorage) Delete(_ context.Context, _ string) error {
	close(m.deleteEntered)
	// Simulate zfs destroy blocking on a busy dataset: wait for the
	// reader (zfs recv) to exit before returning.
	<-m.readerDone
	return nil
}

type mockDeleteNetworkManager struct{}

func (m *mockDeleteNetworkManager) DeleteInterface(_ context.Context, _, _, _ string) error {
	return nil
}

type testLogger struct{}

func (l *testLogger) WarnContext(_ context.Context, _ string, _ ...any)  {}
func (l *testLogger) DebugContext(_ context.Context, _ string, _ ...any) {}

// TestRollbackClosesRecvWritersBeforeDelete verifies that Rollback terminates
// in-flight zfs recv pipe writers before calling storageManager.Delete.
//
// It simulates the production failure mode:
//   - A zfs recv process holds a dataset open via a pipe reader
//   - zfs destroy (Delete) blocks while the dataset is busy
//   - Rollback must close the pipe writer first so zfs recv exits,
//     which unblocks zfs destroy
//
// If closeRecvWriters were removed, Delete would block forever and the test
// would time out.
func TestRollbackClosesRecvWritersBeforeDelete(t *testing.T) {
	t.Parallel()

	// Simulate an in-flight zfs recv: a reader goroutine that blocks
	// on the pipe until it's closed.
	pr, pw := io.Pipe()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		io.Copy(io.Discard, pr)
	}()

	storage := &busyDatasetStorage{
		readerDone:    readerDone,
		deleteEntered: make(chan struct{}),
	}
	rb := &receiveVMRollback{
		ctx:               context.Background(),
		log:               &testLogger{},
		storageManager:    storage,
		networkManager:    &mockDeleteNetworkManager{},
		instanceID:        "test-instance",
		instanceDir:       t.TempDir(),
		zfsDatasetCreated: true,
	}
	rb.trackRecvWriter(pw)

	// Rollback must: close pipe writer → reader exits → Delete unblocks → returns.
	// If the pipe writer is NOT closed before Delete, Delete blocks on
	// readerDone forever and this times out.
	rollbackDone := make(chan struct{})
	go func() {
		defer close(rollbackDone)
		rb.Rollback()
	}()

	select {
	case <-rollbackDone:
		// Rollback completed — pipe was closed before Delete, proving ordering.
	case <-time.After(5 * time.Second):
		t.Fatal("Rollback hung: pipe writer was not closed before Delete")
	}
}

func TestRollbackWithNoActiveWriters(t *testing.T) {
	t.Parallel()

	deleteEntered := make(chan struct{})
	alreadyClosed := make(chan struct{})
	close(alreadyClosed)
	storage := &busyDatasetStorage{
		readerDone:    alreadyClosed,
		deleteEntered: deleteEntered,
	}

	rb := &receiveVMRollback{
		ctx:               context.Background(),
		log:               &testLogger{},
		storageManager:    storage,
		networkManager:    &mockDeleteNetworkManager{},
		instanceID:        "test-instance",
		instanceDir:       t.TempDir(),
		zfsDatasetCreated: true,
	}

	// No pipe writers tracked — Rollback should still call Delete and return.
	rb.Rollback()

	select {
	case <-deleteEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("storageManager.Delete was never called")
	}
}

func TestRollbackClosesMultipleWriters(t *testing.T) {
	t.Parallel()

	// Simulate two in-flight zfs recv processes (phase 1 + phase 2).
	// Delete blocks until ALL readers exit.
	allReadersDone := make(chan struct{})
	var readersWg sync.WaitGroup

	rb := &receiveVMRollback{
		ctx:               context.Background(),
		log:               &testLogger{},
		storageManager:    &busyDatasetStorage{readerDone: allReadersDone, deleteEntered: make(chan struct{})},
		networkManager:    &mockDeleteNetworkManager{},
		instanceID:        "test-instance",
		instanceDir:       t.TempDir(),
		zfsDatasetCreated: true,
	}

	for range 2 {
		pr, pw := io.Pipe()
		rb.trackRecvWriter(pw)
		readersWg.Add(1)
		go func() {
			defer readersWg.Done()
			io.Copy(io.Discard, pr)
		}()
	}

	// Close allReadersDone when both reader goroutines finish.
	go func() {
		readersWg.Wait()
		close(allReadersDone)
	}()

	rollbackDone := make(chan struct{})
	go func() {
		defer close(rollbackDone)
		rb.Rollback()
	}()

	select {
	case <-rollbackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Rollback hung: not all pipe writers were closed before Delete")
	}
}

// TestPersistMigrationPlaceholderProtectsLeaseFromReconciler verifies the
// migration-path fix for the duplicate-IP class of bugs: during a live
// migration's transfer phase, the target-allocated IP must not be seen as
// an orphan lease by the reconciler. The placeholder written by
// persistMigrationPlaceholder accomplishes this two ways — it puts the IP
// into validIPs via listInstances, and its State=CREATING trips the
// transient-state abort. This test asserts both halves end-to-end.
func TestPersistMigrationPlaceholderProtectsLeaseFromReconciler(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	const (
		instanceID = "vm999999-migrating"
		ipCIDR     = "10.42.7.42/16"
		mac        = "02:11:22:33:44:55"
	)
	iface := &api.NetworkInterface{
		IP:         &api.IPAddress{IPV4: ipCIDR},
		MACAddress: mac,
	}
	if err := svc.persistMigrationPlaceholder(instanceID, iface); err != nil {
		t.Fatalf("persistMigrationPlaceholder: %v", err)
	}

	// Placeholder must land on disk with State=CREATING and the IP/MAC
	// populated, so listInstances and the reconciler see it.
	loaded, err := svc.loadInstanceConfig(instanceID)
	if err != nil {
		t.Fatalf("loadInstanceConfig after placeholder save: %v", err)
	}
	if loaded.State != api.VMState_CREATING {
		t.Fatalf("placeholder state = %v, want CREATING", loaded.State)
	}
	if loaded.VMConfig == nil || loaded.VMConfig.NetworkInterface == nil || loaded.VMConfig.NetworkInterface.IP == nil {
		t.Fatalf("placeholder missing network interface: %+v", loaded.VMConfig)
	}
	if got := loaded.VMConfig.NetworkInterface.IP.IPV4; got != ipCIDR {
		t.Errorf("placeholder IP = %q, want %q", got, ipCIDR)
	}
	if got := loaded.VMConfig.NetworkInterface.MACAddress; got != mac {
		t.Errorf("placeholder MAC = %q, want %q", got, mac)
	}

	// listInstances must surface the placeholder — this is the input the
	// reconciler sees. Drive it through reconcileIPLeasesFromInstances and
	// confirm the CREATING state aborts the reconcile (0 calls to
	// NAT.ReconcileLeases) so the in-flight migration lease cannot be
	// wrongly released.
	instances, err := svc.listInstances(t.Context())
	if err != nil {
		t.Fatalf("listInstances: %v", err)
	}
	found := false
	for _, inst := range instances {
		if inst.ID == instanceID {
			found = true
			if inst.State != api.VMState_CREATING {
				t.Errorf("listed placeholder state = %v, want CREATING", inst.State)
			}
		}
	}
	if !found {
		t.Fatalf("listInstances did not include placeholder %s", instanceID)
	}

	svc.reconcileIPLeasesFromInstances(t.Context(), instances)
	if n := nm.callCount(); n != 0 {
		t.Fatalf("reconciler should abort on CREATING migration placeholder; got %d ReconcileLeases calls", n)
	}
}

func TestSameVMIP(t *testing.T) {
	mkInstance := func(ipv4 string) *api.Instance {
		if ipv4 == "" {
			return &api.Instance{VMConfig: &api.VMConfig{}}
		}
		return &api.Instance{
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: ipv4},
				},
			},
		}
	}
	mkNet := func(ipv4 string) *api.NetworkInterface {
		return &api.NetworkInterface{IP: &api.IPAddress{IPV4: ipv4}}
	}

	tests := []struct {
		name   string
		source *api.Instance
		target *api.NetworkInterface
		want   bool
	}{
		{
			name:   "same IP different CIDR",
			source: mkInstance("10.42.0.42/24"),
			target: mkNet("10.42.0.42/16"),
			want:   true,
		},
		{
			name:   "same IP same CIDR",
			source: mkInstance("10.42.0.42/24"),
			target: mkNet("10.42.0.42/24"),
			want:   true,
		},
		{
			name:   "different IP nat to netns",
			source: mkInstance("10.42.0.5/16"),
			target: mkNet("10.42.0.42/24"),
			want:   false,
		},
		{
			name:   "nil source",
			source: nil,
			target: mkNet("10.42.0.42/24"),
			want:   false,
		},
		{
			name:   "nil target",
			source: mkInstance("10.42.0.42/24"),
			target: nil,
			want:   false,
		},
		{
			name:   "source missing network interface",
			source: mkInstance(""),
			target: mkNet("10.42.0.42/24"),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sameVMIP(tt.source, tt.target)
			if got != tt.want {
				t.Errorf("sameVMIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEditSnapshotConfigUpdatesTapName(t *testing.T) {
	mkSnapshotConfig := func(t *testing.T, tapName, cmdline string) string {
		t.Helper()
		dir := t.TempDir()
		chvConfig := map[string]any{
			"disks": []any{
				map[string]any{"path": "/old/disk"},
			},
			"payload": map[string]any{
				"kernel":  "/old/kernel",
				"cmdline": cmdline,
			},
			"net": []any{
				map[string]any{
					"tap": tapName,
					"mac": "02:73:6d:63:28:e6",
				},
			},
		}
		data, err := json.Marshal(chvConfig)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	readTap := func(t *testing.T, dir string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		nets := result["net"].([]any)
		netCfg := nets[0].(map[string]any)
		return netCfg["tap"].(string)
	}

	cmdline := "console=hvc0 root=/dev/vda ip=10.42.0.5::10.42.0.1:255.255.0.0:island-queen:eth0:none:1.1.1.1:8.8.8.8:ntp.ubuntu.com"
	srcVMConfig := &api.VMConfig{Name: "island-queen"}

	t.Run("nat to netns", func(t *testing.T) {
		dir := mkSnapshotConfig(t, "tap-5c4c99", cmdline)
		target := &api.NetworkInterface{
			Name:        "tap-vm000001",
			DeviceName:  "eth0",
			IP:          &api.IPAddress{IPV4: "10.42.0.42/24", GatewayV4: "10.42.0.1"},
			Nameservers: []string{"1.1.1.1"},
			NTPServer:   "ntp.ubuntu.com",
		}
		if err := editSnapshotConfig(dir, "/new/disk", "/new/kernel", "/run/op.sock", srcVMConfig, target); err != nil {
			t.Fatal(err)
		}
		if got := readTap(t, dir); got != "tap-vm000001" {
			t.Errorf("tap = %q, want %q", got, "tap-vm000001")
		}
	})

	t.Run("netns to nat", func(t *testing.T) {
		dir := mkSnapshotConfig(t, "tap-vm000001", cmdline)
		target := &api.NetworkInterface{
			Name:        "tap-a1b2c3",
			DeviceName:  "eth0",
			IP:          &api.IPAddress{IPV4: "10.42.0.7/16", GatewayV4: "10.42.0.1"},
			Nameservers: []string{"1.1.1.1"},
			NTPServer:   "ntp.ubuntu.com",
		}
		if err := editSnapshotConfig(dir, "/new/disk", "/new/kernel", "/run/op.sock", srcVMConfig, target); err != nil {
			t.Fatal(err)
		}
		if got := readTap(t, dir); got != "tap-a1b2c3" {
			t.Errorf("tap = %q, want %q", got, "tap-a1b2c3")
		}
	})

	t.Run("vsock socket path rewritten", func(t *testing.T) {
		dir := mkSnapshotConfig(t, "tap-orig", cmdline)
		// Inject a vsock config as cloud-hypervisor would persist it.
		configPath := filepath.Join(dir, "config.json")
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		cfg["vsock"] = map[string]any{"cid": 3, "socket": "/src/exelet/opssh.sock"}
		out, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, out, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := editSnapshotConfig(dir, "/new/disk", "/new/kernel", "/run/new-op.sock", srcVMConfig, nil); err != nil {
			t.Fatal(err)
		}
		result, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(result, &got); err != nil {
			t.Fatal(err)
		}
		vs, ok := got["vsock"].(map[string]any)
		if !ok {
			t.Fatalf("vsock missing: %v", got)
		}
		if vs["socket"] != "/run/new-op.sock" {
			t.Errorf("vsock socket = %v, want /run/new-op.sock", vs["socket"])
		}
	})

	t.Run("nil target skips tap update", func(t *testing.T) {
		dir := mkSnapshotConfig(t, "tap-orig", cmdline)
		if err := editSnapshotConfig(dir, "/new/disk", "/new/kernel", "/run/op.sock", srcVMConfig, nil); err != nil {
			t.Fatal(err)
		}
		if got := readTap(t, dir); got != "tap-orig" {
			t.Errorf("tap = %q, want %q (should be unchanged)", got, "tap-orig")
		}
	})
}
