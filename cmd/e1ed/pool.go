package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type SlotState int

const (
	SlotCreating SlotState = iota
	SlotReady
	SlotClaimed
	SlotDestroying
)

func (s SlotState) String() string {
	switch s {
	case SlotCreating:
		return "creating"
	case SlotReady:
		return "ready"
	case SlotClaimed:
		return "claimed"
	case SlotDestroying:
		return "destroying"
	default:
		return "unknown"
	}
}

type VMInfo struct {
	Name    string
	IP      string
	User    string
	EnvFile string
}

type Slot struct {
	State     SlotState
	VM        *VMInfo
	ReadyAt   time.Time
	ClaimedBy string
}

type Pool struct {
	mu       sync.Mutex
	slots    []*Slot
	target   int
	maxIdle  time.Duration
	opsDir   string // path to ops/ directory (from permanent worktree)
	wg       sync.WaitGroup
	stop     chan struct{}
	claimCh  chan struct{} // signaled when a slot becomes ready
	executor Executor
}

// Executor abstracts shell commands for testing.
type Executor interface {
	// StartVM runs ci-vm.py create and returns VM info.
	StartVM(ctx context.Context, name, opsDir string) (*VMInfo, error)
	// DestroyVM runs ci-vm.py destroy with the given env file.
	DestroyVM(ctx context.Context, envFile, opsDir string) error
	// DestroyVMByName destroys a VM by name, for cleanup
	// when no env file is available (e.g., creation timed out).
	DestroyVMByName(ctx context.Context, name, opsDir string)
}

type ShellExecutor struct{}

func (e *ShellExecutor) StartVM(ctx context.Context, name, opsDir string) (*VMInfo, error) {
	script := filepath.Join(opsDir, "ci-vm.py")
	outDir, err := os.MkdirTemp("", "e1ed-vm-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, "python3", script, "create")
	cmd.Dir = filepath.Dir(opsDir) // repo root
	env := os.Environ()
	// Ensure HOME is set; systemd may not provide it.
	if os.Getenv("HOME") == "" {
		env = append(env, "HOME=/root")
	}
	cmd.Env = append(env,
		"NAME="+name,
		"OUTDIR="+outDir,
		"E1E_VM_PREFIX=e1ed",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ci-vm.py create: %w", err)
	}

	envFile := filepath.Join(outDir, name+".env")
	return parseEnvFile(envFile)
}

func (e *ShellExecutor) DestroyVM(ctx context.Context, envFile, opsDir string) error {
	script := filepath.Join(opsDir, "ci-vm.py")
	cmd := exec.CommandContext(ctx, "python3", script, "destroy", envFile)
	cmd.Dir = filepath.Dir(opsDir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (e *ShellExecutor) DestroyVMByName(ctx context.Context, name, opsDir string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	slog.InfoContext(ctx, "destroying VM by name", "name", name)

	// Best-effort: kill the cloud-hypervisor process and clean up.
	pidFile := fmt.Sprintf("/tmp/ch-pid-%s", name)
	if data, err := os.ReadFile(pidFile); err == nil {
		pid := strings.TrimSpace(string(data))
		if pid != "" {
			exec.CommandContext(ctx, "sudo", "kill", "-9", pid).Run()
		}
	}

	// Clean up TAP interface.
	tapName := fmt.Sprintf("vm%s", name) // approximate; ci-vm.py uses sha256
	exec.CommandContext(ctx, "sudo", "ip", "link", "del", tapName).Run()

	// Clean up disk images and temp files.
	workdir := "/var/lib/libvirt/images"
	os.Remove(filepath.Join(workdir, name+".qcow2"))
	os.Remove(filepath.Join(workdir, name+"-data.qcow2"))
	os.Remove(filepath.Join(workdir, name+"-seed.iso"))
	os.Remove(pidFile)
	os.Remove(fmt.Sprintf("/tmp/ch-%s.log", name))
	os.Remove(fmt.Sprintf("/tmp/ch-api-%s.sock", name))
}

func parseEnvFile(path string) (*VMInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	vm := &VMInfo{EnvFile: path}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "VM_NAME":
			vm.Name = v
		case "VM_IP":
			vm.IP = v
		case "VM_USER":
			vm.User = v
		}
	}
	if vm.Name == "" || vm.IP == "" {
		return nil, fmt.Errorf("env file %s missing VM_NAME or VM_IP", path)
	}
	if vm.User == "" {
		vm.User = "ubuntu"
	}
	return vm, nil
}

func NewPool(target int, maxIdle time.Duration, opsDir string, executor Executor) *Pool {
	p := &Pool{
		target:   target,
		maxIdle:  maxIdle,
		opsDir:   opsDir,
		slots:    make([]*Slot, target),
		stop:     make(chan struct{}),
		claimCh:  make(chan struct{}, target),
		executor: executor,
	}
	for i := range p.slots {
		p.slots[i] = &Slot{State: SlotCreating}
	}
	return p
}

// Start begins creating VMs and runs the background maintenance loop.
func (p *Pool) Start() {
	// Stagger initial VM creation to avoid thundering herd.
	for i := range p.slots {
		p.wg.Add(1)
		go func(idx int) {
			defer p.wg.Done()
			// Stagger by 2 seconds per slot.
			select {
			case <-time.After(time.Duration(idx) * 2 * time.Second):
			case <-p.stop:
				return
			}
			p.createSlot(idx)
		}(i)
	}

	// Background maintenance goroutine.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.maintain()
	}()
}

// Stop shuts down the pool, destroying all VMs.
func (p *Pool) Stop() {
	close(p.stop)
	p.wg.Wait()

	// Destroy all remaining VMs.
	p.mu.Lock()
	slots := make([]*Slot, len(p.slots))
	copy(slots, p.slots)
	p.mu.Unlock()

	for _, s := range slots {
		if s.VM != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := p.executor.DestroyVM(ctx, s.VM.EnvFile, p.opsDir); err != nil {
				slog.Error("destroy VM on shutdown", "vm", s.VM.Name, "err", err)
			}
			cancel()
		}
	}
}

// WaitReady blocks until at least one slot is in the ready state.
func (p *Pool) WaitReady(ctx context.Context) error {
	for {
		p.mu.Lock()
		for _, s := range p.slots {
			if s.State == SlotReady {
				p.mu.Unlock()
				return nil
			}
		}
		p.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.claimCh:
		}
	}
}

// Claim atomically claims a ready slot, returning the VM info.
// It blocks until a slot is available or the context is cancelled.
func (p *Pool) Claim(ctx context.Context, runID string) (*VMInfo, int, error) {
	for {
		p.mu.Lock()
		for i, s := range p.slots {
			if s.State == SlotReady {
				s.State = SlotClaimed
				s.ClaimedBy = runID
				vm := s.VM
				p.mu.Unlock()
				slog.InfoContext(ctx, "claimed slot", "slot", i, "vm", vm.Name, "run", runID)
				return vm, i, nil
			}
		}
		p.mu.Unlock()

		// Wait for a slot to become ready or context cancellation.
		select {
		case <-ctx.Done():
			return nil, 0, fmt.Errorf("waiting for VM: %w", ctx.Err())
		case <-p.claimCh:
			// A slot might be ready; loop back and check.
		}
	}
}

// Release marks a claimed slot for destruction and triggers replacement.
func (p *Pool) Release(slotIdx int) {
	p.mu.Lock()
	s := p.slots[slotIdx]
	if s.State != SlotClaimed {
		p.mu.Unlock()
		slog.Warn("release called on non-claimed slot", "slot", slotIdx, "state", s.State)
		return
	}
	vm := s.VM
	s.State = SlotDestroying
	s.VM = nil
	s.ClaimedBy = ""
	p.mu.Unlock()

	slog.Info("releasing slot", "slot", slotIdx, "vm", vm.Name)

	// Destroy and recreate in background.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.destroyAndRecreate(slotIdx, vm)
	}()
}

// Status returns a snapshot of the pool state.
func (p *Pool) Status() PoolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	var status PoolStatus
	status.Target = p.target
	for _, s := range p.slots {
		switch s.State {
		case SlotCreating:
			status.Creating++
		case SlotReady:
			status.Ready++
		case SlotClaimed:
			status.Claimed++
		case SlotDestroying:
			status.Destroying++
		}
	}
	return status
}

type PoolStatus struct {
	Target     int `json:"target"`
	Ready      int `json:"ready"`
	Creating   int `json:"creating"`
	Claimed    int `json:"claimed"`
	Destroying int `json:"destroying"`
}

func (p *Pool) createSlot(idx int) {
	select {
	case <-p.stop:
		return
	default:
	}

	name := fmt.Sprintf("e1ed-%d-%s", idx, time.Now().Format("20060102150405"))
	slog.Info("creating VM", "slot", idx, "name", name)

	// 15 minutes: first-time provisioning builds Cloud Hypervisor via Docker.
	// Subsequent runs use snapshot cache and complete in ~30s.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	vm, err := p.executor.StartVM(ctx, name, p.opsDir)
	if err != nil {
		slog.ErrorContext(ctx, "failed to create VM", "slot", idx, "name", name, "err", err)
		// Best-effort cleanup: the VM may exist even though the
		// script failed (e.g., timeout during provisioning).
		p.executor.DestroyVMByName(context.Background(), name, p.opsDir)
		// Retry after a delay.
		select {
		case <-time.After(10 * time.Second):
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.createSlot(idx)
			}()
		case <-p.stop:
		}
		return
	}

	p.mu.Lock()
	p.slots[idx].State = SlotReady
	p.slots[idx].VM = vm
	p.slots[idx].ReadyAt = time.Now()
	p.slots[idx].ClaimedBy = ""
	p.mu.Unlock()

	slog.InfoContext(ctx, "VM ready", "slot", idx, "name", vm.Name, "ip", vm.IP)

	// Signal that a slot is ready (non-blocking).
	select {
	case p.claimCh <- struct{}{}:
	default:
	}
}

func (p *Pool) destroyAndRecreate(idx int, vm *VMInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := p.executor.DestroyVM(ctx, vm.EnvFile, p.opsDir); err != nil {
		slog.ErrorContext(ctx, "failed to destroy VM", "slot", idx, "vm", vm.Name, "err", err)
	}

	p.mu.Lock()
	p.slots[idx].State = SlotCreating
	p.slots[idx].VM = nil
	p.mu.Unlock()

	p.createSlot(idx)
}

// Recycle destroys and recreates all ready (unclaimed) slots.
// Claimed slots are left alone; they will be recreated when released.
func (p *Pool) Recycle() int {
	p.mu.Lock()
	var toRecycle []int
	var vms []*VMInfo
	for i, s := range p.slots {
		if s.State == SlotReady {
			slog.Info("recycling VM", "slot", i, "vm", s.VM.Name)
			s.State = SlotDestroying
			toRecycle = append(toRecycle, i)
			vms = append(vms, s.VM)
			s.VM = nil
		}
	}
	p.mu.Unlock()

	for j, idx := range toRecycle {
		p.wg.Add(1)
		go func(i int, vm *VMInfo) {
			defer p.wg.Done()
			p.destroyAndRecreate(i, vm)
		}(idx, vms[j])
	}
	return len(toRecycle)
}

func (p *Pool) maintain() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.recycleStale()
		}
	}
}

func (p *Pool) recycleStale() {
	p.mu.Lock()
	var toRecycle []int
	var vms []*VMInfo
	for i, s := range p.slots {
		if s.State == SlotReady && time.Since(s.ReadyAt) > p.maxIdle {
			slog.Info("recycling stale VM", "slot", i, "vm", s.VM.Name, "age", time.Since(s.ReadyAt))
			s.State = SlotDestroying
			toRecycle = append(toRecycle, i)
			vms = append(vms, s.VM)
			s.VM = nil
		}
	}
	p.mu.Unlock()

	for j, idx := range toRecycle {
		p.wg.Add(1)
		go func(i int, vm *VMInfo) {
			defer p.wg.Done()
			p.destroyAndRecreate(i, vm)
		}(idx, vms[j])
	}
}
