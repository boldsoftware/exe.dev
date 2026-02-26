// sharing-confusion reads cross_user_access.csv and generates
// per-owner JSON email files for use with the emailtool.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type csvRow struct {
	BoxName     string
	OwnerEmail  string
	Port        string
	DefaultPort string
	ShareMode   string
	AccessEmail string
	Count       int
	FirstSeen   string
	LastSeen    string
	RawHost     string
}

type templateRow struct {
	VMName      string
	AccessEmail string
	RawHost     string
	Count       int
	FirstSeen   string
	LastSeen    string
}

type templateData struct {
	Email string
	Rows  []templateRow
}

type emailJSON struct {
	To            string `json:"to"`
	From          string `json:"from"`
	Subject       string `json:"subject"`
	HTMLBody      string `json:"html_body"`
	TextBody      string `json:"text_body"`
	MessageStream string `json:"message_stream"`
	Sent          bool   `json:"sent"`
	SentAt        string `json:"sent_at,omitempty"`
}

func main() {
	csvPath := flag.String("csv", "adhoc/cross_user_access.csv", "path to CSV file")
	outDir := flag.String("out", "adhoc/sharing-confusion-emails", "output directory for JSON email files")
	tmplPath := flag.String("template", "adhoc/cmd/sharing-confusion/template.html", "path to HTML email template")
	flag.Parse()

	if err := run(*csvPath, *outDir, *tmplPath); err != nil {
		log.Fatal(err)
	}
}

func run(csvPath, outDir, tmplPath string) error {
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	rows, err := readCSV(csvPath)
	if err != nil {
		return fmt.Errorf("read csv: %w", err)
	}

	// Group by owner email.
	byOwner := map[string][]csvRow{}
	for _, r := range rows {
		byOwner[r.OwnerEmail] = append(byOwner[r.OwnerEmail], r)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	for ownerEmail, ownerRows := range byOwner {
		// Sort by count descending.
		sort.Slice(ownerRows, func(i, j int) bool {
			return ownerRows[i].Count > ownerRows[j].Count
		})

		var trows []templateRow
		for _, r := range ownerRows {
			trows = append(trows, templateRow{
				VMName:      r.BoxName,
				AccessEmail: r.AccessEmail,
				RawHost:     r.RawHost,
				Count:       r.Count,
				FirstSeen:   formatDate(r.FirstSeen),
				LastSeen:    formatDate(r.LastSeen),
			})
		}

		data := templateData{
			Email: ownerEmail,
			Rows:  trows,
		}

		var htmlBuf strings.Builder
		if err := tmpl.Execute(&htmlBuf, data); err != nil {
			return fmt.Errorf("render template for %s: %w", ownerEmail, err)
		}

		textBody := renderTextBody(data)

		email := emailJSON{
			To:            ownerEmail,
			From:          "exe.dev <support@exe.dev>",
			Subject:       "Potential unintentional VM oversharing",
			HTMLBody:      htmlBuf.String(),
			TextBody:      textBody,
			MessageStream: "",
		}

		// Preserve sent status if the file already exists.
		filename := emailFilename(ownerEmail)
		outPath := filepath.Join(outDir, filename)
		if existing, err := readExistingEmail(outPath); err == nil {
			email.Sent = existing.Sent
			email.SentAt = existing.SentAt
		}

		jsonBytes, err := json.MarshalIndent(email, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json for %s: %w", ownerEmail, err)
		}

		if err := os.WriteFile(outPath, jsonBytes, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}

		status := "new"
		if email.Sent {
			status = "already sent"
		}
		fmt.Printf("  %s (%s) - %d access records\n", ownerEmail, status, len(ownerRows))
	}

	fmt.Printf("\nWrote %d email files to %s\n", len(byOwner), outDir)
	return nil
}

func readCSV(path string) ([]csvRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)

	// Build a map from column name to index using the header.
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, name := range header {
		col[name] = i
	}
	required := []string{"box_name", "owner_email", "port", "default_port", "share_mode", "access_email", "count", "first_seen", "last_seen", "raw_host"}
	for _, name := range required {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("missing required column %q", name)
		}
	}

	var rows []csvRow
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		count, _ := strconv.Atoi(record[col["count"]])
		rows = append(rows, csvRow{
			BoxName:     record[col["box_name"]],
			OwnerEmail:  record[col["owner_email"]],
			Port:        record[col["port"]],
			DefaultPort: record[col["default_port"]],
			ShareMode:   record[col["share_mode"]],
			AccessEmail: record[col["access_email"]],
			Count:       count,
			FirstSeen:   record[col["first_seen"]],
			LastSeen:    record[col["last_seen"]],
			RawHost:     record[col["raw_host"]],
		})
	}
	return rows, nil
}

func renderTextBody(data templateData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dear %s,\n\n", data.Email)
	b.WriteString("We recently discovered and fixed a sharing bug. When you share a VM's HTTP server via a share link or an e-mail, it should only provide access to the specific port you explicitly shared. Prior to this fix, it provided access to all ports. This includes the Shelley port (9999).\n\n")
	b.WriteString("We have scoured our access logs for VMs. The table below shows who may have accessed one of your VMs, on which ports, and how many times. If this sharing was intentional, there is no further action required. If this was unintentional, and if there are secrets or keys on the virtual machine, you should rotate them. Please reach out to support@exe.dev if you have questions.\n\n")

	// Simple text table.
	fmt.Fprintf(&b, "%-25s %-35s %-53s %8s  %s\n", "VM", "Accessed By", "URL", "Requests", "Date Range")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 148))
	for _, r := range data.Rows {
		dateRange := r.FirstSeen + " - " + r.LastSeen
		fmt.Fprintf(&b, "%-25s %-35s %-53s %8d  %s\n", r.VMName, r.AccessEmail, "https://"+r.RawHost, r.Count, dateRange)
	}

	b.WriteString("\nIf you shared the Shelley port because you wanted to work together with a teammate, this fix likely broke your workflow. Please reach out to us at support@exe.dev, and we can help figure out an appropriate solution for your use case.\n\n")
	b.WriteString("Thanks,\nThe exe.dev team\n")
	return b.String()
}

func emailFilename(email string) string {
	s := strings.ReplaceAll(email, "@", "_at_")
	s = strings.ReplaceAll(s, "+", "_plus_")
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, s)
	return s + ".json"
}

func readExistingEmail(path string) (*emailJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var email emailJSON
	if err := json.Unmarshal(data, &email); err != nil {
		return nil, err
	}
	return &email, nil
}

// formatDate trims the timezone offset and sub-second precision
// from a timestamp like "2026-02-03 20:50:53.288+00".
func formatDate(s string) string {
	// Trim everything after seconds.
	if idx := strings.Index(s, "."); idx != -1 {
		s = s[:idx]
	}
	if idx := strings.Index(s, "+"); idx != -1 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
