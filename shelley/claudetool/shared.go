// Package claudetool provides tools for Claude AI models.
//
// When adding, removing, or modifying tools in this package,
// remember to update the tool display template in termui/termui.go
// to ensure proper tool output formatting.
package claudetool

import (
	"context"
)

type workingDirCtxKeyType string

const workingDirCtxKey workingDirCtxKeyType = "workingDir"

func WithWorkingDir(ctx context.Context, wd string) context.Context {
	return context.WithValue(ctx, workingDirCtxKey, wd)
}

func WorkingDir(ctx context.Context) string {
	// If cmd.Dir is empty, it uses the current working directory,
	// so we can use that as a fallback.
	wd, _ := ctx.Value(workingDirCtxKey).(string)
	return wd
}

type sessionIDCtxKeyType string

const sessionIDCtxKey sessionIDCtxKeyType = "sessionID"

func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDCtxKey, sessionID)
}

func SessionID(ctx context.Context) string {
	sessionID, _ := ctx.Value(sessionIDCtxKey).(string)
	return sessionID
}
