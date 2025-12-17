package execore

import (
	"context"
	"fmt"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

const (
	userEventCreatedBox        = "created_box"
	userEventUsedREPL          = "used_repl"
	userEventSetBrowserCookies = "set_browser_cookies"
	userEventHasRunHelp        = "ran_help"
)

// recordUserEvent increments the number of times userID has experienced event.
func (s *Server) recordUserEvent(ctx context.Context, userID, event string) error {
	if s.db == nil {
		return fmt.Errorf("database not initialized")
	}
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		return s.recordUserEventTx(tx, userID, event)
	})
}

// recordUserEventTx increments the number of times userID has experienced event, within transaction tx.
func (s *Server) recordUserEventTx(tx *sqlite.Tx, userID, event string) error {
	queries := exedb.New(tx.Conn())
	return queries.RecordUserEvent(context.WithoutCancel(tx.Context()), exedb.RecordUserEventParams{
		UserID: userID,
		Event:  event,
	})
}

// recordUserEventBestEffort increments the number of times userID has experienced event, logging on error.
func (s *Server) recordUserEventBestEffort(ctx context.Context, userID, event string) {
	err := s.recordUserEvent(ctx, userID, event)
	if err != nil {
		s.slog().WarnContext(ctx, "recordUserEventBestEffort database error", "userID", userID, "event", event, "error", err)
	}
}

func (s *Server) allUserEventsBestEffort(ctx context.Context, userID string) map[string]int {
	events := make(map[string]int)
	results, err := withRxRes1(s, ctx, (*exedb.Queries).GetAllUserEvents, userID)
	if err != nil {
		s.slog().WarnContext(ctx, "allUserEventsBestEffort database error", "userID", userID, "error", err)
		return events
	}

	for _, result := range results {
		events[result.Event] = int(result.Count)
	}
	return events
}
