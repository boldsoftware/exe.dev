package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	failureStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	progressFullStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("42"))

	progressEmptyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("238"))
)

type tickMsg time.Time

type doneMsg struct{}

type dashboardModel struct {
	migrator  *Migrator
	collector *ReportCollector
	startTime time.Time
	done      bool
	width     int
	height    int
}

func newDashboardModel(migrator *Migrator, collector *ReportCollector) dashboardModel {
	return dashboardModel{
		migrator:  migrator,
		collector: collector,
		startTime: time.Now(),
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (d dashboardModel) Init() tea.Cmd {
	return tickCmd()
}

func (d dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return d, tea.Quit
		}
	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.height = msg.Height
	case tickMsg:
		return d, tickCmd()
	case doneMsg:
		d.done = true
		return d, tea.Quit
	}
	return d, nil
}

func (d dashboardModel) View() string {
	var b strings.Builder

	elapsed := time.Since(d.startTime).Truncate(time.Second)
	total, successes, failures, skipped := d.collector.Counts()
	avgDur := d.collector.AvgDuration().Truncate(time.Millisecond)

	// Title
	b.WriteString(titleStyle.Render("Storage Tier Migration Tool"))
	b.WriteString("\n")

	// Summary bar
	b.WriteString(headerStyle.Render("Summary"))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  Elapsed: %s  ", elapsed))
	b.WriteString(fmt.Sprintf("Total: %d  ", total))
	b.WriteString(successStyle.Render(fmt.Sprintf("OK: %d", successes)))
	b.WriteString("  ")
	b.WriteString(failureStyle.Render(fmt.Sprintf("Fail: %d", failures)))
	if skipped > 0 {
		b.WriteString("  ")
		b.WriteString(dimStyle.Render(fmt.Sprintf("Skip: %d", skipped)))
	}
	if successes > 0 {
		b.WriteString(fmt.Sprintf("  Avg: %s", avgDur))
	}
	b.WriteString("\n\n")

	// Active operations
	activeOps := d.migrator.ActiveOps()
	b.WriteString(headerStyle.Render(fmt.Sprintf("Active Operations (%d)", len(activeOps))))
	b.WriteString("\n")
	if len(activeOps) == 0 {
		b.WriteString(dimStyle.Render("  (none)"))
		b.WriteString("\n")
	} else {
		b.WriteString(fmt.Sprintf("  %-20s %-10s %-10s %-12s %s\n",
			"INSTANCE", "FROM", "TO", "PROGRESS", "ELAPSED"))
		for _, op := range activeOps {
			elapsed := time.Since(op.StartedAt).Truncate(time.Second)
			progress := renderProgressBar(op.Progress, 20)
			pct := fmt.Sprintf("%.0f%%", op.Progress*100)
			b.WriteString(fmt.Sprintf("  %-20s %-10s %-10s %s %-5s %s\n",
				truncate(op.InstanceID, 20),
				op.SourcePool,
				op.TargetPool,
				progress,
				pct,
				elapsed))
		}
	}
	b.WriteString("\n")

	// Recent completions
	recent := d.collector.RecentCompleted(10)
	b.WriteString(headerStyle.Render(fmt.Sprintf("Recent Migrations (%d total)", total)))
	b.WriteString("\n")
	if len(recent) == 0 {
		b.WriteString(dimStyle.Render("  (none yet)"))
		b.WriteString("\n")
	} else {
		b.WriteString(fmt.Sprintf("  %-20s %-10s %-10s %-10s %-12s %s\n",
			"INSTANCE", "FROM", "TO", "STATUS", "DURATION", "EXELET"))
		for _, r := range recent {
			status := successStyle.Render("OK")
			if r.State != "completed" {
				status = failureStyle.Render(r.State)
			}
			b.WriteString(fmt.Sprintf("  %-20s %-10s %-10s %-10s %-12s %s\n",
				truncate(r.InstanceID, 20),
				r.SourcePool,
				r.TargetPool,
				status,
				r.DurationStr,
				r.Exelet))
		}
	}

	if d.done {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Migration complete. Press q to exit."))
		b.WriteString("\n")
	}

	return b.String()
}

func renderProgressBar(progress float32, width int) string {
	filled := int(progress * float32(width))
	if filled > width {
		filled = width
	}
	empty := width - filled
	bar := progressFullStyle.Render(strings.Repeat("█", filled)) +
		progressEmptyStyle.Render(strings.Repeat("░", empty))
	return bar
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}
