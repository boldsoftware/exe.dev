package logging

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/slack-go/slack"
)

// maxSlackFieldLen is the maximum length of a single attribute value in a
// Slack attachment field. Slack truncates long messages, so we trim aggressively
// to keep important context visible.
const maxSlackFieldLen = 1500

// truncate shortens s to at most n runes, appending a marker if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("…[truncated, %d more chars]", len(s)-n)
}

// hostname is captured once at startup for use in Slack messages.
var hostname = func() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}()

// honeycombEnv is the Honeycomb environment name (e.g., "production" or "staging").
// Set via SetHoneycombEnv during logger setup.
var honeycombEnv string

// SetHoneycombEnv sets the Honeycomb environment name for generating trace links.
func SetHoneycombEnv(env string) {
	honeycombEnv = env
}

// HoneycombConverter creates Slack messages with Honeycomb trace links.
// When a trace_id attribute is present, it adds a clickable link to view the trace in Honeycomb.
func HoneycombConverter(addSource bool, replaceAttr func(groups []string, a slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record) *slack.WebhookMessage {
	// Collect all attributes
	attrs := make(map[string]any)

	// Add logger-level attributes
	for _, attr := range loggerAttr {
		collectAttr("", attr, attrs)
	}

	// Add record attributes
	record.Attrs(func(attr slog.Attr) bool {
		collectAttr("", attr, attrs)
		return true
	})

	// Build the message. Prepend hostname so it's always visible even if Slack
	// truncates the attachment fields (which can happen for long error messages).
	message := &slack.WebhookMessage{}
	message.Text = fmt.Sprintf("[%s] %s", hostname, record.Message)

	// Color based on level
	color := "#36a64f" // green default
	switch record.Level {
	case slog.LevelError:
		color = "#ff0000" // red
	case slog.LevelWarn:
		color = "#ffcc00" // yellow
	case slog.LevelInfo:
		color = "#36a64f" // green
	case slog.LevelDebug:
		color = "#808080" // gray
	}

	attachment := slack.Attachment{
		Color:  color,
		Fields: []slack.AttachmentField{},
	}

	// Check for trace_id and create Honeycomb link
	var traceID string
	if tid, ok := attrs["trace_id"]; ok {
		if s, ok := tid.(string); ok {
			traceID = s
		}
	}

	if traceID != "" && honeycombEnv != "" {
		honeycombURL := buildHoneycombURL(honeycombEnv, traceID, record.Time)
		attachment.Fields = append(attachment.Fields, slack.AttachmentField{
			Title: "trace",
			Value: fmt.Sprintf("<%s|%s>", honeycombURL, traceID),
			Short: false,
		})
		// Remove trace_id from attrs since we've handled it specially
		delete(attrs, "trace_id")
	}

	// Add remaining attributes as fields. Sort with important keys first
	// (so they survive any downstream truncation) and the rest alphabetically
	// for deterministic output. Truncate each value so a single huge attribute
	// (e.g. a multi-VM error) doesn't push everything else out of view.
	// Render error/err last so short context fields (host, ip, userID, ...)
	// are visible even if Slack truncates the message before reaching the
	// (potentially huge) error value.
	isErr := func(k string) bool { return k == "error" || k == "err" || k == "grpc.error" }
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ei, ej := isErr(keys[i]), isErr(keys[j])
		if ei != ej {
			return !ei
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		value := attrs[key]
		var valueStr string
		switch v := value.(type) {
		case string:
			valueStr = v
		case error:
			valueStr = v.Error()
		default:
			valueStr = fmt.Sprintf("%v", v)
		}
		valueStr = truncate(valueStr, maxSlackFieldLen)
		attachment.Fields = append(attachment.Fields, slack.AttachmentField{
			Title: key,
			Value: valueStr,
			Short: len(valueStr) < 40,
		})
	}

	message.Attachments = []slack.Attachment{attachment}
	return message
}

// collectAttr recursively collects attributes into a flat map.
func collectAttr(prefix string, attr slog.Attr, attrs map[string]any) {
	key := attr.Key
	if prefix != "" {
		key = prefix + "." + key
	}

	if attr.Value.Kind() == slog.KindGroup {
		for _, a := range attr.Value.Group() {
			collectAttr(key, a, attrs)
		}
		return
	}

	attrs[key] = attr.Value.Any()
}

// buildHoneycombURL creates a Honeycomb query URL for the given trace ID.
// The query searches for the trace within ±10 minutes of the event time.
func buildHoneycombURL(env, traceID string, eventTime time.Time) string {
	// Time window: ±10 minutes around the event
	startTime := eventTime.Add(-10 * time.Minute).Unix()
	endTime := eventTime.Add(10 * time.Minute).Unix()

	// Build the query object
	query := map[string]any{
		"start_time":         startTime,
		"end_time":           endTime,
		"granularity":        0,
		"breakdowns":         []any{},
		"calculations":       []map[string]string{{"op": "COUNT"}},
		"filters":            []map[string]string{{"column": "trace_id", "op": "=", "value": traceID}},
		"filter_combination": "AND",
		"orders":             []any{},
		"havings":            []any{},
		"trace_joins":        []any{},
		"limit":              100,
	}

	queryJSON, _ := json.Marshal(query)

	return fmt.Sprintf(
		"https://ui.honeycomb.io/bold-00/environments/%s/datasets/exed?query=%s",
		env,
		url.QueryEscape(string(queryJSON)),
	)
}
