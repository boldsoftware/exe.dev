package a

import (
	"context"
	"log/slog"
	"net/http"
)

func goodFunction(ctx context.Context) {
	slog.InfoContext(ctx, "this is good")
	slog.WarnContext(ctx, "this is also good")
}

func badFunction(ctx context.Context) {
	slog.Info("should use InfoContext")   // want "should use slog.InfoContext instead of slog.Info when ctx is available"
	slog.Warn("should use WarnContext")   // want "should use slog.WarnContext instead of slog.Warn when ctx is available"
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
	slog.Info("message", "key", "value")       // want "should use slog.InfoContext instead of slog.Info when ctx is available"
	slog.Warn("warning", "error", "something") // want "should use slog.WarnContext instead of slog.Warn when ctx is available"
}

// Test case: ctx declared AFTER the slog call - should NOT flag
func ctxDeclaredAfter() {
	slog.Info("this is fine, ctx not declared yet")
	ctx := context.Background()
	_ = ctx
}

// Test case: slog call before ctx declaration, but then uses ctx after
func ctxDeclaredAfterThenUsed() {
	slog.Info("this is fine, ctx not declared yet")

	ctx := context.Background()
	slog.Info("should use InfoContext") // want "should use slog.InfoContext instead of slog.Info when ctx is available"
	_ = ctx
}

// Test case: HTTP handler without ctx variable - should suggest r.Context()
func httpHandler(w http.ResponseWriter, r *http.Request) {
	slog.Info("should use r.Context()") // want "should use slog.InfoContext instead of slog.Info when r is available"
	slog.Error("error message")         // want "should use slog.ErrorContext instead of slog.Error when r is available"
}

// Test case: HTTP handler WITH ctx variable - should use ctx, not r.Context()
func httpHandlerWithCtx(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slog.Info("should use ctx") // want "should use slog.InfoContext instead of slog.Info when ctx is available"
	_ = ctx
}

// Test case: HTTP handler but r is not *http.Request
func notHttpHandler(w http.ResponseWriter, r string) {
	slog.Info("this is fine, r is not *http.Request")
}
