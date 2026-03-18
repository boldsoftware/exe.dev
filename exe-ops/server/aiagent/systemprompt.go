package aiagent

import (
	"fmt"
	"strings"
	"time"

	"exe.dev/exe-ops/apitype"
)

// BuildSystemPrompt constructs the system prompt with live fleet context.
func BuildSystemPrompt(servers []apitype.FleetServer) string {
	var b strings.Builder

	b.WriteString(`You are an expert SRE (Site Reliability Engineer) assistant for exe-ops, an infrastructure monitoring system. You have deep knowledge of Linux systems, networking, storage (especially ZFS), and distributed systems operations.

Your role:
- Help operators understand fleet health and diagnose issues
- Provide actionable recommendations based on real fleet data
- Explain metrics, alerts, and system behavior clearly
- Suggest remediation steps for common infrastructure problems

You have access to tools that let you query the fleet database for real-time server information. Use these tools when the user asks about specific servers, fleet status, or needs current data to answer their question.

Guidelines:
- Be concise and direct — operators value brevity
- Lead with the most critical information
- Use exact numbers from the data, not approximations
- Flag anything that looks anomalous or concerning
- When suggesting commands, prefer safe read-only operations first
- If you're unsure about something, say so rather than guessing
- Memory, disk, network, and storage values from the database are in bytes. Always convert these to human-readable units (KB, MB, GB, TB) in your responses unless the user specifically asks for bytes

Formatting rules:
- Use standard Markdown only (headings, bold, lists, tables, fenced code blocks)
- Never use LaTeX, MathJax, or KaTeX notation (no \frac, \text, \left, etc.)
- For math or formulas, use plain text with standard operators: *, /, +, -, =
- Use Markdown tables for structured data
- Use fenced code blocks with a language tag for commands and code

`)

	b.WriteString(fmt.Sprintf("Current time: %s\n\n", time.Now().UTC().Format(time.RFC3339)))

	if len(servers) == 0 {
		b.WriteString("Fleet status: No servers currently reporting.\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("Fleet overview: %d servers reporting\n\n", len(servers)))
	b.WriteString("| Server | Role | Region | Env | CPU%% | Mem Used/Total | Disk Used/Total | Last Seen |\n")
	b.WriteString("|--------|------|--------|-----|------|----------------|-----------------|----------|\n")

	for _, s := range servers {
		memPct := float64(0)
		if s.MemTotal > 0 {
			memPct = float64(s.MemUsed) / float64(s.MemTotal) * 100
		}
		diskPct := float64(0)
		if s.DiskTotal > 0 {
			diskPct = float64(s.DiskUsed) / float64(s.DiskTotal) * 100
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %.1f | %.0f%% (%s/%s) | %.0f%% (%s/%s) | %s |\n",
			s.Name, s.Role, s.Region, s.Env,
			s.CPU,
			memPct, humanBytes(s.MemUsed), humanBytes(s.MemTotal),
			diskPct, humanBytes(s.DiskUsed), humanBytes(s.DiskTotal),
			s.LastSeen,
		))
	}

	// Summarize any concerns.
	var concerns []string
	for _, s := range servers {
		if s.CPU > 90 {
			concerns = append(concerns, fmt.Sprintf("%s: CPU at %.1f%%", s.Name, s.CPU))
		}
		if s.MemTotal > 0 && float64(s.MemUsed)/float64(s.MemTotal) > 0.9 {
			concerns = append(concerns, fmt.Sprintf("%s: memory at %.0f%%", s.Name, float64(s.MemUsed)/float64(s.MemTotal)*100))
		}
		if s.DiskTotal > 0 && float64(s.DiskUsed)/float64(s.DiskTotal) > 0.85 {
			concerns = append(concerns, fmt.Sprintf("%s: disk at %.0f%%", s.Name, float64(s.DiskUsed)/float64(s.DiskTotal)*100))
		}
		if len(s.FailedUnits) > 0 {
			concerns = append(concerns, fmt.Sprintf("%s: %d failed systemd units", s.Name, len(s.FailedUnits)))
		}
	}
	if len(concerns) > 0 {
		b.WriteString("\n⚠ Active concerns:\n")
		for _, c := range concerns {
			b.WriteString(fmt.Sprintf("- %s\n", c))
		}
	}

	return b.String()
}

func humanBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.1fT", float64(b)/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.1fG", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1fM", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1fK", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
