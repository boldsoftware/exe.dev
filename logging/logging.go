package logging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"exe.dev/tracing"
	"github.com/lmittmann/tint"
	slogmulti "github.com/samber/slog-multi"
	slogslack "github.com/samber/slog-slack/v2"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// SetupLogger configures slog based on the LOG_FORMAT environment variable.
// LOG_FORMAT can be "json", "text", "tint", or "" (defaults: tint in dev, text in prod)
// LOG_LEVEL can be "debug", "info", "warn", "error" (default: info)
func SetupLogger(devMode string) {
	logFormat := strings.ToLower(os.Getenv("LOG_FORMAT"))
	logLevel := strings.ToLower(os.Getenv("LOG_LEVEL"))

	// Set default format based on dev mode if not explicitly set
	if logFormat == "" {
		if devMode != "" {
			logFormat = "tint" // Use tint in dev mode
		} else {
			logFormat = "text" // Use text in production
		}
	}

	// Parse log level
	var level slog.Level
	switch logLevel {
	case "debug":
		level = slog.LevelDebug
	case "info", "":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// Create handler based on format
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: level,
	}

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
	if devMode == "" && slackBotToken != "" {
		opt := slogslack.Option{
			Level:    slog.LevelError,
			BotToken: slackBotToken,
			Channel:  "#page",
		}
		handler = slogmulti.Fanout(handler, opt.NewSlackHandler())
	}

	// Add OTEL handler if OTEL_EXPORTER_OTLP_ENDPOINT is configured
	otelEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if otelEndpoint != "" {
		otelHandler := setupOTELHandler(level)
		if otelHandler != nil {
			handler = slogmulti.Fanout(handler, otelHandler)
		}
	}

	// Wrap handler with tracing handler to add trace_id from context
	handler = tracing.NewHandler(handler)

	// Create logger with cmd attribute from os.Args[0]
	logger := slog.New(handler)
	if len(os.Args) > 0 {
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
func setupOTELHandler(level slog.Level) slog.Handler {
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
