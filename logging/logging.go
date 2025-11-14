package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"exe.dev/tracing"
	"github.com/lmittmann/tint"
	slogmulti "github.com/samber/slog-multi"
	slogslack "github.com/samber/slog-slack/v2"
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
