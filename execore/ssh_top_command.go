package execore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"exe.dev/container"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	topPollInterval = 5 * time.Second
	topMaxDuration  = 10 * time.Minute
)

// vmUsageRow is one row in the top display, representing a single VM.
type vmUsageRow struct {
	Name       string
	Status     string
	CPUPercent float64
	MemBytes   uint64
	DiskBytes  uint64
	NetRx      uint64
	NetTx      uint64
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

	// fetchFunc fetches fresh VM usage rows. Injected for testability.
	fetchFunc func(ctx context.Context) ([]vmUsageRow, error)
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
		m.rows = msg.rows
		m.err = msg.err
		m.lastPoll = time.Now()
	}
	return m, nil
}

func (m *topModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Header
	elapsed := time.Since(m.startTime).Truncate(time.Second)
	remaining := (topMaxDuration - elapsed).Truncate(time.Second)
	if remaining < 0 {
		remaining = 0
	}
	b.WriteString(fmt.Sprintf("\033[1mexe top\033[0m  uptime %s  (auto-quit in %s)  press q to exit\n", elapsed, remaining))

	if m.err != nil {
		b.WriteString(fmt.Sprintf("\033[1;31merror: %v\033[0m\n", m.err))
		return b.String()
	}

	if len(m.rows) == 0 {
		b.WriteString("\033[2mno running VMs\033[0m\n")
		return b.String()
	}

	// Column header
	b.WriteString(fmt.Sprintf("\033[1;37m%-20s %-10s %8s %10s %10s %10s %10s\033[0m\n",
		"VM", "STATUS", "CPU%", "MEM", "DISK", "NET RX", "NET TX"))

	for _, row := range m.rows {
		// VM name
		name := row.Name
		if len(name) > 19 {
			name = name[:19] + "…"
		}

		// Status with color
		statusStr := colorizeStatus(row.Status)

		// CPU% with color gradient
		cpuStr := colorizeCPU(row.CPUPercent)

		// Memory
		memStr := colorizeMemory(row.MemBytes)

		b.WriteString(fmt.Sprintf("%-20s %-21s %8s %10s %10s %10s %10s\n",
			name, statusStr, cpuStr,
			memStr,
			topFmtBytes(row.DiskBytes),
			topFmtBytes(row.NetRx),
			topFmtBytes(row.NetTx)))
	}

	return b.String()
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
func (ss *SSHServer) fetchVMUsageForUser(ctx context.Context, userID string) ([]vmUsageRow, error) {
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

			stream, err := ec.client.ListVMUsage(listCtx, &resourceapi.ListVMUsageRequest{})
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
					row.DiskBytes = u.DiskBytes
					row.NetRx = u.NetRxBytes
					row.NetTx = u.NetTxBytes
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

func (ss *SSHServer) handleTopCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if cc.SSHSession == nil {
		return fmt.Errorf("top requires a terminal session")
	}

	width, height := 80, 24
	if pty, ok := cc.SSHSession.Pty(); ok {
		if pty.Window.Width > 0 {
			width = pty.Window.Width
		}
		if pty.Window.Height > 0 {
			height = pty.Window.Height
		}
	}

	userID := cc.User.ID
	model := &topModel{
		width:     width,
		height:    height,
		startTime: time.Now(),
		fetchFunc: func(ctx context.Context) ([]vmUsageRow, error) {
			return ss.fetchVMUsageForUser(ctx, userID)
		},
	}

	input := &gameSessionInput{session: cc.SSHSession}

	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(cc.SSHSession),
		tea.WithAltScreen(),
	)

	if _, err := program.Run(); err != nil {
		return err
	}

	fmt.Fprint(cc.Output, "\r\n")
	return nil
}
