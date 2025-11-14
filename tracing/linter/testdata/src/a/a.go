package a

import (
	"context"
	"log/slog"
)

func goodFunction(ctx context.Context) {
	slog.InfoContext(ctx, "this is good")
	slog.WarnContext(ctx, "this is also good")
}

func badFunction(ctx context.Context) {
	slog.Info("should use InfoContext") // want "should use slog.InfoContext instead of slog.Info when ctx is available"
	slog.Warn("should use WarnContext") // want "should use slog.WarnContext instead of slog.Warn when ctx is available"
	slog.Error("should use ErrorContext") // want "should use slog.ErrorContext instead of slog.Error when ctx is available"
	slog.Debug("should use DebugContext") // want "should use slog.DebugContext instead of slog.Debug when ctx is available"
}

func noCtxFunction() {
	slog.Info("this is fine, no ctx in scope")
	slog.Warn("this is also fine")
}

func nestedFunction(ctx context.Context) {
	func() {
		slog.Info("should use InfoContext in closure") // want "should use slog.InfoContext instead of slog.Info when ctx is available"
	}()
}

func withArgs(ctx context.Context) {
	slog.Info("message", "key", "value") // want "should use slog.InfoContext instead of slog.Info when ctx is available"
	slog.Warn("warning", "error", "something") // want "should use slog.WarnContext instead of slog.Warn when ctx is available"
}
