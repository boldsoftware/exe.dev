package logging

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"exe.dev/stage"
	"exe.dev/tracing"
	"github.com/lmittmann/tint"
	"github.com/prometheus/client_golang/prometheus"
	slogmulti "github.com/samber/slog-multi"
	slogslack "github.com/samber/slog-slack/v2"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// SetupLogger configures slog based on env.
// LOG_FORMAT and LOG_LEVEL environment variables override env settings.
// Empty values fall back to defaults ("text" for format, "info" for level).
// LOG_FORMAT can be "json", "text", or "tint".
// LOG_LEVEL can be "debug", "info", "warn", or "error".
// If registry is non-nil, log metrics will be registered and counted.
func SetupLogger(env stage.Env, registry *prometheus.Registry) {
	// TODO: get rid of LOG_FORMAT and LOG_LEVEL env vars in favor of stage.Env only.
	logFormat := cmp.Or(
		strings.ToLower(os.Getenv("LOG_FORMAT")),
		env.LogFormat,
		"text",
	)
	logLevel := cmp.Or(
		strings.ToLower(os.Getenv("LOG_LEVEL")),
		env.LogLevel,
		"info",
	)

	// Parse log level
	var level slog.Level
	switch logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default: // "info" or unknown
		level = slog.LevelInfo
	}

	// Create handler based on format
	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	switch logFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case "tint":
		// Get the last four characters of the command
		cmd := filepath.Base(os.Args[0])
		var tail string
		if len(cmd) >= 4 {
			tail = cmd[len(cmd)-4:]
		} else {
			tail = cmd
		}
		handler = tint.NewHandler(os.Stdout, &tint.Options{
			Level:      level,
			TimeFormat: "15:04:05 " + tail,
		})
	default: // "text" and any unknown format
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	slackBotToken := strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	if env.LogErrorSlackChannel != "" && slackBotToken != "" {
		// Set Honeycomb environment for trace links in Slack messages
		SetHoneycombEnv(env.HoneycombEnv)
		opt := slogslack.Option{
			Level:     slog.LevelError,
			BotToken:  slackBotToken,
			Channel:   env.LogErrorSlackChannel,
			Converter: HoneycombConverter,
		}
		handler = slogmulti.Fanout(handler, &detachContextHandler{opt.NewSlackHandler()})
	}

	// Add OTEL handler if OTEL_EXPORTER_OTLP_ENDPOINT is configured
	otelEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if otelEndpoint != "" {
		otelHandler := setupOTELHandler()
		if otelHandler != nil {
			handler = slogmulti.Fanout(handler, otelHandler)
		}
	}

	// Wrap handler with tracing handler to add trace_id from context
	handler = tracing.NewHandler(handler)

	// Wrap handler with metrics handler if registry is provided
	if registry != nil {
		metrics := NewLogMetrics(registry)
		handler = NewMetricsHandler(handler, metrics)
	}

	logger := slog.New(handler)
	if env.LogCmdAttr && len(os.Args) > 0 {
		cmdName := filepath.Base(os.Args[0])
		logger = logger.With("cmd", cmdName)
	}

	// Set as default logger
	slog.SetDefault(logger)
}

// setupOTELHandler creates an OTEL log handler that exports logs to the
// configured OTLP endpoint. It uses standard OTEL environment variables:
// - OTEL_EXPORTER_OTLP_ENDPOINT: the endpoint URL (e.g., https://api.honeycomb.io)
// - OTEL_EXPORTER_OTLP_HEADERS: headers to include (e.g., x-honeycomb-team=<api-key>)
// - OTEL_SERVICE_NAME: the service name to use
//
// Backpressure behavior: The batch processor queues up to 2,048 log records
// by default. If the OTLP endpoint is slow or unreachable, logs are dropped
// once the queue is full - no OOM risk. Tunable via environment variables:
// - OTEL_BLRP_MAX_QUEUE_SIZE: max queued records (default 2048)
// - OTEL_BLRP_SCHEDULE_DELAY: export interval (default 1s)
// - OTEL_BLRP_MAX_EXPORT_BATCH_SIZE: records per export (default 512)
func setupOTELHandler() slog.Handler {
	ctx := context.Background()

	logExporter, err := otlploghttp.New(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to create OTEL log exporter", "error", err)
		return nil
	}

	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(
			sdklog.NewBatchProcessor(logExporter),
		),
	)

	scopeName := "unknown"
	if len(os.Args) > 0 {
		scopeName = filepath.Base(os.Args[0])
	}

	return otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(lp))
}

// detachContextHandler wraps a slog.Handler to use context.WithoutCancel,
// preventing context cancellation from affecting the underlying handler.
// This is necessary for handlers like slogslack that spawn goroutines
// using the provided context.
type detachContextHandler struct {
	slog.Handler
}

func (h *detachContextHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.Handler.Handle(context.WithoutCancel(ctx), r)
}
