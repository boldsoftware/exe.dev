package logging

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/slack-go/slack"
)

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

	// Build the message
	message := &slack.WebhookMessage{}
	message.Text = record.Message

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

	// Add remaining attributes as fields
	for key, value := range attrs {
		var valueStr string
		switch v := value.(type) {
		case string:
			valueStr = v
		case error:
			valueStr = v.Error()
		default:
			valueStr = fmt.Sprintf("%v", v)
		}
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
