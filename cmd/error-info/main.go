package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	remoteHost       = "ubuntu@exed-01"
	contextEntries   = 5
	serviceName      = "exed"
	defaultSinceSpan = 7 * 24 * time.Hour
)

func main() {
	sinceFlag := flag.String("since", "", "journalctl --since value (defaults to 1 week ago, e.g. 2024-06-02T15:04:05Z)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: error-info [flags] <pattern>\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	since := *sinceFlag
	if since == "" {
		since = time.Now().Add(-defaultSinceSpan).UTC().Format(time.RFC3339)
	}

	pattern := flag.Arg(0)
	match, err := findLatestMatch(pattern, since)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	before, err := fetchBefore(match.Cursor, contextEntries)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	after, err := fetchAfter(match.Cursor, contextEntries)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	for _, entry := range before {
		fmt.Println(entry.shortPrecise())
	}
	fmt.Println(match.shortPrecise())
	for _, entry := range after {
		fmt.Println(entry.shortPrecise())
	}
}

func findLatestMatch(pattern, since string) (journalEntry, error) {
	args := []string{
		"--since", since,
		"--grep", pattern,
		"--reverse",
		"--lines", "1",
	}
	entries, err := runJournalctl(args...)
	if err != nil {
		return journalEntry{}, err
	}
	if len(entries) == 0 {
		return journalEntry{}, fmt.Errorf("no log entries matched %q", pattern)
	}
	return entries[0], nil
}

func fetchBefore(cursor string, count int) ([]journalEntry, error) {
	if count == 0 {
		return nil, nil
	}
	args := []string{
		"--cursor", cursor,
		"--reverse",
		"--lines", strconv.Itoa(count + 1),
	}
	entries, err := runJournalctl(args...)
	if err != nil {
		return nil, err
	}

	var result []journalEntry
	for _, entry := range entries {
		if entry.Cursor == cursor {
			continue
		}
		result = append(result, entry)
		if len(result) == count {
			break
		}
	}
	reverseEntries(result)
	return result, nil
}

func fetchAfter(cursor string, count int) ([]journalEntry, error) {
	if count == 0 {
		return nil, nil
	}
	args := []string{
		"--after-cursor", cursor,
		"--lines", strconv.Itoa(count),
	}
	return runJournalctl(args...)
}

func runJournalctl(extra ...string) ([]journalEntry, error) {
	base := []string{
		"sudo",
		"journalctl",
		"-u", serviceName,
		"--no-pager",
		"--output", "json",
	}
	base = append(base, extra...)

	remoteCmd := shellJoin(base)

	cmd := exec.Command("ssh", remoteHost, remoteCmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh: %w", err)
	}

	entries, err := parseJournalEntries(stdout)
	if err != nil {
		cmd.Wait()
		return nil, err
	}

	if err := cmd.Wait(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nil, fmt.Errorf("%w: %s", err, errMsg)
		}
		return nil, err
	}

	return entries, nil
}

func parseJournalEntries(r io.Reader) ([]journalEntry, error) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)

	var entries []journalEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		entry, err := decodeEntry(line)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func decodeEntry(raw []byte) (journalEntry, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return journalEntry{}, fmt.Errorf("decode journal entry: %w", err)
	}

	cursor := getString(data, "__CURSOR")
	if cursor == "" {
		return journalEntry{}, fmt.Errorf("journal entry missing cursor")
	}

	ts, err := parseTimestamp(getString(data, "__REALTIME_TIMESTAMP"))
	if err != nil {
		return journalEntry{}, fmt.Errorf("parse timestamp: %w", err)
	}

	entry := journalEntry{
		Cursor:     cursor,
		Timestamp:  ts,
		Hostname:   firstNonEmpty(getString(data, "_HOSTNAME"), hostFromRemote()),
		Identifier: identifierFrom(data),
		PID:        getString(data, "_PID"),
		Message:    getString(data, "MESSAGE"),
	}
	return entry, nil
}

func parseTimestamp(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	secs := micros / 1_000_000
	nanos := (micros % 1_000_000) * 1_000
	return time.Unix(secs, nanos).Local(), nil
}

func identifierFrom(data map[string]any) string {
	id := firstNonEmpty(
		getString(data, "SYSLOG_IDENTIFIER"),
		strings.TrimSuffix(getString(data, "_SYSTEMD_UNIT"), ".service"),
		getString(data, "_COMM"),
	)
	if id == "" {
		return serviceName
	}
	return id
}

func getString(data map[string]any, key string) string {
	val, ok := data[key]
	if !ok {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return fmt.Sprint(v)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func hostFromRemote() string {
	parts := strings.Split(remoteHost, "@")
	if len(parts) == 2 {
		return parts[1]
	}
	return remoteHost
}

type journalEntry struct {
	Cursor     string
	Timestamp  time.Time
	Hostname   string
	Identifier string
	PID        string
	Message    string
}

func (je journalEntry) shortPrecise() string {
	ts := je.Timestamp.Format("Jan 02 15:04:05.000000")
	identifier := je.Identifier
	if identifier == "" {
		identifier = serviceName
	}
	message := strings.ReplaceAll(je.Message, "\n", "\n\t")
	if je.PID != "" {
		return fmt.Sprintf("%s %s %s[%s]: %s", ts, je.Hostname, identifier, je.PID, message)
	}
	return fmt.Sprintf("%s %s %s: %s", ts, je.Hostname, identifier, message)
}

func reverseEntries(entries []journalEntry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}

func shellJoin(parts []string) string {
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellQuote(part))
	}
	return b.String()
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if isSafeShellWord(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func isSafeShellWord(s string) bool {
	for _, r := range s {
		if !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '@' || r == '+' || r == ',' || r == '=' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}
