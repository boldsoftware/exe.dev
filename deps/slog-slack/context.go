package slogslack

import "context"

type threadTimestampCtxKey struct{}

// reply in thread to posts with timestamps set this
func WithThreadTimestamp(ctx context.Context, ts string) context.Context {
	return context.WithValue(ctx, threadTimestampCtxKey{}, ts)
}

func contextThreadTimestamp(ctx context.Context) string {
	if v, ok := ctx.Value(threadTimestampCtxKey{}).(string); ok {
		return v
	}
	return ""
}

type replyBroadcastCtxKey struct{}

// broadcast to channel when replies to thread if set
func WithReplyBroadcast(ctx context.Context) context.Context {
	return context.WithValue(ctx, replyBroadcastCtxKey{}, true)
}

func contextReplyBroadcast(ctx context.Context) bool {
	_, ok := ctx.Value(replyBroadcastCtxKey{}).(bool)
	return ok
}
