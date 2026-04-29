package execore

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"exe.dev/container"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const (
	topPollInterval = 5 * time.Second
	topMaxDuration  = 10 * time.Minute
)

// vmUsageRow is one row in the top display, representing a single VM.
type vmUsageRow struct {
	Name             string
	Status           string
	CPUPercent       float64 // 100% = 1 core
	MemBytes         uint64  // cgroup memory.current (total charged — includes host page cache)
	SwapBytes        uint64
	DiskBytes        uint64 // compressed on-disk usage (ZFS used)
	DiskLogicalBytes uint64 // uncompressed logical usage (ZFS logicalused, matches df -h)
	DiskCapacity     uint64 // provisioned size (ZFS volsize)
	MemCapacity      uint64 // allocated memory from VM config
	CPUs             uint64 // allocated vCPUs from VM config
	NetRx            uint64 // cumulative bytes
	NetTx            uint64 // cumulative bytes

	// Cgroup memory.stat breakdown. Older exelets leave these zero.
	MemAnonBytes         uint64 // anonymous memory (closest proxy to VM working set)
	MemFileBytes         uint64 // page cache (reclaimable)
	MemKernelBytes       uint64
	MemShmemBytes        uint64
	MemSlabBytes         uint64
	MemInactiveFileBytes uint64

	// Filesystem-level (ext4) view, populated only when the caller is
	// allowed to request it (per-stage gate plus the ext4-allow-list)
	// and the exelet returns non-zero data.
	FsTotalBytes     uint64
	FsFreeBytes      uint64
	FsAvailableBytes uint64
}

// DisplayMemBytes returns the user-facing memory figure: cgroup memory.current
// minus the host page cache attributed to the VM's disk I/O. The page cache
// (memory.stat "file") is reclaimable and not part of the guest working set,
// so charging it to the VM dramatically overstates real usage. Older exelets
// don't report MemFileBytes (it's zero), in which case this falls back to
// MemBytes.
func (r vmUsageRow) DisplayMemBytes() uint64 {
	if r.MemFileBytes >= r.MemBytes {
		return 0
	}
	return r.MemBytes - r.MemFileBytes
}

// sortColumn enumerates the columns the user can sort by via 's'.
type sortColumn int

const (
	sortCPU sortColumn = iota
	sortMem
	sortSwap
	sortRAM
	sortDisk
	sortNetRx
	sortNetTx
	sortName
	sortColumnCount
)

func (s sortColumn) header() string {
	switch s {
	case sortCPU:
		return "CPU%"
	case sortMem:
		return "MEM"
	case sortSwap:
		return "SWAP"
	case sortRAM:
		return "RAM"
	case sortDisk:
		return "DISK"
	case sortNetRx:
		return "NET RX"
	case sortNetTx:
		return "NET TX"
	case sortName:
		return "VM"
	}
	return ""
}

// topModel is the bubbletea model for the "top" command.
type topModel struct {
	rows      []vmUsageRow
	err       error
	width     int
	height    int
	quitting  bool
	startTime time.Time
	lastPoll  time.Time
	sortBy    sortColumn

	// Previous poll data for computing network rates.
	prevRows map[string]vmUsageRow
	prevTime time.Time

	// Computed network rates (bytes/sec) keyed by VM name.
	netRxRate map[string]float64
	netTxRate map[string]float64

	// fetchFunc fetches fresh VM usage rows. Injected for testability.
	fetchFunc func(ctx context.Context) ([]vmUsageRow, error)

	// showFsUsage adds a USED/CAP disk column when true; only set when
	// the caller is allowed to see ext4 superblock-derived numbers.
	showFsUsage bool
}

type (
	topTickMsg struct{}
	usageMsg   struct {
		rows []vmUsageRow
		err  error
	}
)

func topTickCmd() tea.Cmd {
	return tea.Tick(topPollInterval, func(t time.Time) tea.Msg {
		return topTickMsg{}
	})
}

func fetchUsageCmd(fetchFunc func(ctx context.Context) ([]vmUsageRow, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := fetchFunc(ctx)
		return usageMsg{rows: rows, err: err}
	}
}

func (m *topModel) Init() tea.Cmd {
	return tea.Batch(topTickCmd(), fetchUsageCmd(m.fetchFunc))
}

func (m *topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "s":
			m.sortBy = (m.sortBy + 1) % sortColumnCount
			m.sortRows()
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case topTickMsg:
		if time.Since(m.startTime) >= topMaxDuration {
			m.quitting = true
			return m, tea.Quit
		}
		return m, tea.Batch(topTickCmd(), fetchUsageCmd(m.fetchFunc))
	case usageMsg:
		now := time.Now()
		if msg.err == nil && m.prevRows != nil && !m.prevTime.IsZero() {
			elapsed := now.Sub(m.prevTime).Seconds()
			if elapsed > 0 {
				if m.netRxRate == nil {
					m.netRxRate = make(map[string]float64)
					m.netTxRate = make(map[string]float64)
				}
				for _, row := range msg.rows {
					if prev, ok := m.prevRows[row.Name]; ok {
						if row.NetRx >= prev.NetRx {
							m.netRxRate[row.Name] = float64(row.NetRx-prev.NetRx) / elapsed
						}
						if row.NetTx >= prev.NetTx {
							m.netTxRate[row.Name] = float64(row.NetTx-prev.NetTx) / elapsed
						}
					}
				}
			}
		}
		// Store current rows for next delta.
		if msg.err == nil {
			prev := make(map[string]vmUsageRow, len(msg.rows))
			for _, row := range msg.rows {
				prev[row.Name] = row
			}
			m.prevRows = prev
			m.prevTime = now
		}
		m.rows = msg.rows
		m.err = msg.err
		m.lastPoll = now
		m.sortRows()
	}
	return m, nil
}

// sortRows sorts m.rows: running VMs first, then by the active sort column.
func (m *topModel) sortRows() {
	sort.SliceStable(m.rows, func(i, j int) bool {
		ri := m.rows[i].Status == string(container.StatusRunning)
		rj := m.rows[j].Status == string(container.StatusRunning)
		if ri != rj {
			return ri
		}
		a, b := m.rows[i], m.rows[j]
		switch m.sortBy {
		case sortCPU:
			if a.CPUPercent != b.CPUPercent {
				return a.CPUPercent > b.CPUPercent
			}
		case sortMem:
			if a.DisplayMemBytes() != b.DisplayMemBytes() {
				return a.DisplayMemBytes() > b.DisplayMemBytes()
			}
		case sortSwap:
			if a.SwapBytes != b.SwapBytes {
				return a.SwapBytes > b.SwapBytes
			}
		case sortRAM:
			if a.MemCapacity != b.MemCapacity {
				return a.MemCapacity > b.MemCapacity
			}
		case sortDisk:
			if a.DiskCapacity != b.DiskCapacity {
				return a.DiskCapacity > b.DiskCapacity
			}
		case sortNetRx:
			ar := m.netRxRate[a.Name]
			br := m.netRxRate[b.Name]
			if ar != br {
				return ar > br
			}
		case sortNetTx:
			at := m.netTxRate[a.Name]
			bt := m.netTxRate[b.Name]
			if at != bt {
				return at > bt
			}
		}
		return a.Name < b.Name
	})
}

func (m *topModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Header line
	elapsed := time.Since(m.startTime).Truncate(time.Second)
	remaining := (topMaxDuration - elapsed).Truncate(time.Second)
	if remaining < 0 {
		remaining = 0
	}
	// Compose two header variants; pick the widest that fits.
	hdrFull := fmt.Sprintf("\033[1mexe top\033[0m  uptime %s  (auto-quit in %s)  sort:%s  [s] cycle sort  [q] quit", elapsed, remaining, m.sortBy.header())
	hdrShort := fmt.Sprintf("\033[1mexe top\033[0m  up %s  sort:%s  [s] [q]", elapsed, m.sortBy.header())
	if m.width > 0 && ansi.StringWidth(hdrFull) > m.width {
		b.WriteString(hdrShort)
	} else {
		b.WriteString(hdrFull)
	}
	b.WriteString("\n")

	if m.err != nil {
		b.WriteString(fmt.Sprintf("\033[1;31merror: %v\033[0m\n", m.err))
		return truncateViewLines(b.String(), m.width, m.height)
	}

	if len(m.rows) == 0 {
		b.WriteString("\033[2mno running VMs\033[0m\n")
		return truncateViewLines(b.String(), m.width, m.height)
	}

	// Column header. ANSI-aware padding keeps columns aligned
	// regardless of color escape code lengths.
	// CPU% = percent of one core (200% = 2 cores fully used).
	// MEM  = active (anon+kernel) guest memory, swap excluded.
	// SWAP = bytes the guest has paged out via host swap.
	// RAM  = allocated memory configured for the VM.
	// DISK = provisioned (allocated) disk capacity for the VM.
	hdr := func(label string, width int, col sortColumn, leftAlign bool) {
		text := label
		if m.sortBy == col {
			text = label + "\u25BC"
		}
		if leftAlign {
			b.WriteString(ansiPadRight(text, width))
		} else {
			b.WriteString(ansiPadLeft(text, width))
		}
	}
	b.WriteString("\033[1;37m")
	hdr("VM", 20, sortName, true)
	b.WriteString(" ")
	b.WriteString(ansiPadRight("STATUS", 10))
	b.WriteString(" ")
	hdr("CPU%", 8, sortCPU, false)
	b.WriteString(" ")
	hdr("MEM", 9, sortMem, false)
	b.WriteString(" ")
	hdr("SWAP", 8, sortSwap, false)
	b.WriteString(" ")
	hdr("RAM", 8, sortRAM, false)
	b.WriteString(" ")
	if m.showFsUsage {
		hdr("DISK", 17, sortDisk, false)
	} else {
		hdr("DISK", 8, sortDisk, false)
	}
	b.WriteString(" ")
	hdr("NET RX", 10, sortNetRx, false)
	b.WriteString(" ")
	hdr("NET TX", 10, sortNetTx, false)
	b.WriteString("\033[0m\n")

	// Limit visible rows to fit the terminal. Reserve 2 lines for the
	// header + column header, plus 1 for an optional truncation note.
	visibleRows := m.rows
	maxRows := m.height - 3
	truncated := 0
	if m.height > 0 && maxRows > 0 && len(visibleRows) > maxRows {
		truncated = len(visibleRows) - maxRows
		visibleRows = visibleRows[:maxRows]
	}

	for _, row := range visibleRows {
		name := row.Name
		if len(name) > 19 {
			name = name[:19] + "…"
		}

		statusStr := colorizeStatus(row.Status)
		cpuStr := colorizeCPU(row.CPUPercent)
		memStr := colorizeMemory(row.DisplayMemBytes())
		swapStr := topFmtBytes(row.SwapBytes)
		ramStr := topFmtBytes(row.MemCapacity)
		diskStr := topFmtBytes(row.DiskCapacity)
		if m.showFsUsage {
			// "USED/CAP" view: 8 chars used + "/" + 8 chars capacity = 17.
			used := "-"
			if row.FsTotalBytes > 0 {
				used = topFmtBytes(row.FsTotalBytes - row.FsAvailableBytes)
			}
			diskStr = ansiPadLeft(used, 8) + "/" + ansiPadLeft(topFmtBytes(row.DiskCapacity), 8)
		}

		// Network: rates in Mbps (bits per second).
		var rxStr, txStr string
		if m.netRxRate != nil {
			rxStr = fmtNetRate(m.netRxRate[row.Name])
			txStr = fmtNetRate(m.netTxRate[row.Name])
		} else {
			rxStr = "-"
			txStr = "-"
		}

		b.WriteString(ansiPadRight(name, 20))
		b.WriteString(" ")
		b.WriteString(ansiPadRight(statusStr, 10))
		b.WriteString(" ")
		b.WriteString(ansiPadLeft(cpuStr, 8))
		b.WriteString(" ")
		b.WriteString(ansiPadLeft(memStr, 9))
		b.WriteString(" ")
		b.WriteString(ansiPadLeft(swapStr, 8))
		b.WriteString(" ")
		b.WriteString(ansiPadLeft(ramStr, 8))
		b.WriteString(" ")
		diskWidth := 8
		if m.showFsUsage {
			diskWidth = 17
		}
		b.WriteString(ansiPadLeft(diskStr, diskWidth))
		b.WriteString(" ")
		b.WriteString(ansiPadLeft(rxStr, 10))
		b.WriteString(" ")
		b.WriteString(ansiPadLeft(txStr, 10))
		b.WriteString("\n")
	}

	if truncated > 0 {
		b.WriteString(fmt.Sprintf("\033[2m… %d more not shown (resize terminal to see all)\033[0m\n", truncated))
	}

	return truncateViewLines(b.String(), m.width, m.height)
}

// truncateViewLines clips each line of s to width visible columns and the
// total number of lines to height. This guarantees the View output never
// wraps or overflows the terminal viewport, which would otherwise scroll
// the header off-screen under tea.WithAltScreen.
func truncateViewLines(s string, width, height int) string {
	if width <= 0 && height <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	// strings.Split on "a\nb\n" yields ["a","b",""]; preserve trailing newline.
	trailingNL := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailingNL {
		lines = lines[:len(lines)-1]
	}
	if width > 0 {
		for i, ln := range lines {
			if ansi.StringWidth(ln) > width {
				lines[i] = ansi.Truncate(ln, width, "")
			}
		}
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
		trailingNL = false // avoid extra blank line that would push content up
	}
	out := strings.Join(lines, "\n")
	if trailingNL {
		out += "\n"
	}
	return out
}

// ansiPadRight pads s on the right to width visible columns.
func ansiPadRight(s string, width int) string {
	visible := ansi.StringWidth(s)
	if visible >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visible)
}

// ansiPadLeft pads s on the left to width visible columns.
func ansiPadLeft(s string, width int) string {
	visible := ansi.StringWidth(s)
	if visible >= width {
		return s
	}
	return strings.Repeat(" ", width-visible) + s
}

// fmtNetRate formats a bytes-per-second rate as a human-readable bit rate.
func fmtNetRate(bytesPerSec float64) string {
	bitsPerSec := bytesPerSec * 8
	switch {
	case bitsPerSec >= 1_000_000_000:
		return fmt.Sprintf("%.1f Gbps", bitsPerSec/1_000_000_000)
	case bitsPerSec >= 1_000_000:
		return fmt.Sprintf("%.1f Mbps", bitsPerSec/1_000_000)
	case bitsPerSec >= 1_000:
		return fmt.Sprintf("%.0f Kbps", bitsPerSec/1_000)
	case bitsPerSec == 0:
		return "-"
	default:
		return fmt.Sprintf("%.0f bps", bitsPerSec)
	}
}

// colorizeStatus returns a colored status string.
func colorizeStatus(status string) string {
	switch container.ContainerStatus(status) {
	case container.StatusRunning:
		return "\033[1;32m" + status + "\033[0m"
	case container.StatusStopped:
		return "\033[2m" + status + "\033[0m"
	case container.StatusFailed:
		return "\033[1;31m" + status + "\033[0m"
	case container.StatusBuilding, container.StatusPending:
		return "\033[1;33m" + status + "\033[0m"
	default:
		return status
	}
}

// colorizeCPU returns a colored CPU percentage string.
func colorizeCPU(pct float64) string {
	s := fmt.Sprintf("%.1f%%", pct)
	switch {
	case pct >= 90:
		return "\033[1;31m" + s + "\033[0m" // bright red
	case pct >= 70:
		return "\033[31m" + s + "\033[0m" // red
	case pct >= 50:
		return "\033[33m" + s + "\033[0m" // yellow
	case pct >= 20:
		return "\033[32m" + s + "\033[0m" // green
	default:
		return "\033[2m" + s + "\033[0m" // dim
	}
}

// colorizeMemory returns a colored memory usage string.
func colorizeMemory(bytes uint64) string {
	s := topFmtBytes(bytes)
	switch {
	case bytes >= 4*1024*1024*1024: // >= 4 GiB
		return "\033[1;31m" + s + "\033[0m"
	case bytes >= 2*1024*1024*1024: // >= 2 GiB
		return "\033[33m" + s + "\033[0m"
	case bytes >= 512*1024*1024: // >= 512 MiB
		return "\033[32m" + s + "\033[0m"
	default:
		return "\033[2m" + s + "\033[0m"
	}
}

// topFmtBytes formats bytes compactly for the top display.
func topFmtBytes(b uint64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1fG", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.0fM", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.0fK", float64(b)/1024)
	case b == 0:
		return "-"
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// fetchVMUsageForUser queries all exelets for usage of the user's VMs.
// When collectFs is true, the ListVMUsage call requests the exelet read
// the ext4 superblock; the exelet still applies its own gate.
func (ss *SSHServer) fetchVMUsageForUser(ctx context.Context, userID string, collectFs bool) ([]vmUsageRow, error) {
	// Get user's boxes from DB.
	boxes, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		return nil, fmt.Errorf("listing VMs: %w", err)
	}

	// Build a map of container_id -> box for quick lookups, and group by ctrhost.
	type boxInfo struct {
		name   string
		status string
	}
	ctrhostBoxes := make(map[string]map[string]boxInfo) // ctrhost -> container_id -> boxInfo
	for _, box := range boxes {
		if box.ContainerID == nil || box.Ctrhost == "" {
			continue
		}
		if ctrhostBoxes[box.Ctrhost] == nil {
			ctrhostBoxes[box.Ctrhost] = make(map[string]boxInfo)
		}
		ctrhostBoxes[box.Ctrhost][*box.ContainerID] = boxInfo{
			name:   box.Name,
			status: box.Status,
		}
	}

	// Query exelets in parallel for VM usage.
	var mu sync.Mutex
	var wg sync.WaitGroup
	var allRows []vmUsageRow

	for ctrhostAddr, boxMap := range ctrhostBoxes {
		ec := ss.server.getExeletClient(ctrhostAddr)
		if ec == nil {
			// No client for this host; fill in rows with DB-only info.
			for _, info := range boxMap {
				mu.Lock()
				allRows = append(allRows, vmUsageRow{
					Name:   info.name,
					Status: info.status,
				})
				mu.Unlock()
			}
			continue
		}

		wg.Add(1)
		go func(ec *exeletClient, boxMap map[string]boxInfo) {
			defer wg.Done()

			// Build a set of container IDs we care about.
			wantIDs := make(map[string]bool, len(boxMap))
			for cid := range boxMap {
				wantIDs[cid] = true
			}

			// Use ListVMUsage to get all usages from this exelet, then filter.
			listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			stream, err := ec.client.ListVMUsage(listCtx, &resourceapi.ListVMUsageRequest{
				CollectFilesystemUsage: collectFs,
			})
			usageByID := make(map[string]*resourceapi.VMUsage)
			if err == nil {
				for {
					resp, err := stream.Recv()
					if err != nil {
						break
					}
					if resp.Usage != nil && wantIDs[resp.Usage.ID] {
						usageByID[resp.Usage.ID] = resp.Usage
					}
				}
			}

			mu.Lock()
			defer mu.Unlock()
			for cid, info := range boxMap {
				row := vmUsageRow{
					Name:   info.name,
					Status: info.status,
				}
				if u, ok := usageByID[cid]; ok {
					row.CPUPercent = u.CpuPercent
					row.MemBytes = u.MemoryBytes
					row.SwapBytes = u.SwapBytes
					row.DiskBytes = u.DiskBytes
					row.DiskLogicalBytes = u.DiskLogicalBytes
					row.DiskCapacity = u.DiskCapacityBytes
					row.MemCapacity = u.MemCapacityBytes
					row.CPUs = u.CPUs
					row.NetRx = u.NetRxBytes
					row.NetTx = u.NetTxBytes
					row.MemAnonBytes = u.MemoryAnonBytes
					row.MemFileBytes = u.MemoryFileBytes
					row.MemKernelBytes = u.MemoryKernelBytes
					row.MemShmemBytes = u.MemoryShmemBytes
					row.MemSlabBytes = u.MemorySlabBytes
					row.MemInactiveFileBytes = u.MemoryInactiveFileBytes
					row.FsTotalBytes = u.FsTotalBytes
					row.FsFreeBytes = u.FsFreeBytes
					row.FsAvailableBytes = u.FsAvailableBytes
				}
				allRows = append(allRows, row)
			}
		}(ec, boxMap)
	}

	wg.Wait()

	// Sort: running first (sorted by CPU desc), then stopped.
	sort.Slice(allRows, func(i, j int) bool {
		ri := allRows[i].Status == string(container.StatusRunning)
		rj := allRows[j].Status == string(container.StatusRunning)
		if ri != rj {
			return ri
		}
		if ri && rj {
			return allRows[i].CPUPercent > allRows[j].CPUPercent
		}
		return allRows[i].Name < allRows[j].Name
	})

	return allRows, nil
}

// topSessionInput reads one byte at a time from the SSH session.
// Bubble Tea's alt-screen mode requires single-byte reads to correctly
// parse key events delivered over the SSH channel.
type topSessionInput struct {
	session  io.Reader
	quitSeen bool
}

func (t *topSessionInput) Read(p []byte) (int, error) {
	if t.quitSeen {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	var buf [1]byte
	_, err := t.session.Read(buf[:])
	if err != nil {
		return 0, err
	}
	p[0] = buf[0]
	if buf[0] == 3 { // ctrl-c
		t.quitSeen = true
	}
	return 1, nil
}

func (ss *SSHServer) handleTopCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	userID := cc.User.ID

	// Whether to ask the exelet for ext4 filesystem usage. The exelet
	// will refuse if it doesn't trust this user/stage; we mirror that
	// gate here so the request flag isn't a runtime no-op (and so we
	// know whether to show the extra columns).
	collectFs := ss.server.env.CollectExt4Usage || slices.Contains(ss.server.env.ExtraExt4UsageGroupIDs, userID)

	// Scripted (one-shot / fixed-iteration) mode. -n > 0 or --json runs
	// the fetch loop once-or-N-times and prints rows, no Bubble Tea UI.
	// This is what scripts and tests use; it does not require a PTY.
	nIter, _ := strconv.Atoi(cc.FlagSet.Lookup("n").Value.String())
	wantJSON := cc.WantJSON()
	if nIter > 0 || wantJSON {
		if nIter <= 0 {
			nIter = 1
		}
		interval, err := time.ParseDuration(cc.FlagSet.Lookup("interval").Value.String())
		if err != nil || interval <= 0 {
			interval = topPollInterval
		}
		return ss.runTopOneShot(ctx, cc, userID, collectFs, nIter, interval, wantJSON)
	}

	// Interactive Bubble Tea UI requires a PTY.
	if cc.SSHSession == nil {
		return cc.Errorf("interactive top requires an SSH session; pass -n N or --json for scripted output")
	}
	if _, ok := cc.SSHSession.Pty(); !ok {
		return cc.Errorf("interactive top requires a PTY (`ssh -t`); pass -n N or --json for scripted output")
	}

	width, height := cc.PtySize()
	model := &topModel{
		width:       width,
		height:      height,
		startTime:   time.Now(),
		showFsUsage: collectFs,
		fetchFunc: func(ctx context.Context) ([]vmUsageRow, error) {
			return ss.fetchVMUsageForUser(ctx, userID, collectFs)
		},
	}

	// Tag-scoped keys: filter to VMs with the required tag.
	if perms := getSSHKeyPerms(ctx); perms != nil && perms.Tag != "" {
		requiredTag := perms.Tag
		inner := model.fetchFunc
		model.fetchFunc = func(ctx context.Context) ([]vmUsageRow, error) {
			rows, err := inner(ctx)
			if err != nil {
				return nil, err
			}
			// Look up tags for each VM to filter.
			var filtered []vmUsageRow
			for _, r := range rows {
				box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
					Name:            r.Name,
					CreatedByUserID: userID,
				})
				if err != nil {
					continue
				}
				if slices.Contains(box.GetTags(), requiredTag) {
					filtered = append(filtered, r)
				}
			}
			return filtered, nil
		}
	}

	input := &topSessionInput{session: cc.SSHSession}

	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(cc.SSHSession),
		tea.WithAltScreen(),
	)

	if _, err := program.Run(); err != nil {
		return err
	}

	cc.Write("\n")
	return nil
}

// topJSONRow is the JSON shape emitted by `top -n N --json` and
// `top --json`. Keep these field names stable: scripts depend on them.
type topJSONRow struct {
	Name             string  `json:"name"`
	Status           string  `json:"status"`
	CPUPercent       float64 `json:"cpu_percent"`
	CPUs             uint64  `json:"cpus"`
	MemBytes         uint64  `json:"memory_bytes"`
	MemDisplayBytes  uint64  `json:"memory_display_bytes"`
	MemAnonBytes     uint64  `json:"memory_anon_bytes"`
	MemFileBytes     uint64  `json:"memory_file_bytes"`
	MemKernelBytes   uint64  `json:"memory_kernel_bytes"`
	SwapBytes        uint64  `json:"swap_bytes"`
	MemCapacity      uint64  `json:"memory_capacity_bytes"`
	DiskBytes        uint64  `json:"disk_bytes"`
	DiskLogicalBytes uint64  `json:"disk_logical_bytes"`
	DiskCapacity     uint64  `json:"disk_capacity_bytes"`
	NetRx            uint64  `json:"net_rx_bytes"`
	NetTx            uint64  `json:"net_tx_bytes"`
	NetRxRate        float64 `json:"net_rx_bytes_per_sec,omitempty"`
	NetTxRate        float64 `json:"net_tx_bytes_per_sec,omitempty"`
	FsTotalBytes     uint64  `json:"fs_total_bytes,omitempty"`
	FsFreeBytes      uint64  `json:"fs_free_bytes,omitempty"`
	FsAvailableBytes uint64  `json:"fs_available_bytes,omitempty"`
}

func rowToJSON(r vmUsageRow, rxRate, txRate float64) topJSONRow {
	return topJSONRow{
		Name:             r.Name,
		Status:           r.Status,
		CPUPercent:       r.CPUPercent,
		CPUs:             r.CPUs,
		MemBytes:         r.MemBytes,
		MemDisplayBytes:  r.DisplayMemBytes(),
		MemAnonBytes:     r.MemAnonBytes,
		MemFileBytes:     r.MemFileBytes,
		MemKernelBytes:   r.MemKernelBytes,
		SwapBytes:        r.SwapBytes,
		MemCapacity:      r.MemCapacity,
		DiskBytes:        r.DiskBytes,
		DiskLogicalBytes: r.DiskLogicalBytes,
		DiskCapacity:     r.DiskCapacity,
		NetRx:            r.NetRx,
		NetTx:            r.NetTx,
		NetRxRate:        rxRate,
		NetTxRate:        txRate,
		FsTotalBytes:     r.FsTotalBytes,
		FsFreeBytes:      r.FsFreeBytes,
		FsAvailableBytes: r.FsAvailableBytes,
	}
}

// runTopOneShot executes the top fetch loop n times (n>=1), printing
// results without the Bubble Tea UI. It's used by `top -n N` and
// `top --json`. It runs against any session — PTY or scripted — and
// is the path that scripts and e1e tests exercise.
//
// CPU%: the exelet computes CPU% from the delta between successive
// poll intervals it does internally, so even with n=1 we get a real
// number (not zero) as long as the resource manager has done at
// least one poll. Network rates, however, are computed by `top` from
// the difference between consecutive iterations; with n=1 they're
// reported as 0. n>=2 yields meaningful rates.
func (ss *SSHServer) runTopOneShot(ctx context.Context, cc *exemenu.CommandContext, userID string, collectFs bool, n int, interval time.Duration, wantJSON bool) error {
	var prev map[string]vmUsageRow
	var prevTime time.Time

	for i := 0; i < n; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
		rows, err := ss.fetchVMUsageForUser(ctx, userID, collectFs)
		if err != nil {
			return err
		}
		now := time.Now()
		rxRates := make(map[string]float64, len(rows))
		txRates := make(map[string]float64, len(rows))
		if prev != nil {
			elapsed := now.Sub(prevTime).Seconds()
			if elapsed > 0 {
				for _, r := range rows {
					if p, ok := prev[r.Name]; ok {
						if r.NetRx >= p.NetRx {
							rxRates[r.Name] = float64(r.NetRx-p.NetRx) / elapsed
						}
						if r.NetTx >= p.NetTx {
							txRates[r.Name] = float64(r.NetTx-p.NetTx) / elapsed
						}
					}
				}
			}
		}
		prev = make(map[string]vmUsageRow, len(rows))
		for _, r := range rows {
			prev[r.Name] = r
		}
		prevTime = now

		// Only print on the last iteration: scripts asked for one
		// snapshot, and earlier iterations exist only so we have a
		// previous-poll datum for accurate net rates.
		if i < n-1 {
			continue
		}

		if wantJSON {
			out := struct {
				Iterations int          `json:"iterations"`
				Interval   string       `json:"interval"`
				ShowFs     bool         `json:"show_fs_usage"`
				VMs        []topJSONRow `json:"vms"`
			}{
				Iterations: n,
				Interval:   interval.String(),
				ShowFs:     collectFs,
				VMs:        make([]topJSONRow, 0, len(rows)),
			}
			for _, r := range rows {
				out.VMs = append(out.VMs, rowToJSON(r, rxRates[r.Name], txRates[r.Name]))
			}
			cc.WriteJSON(out)
			continue
		}

		// Plain text scripted output: one row per VM, columns aligned.
		if len(rows) == 0 {
			cc.Writeln("no VMs")
			continue
		}
		header := "VM\tSTATUS\tCPU%\tMEM\tSWAP\tRAM\tDISK\tNET RX/s\tNET TX/s"
		if collectFs {
			header += "\tFS USED\tFS TOTAL"
		}
		cc.Writeln("%s", header)
		for _, r := range rows {
			line := fmt.Sprintf("%s\t%s\t%.1f\t%s\t%s\t%s\t%s\t%s\t%s",
				r.Name, r.Status, r.CPUPercent,
				topFmtBytes(r.DisplayMemBytes()),
				topFmtBytes(r.SwapBytes),
				topFmtBytes(r.MemCapacity),
				topFmtBytes(r.DiskCapacity),
				fmtNetRate(rxRates[r.Name]), fmtNetRate(txRates[r.Name]),
			)
			if collectFs {
				used := "-"
				if r.FsTotalBytes > 0 {
					used = topFmtBytes(r.FsTotalBytes - r.FsAvailableBytes)
				}
				line += fmt.Sprintf("\t%s\t%s", used, topFmtBytes(r.FsTotalBytes))
			}
			cc.Writeln("%s", line)
		}
	}
	return nil
}
