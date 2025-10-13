package exe

import (
	"context"
	"database/sql"
	"errors"
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
	return queries.RecordUserEvent(context.Background(), exedb.RecordUserEventParams{
		UserID: userID,
		Event:  event,
	})
}

// recordUserEventBestEffort increments the number of times userID has experienced event, logging on error.
func (s *Server) recordUserEventBestEffort(ctx context.Context, userID, event string) {
	err := s.recordUserEvent(ctx, userID, event)
	if err != nil {
		s.slog().Warn("recordUserEventBestEffort database error", "userID", userID, "event", event, "error", err)
	}
}

// userEventCount returns the number of times userID has experienced event.
func (s *Server) userEventCount(ctx context.Context, userID, event string) (int, error) {
	count, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (int64, error) {
		ret, err := queries.GetUserEventCount(ctx, exedb.GetUserEventCountParams{
			UserID: userID,
			Event:  event,
		})
		if err != nil {
			return 0, err
		}
		intRet, ok := ret.(int64)
		if ok {
			return intRet, nil
		}
		return 0, fmt.Errorf("could not convert result to int64")
	})
	if err != nil && errors.Is(err, sql.ErrNoRows) {
		return 0, nil // Event hasn't occurred yet
	}
	return int(count), err
}

// userHasEvent reports whether userID has experienced event.
func (s *Server) userHasEvent(ctx context.Context, userID, event string) (bool, error) {
	count, err := s.userEventCount(ctx, userID, event)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// userHasEventBestEffort reports whether userID has experienced event, returning false on error.
func (s *Server) userHasEventBestEffort(ctx context.Context, userID, event string) bool {
	count, err := s.userEventCount(ctx, userID, event)
	if err != nil {
		s.slog().Warn("userHasEventDefaultNo database error", "userID", userID, "event", event, "error", err)
		return false
	}
	return count > 0
}

func (s *Server) allUserEventsBestEffort(ctx context.Context, userID string) map[string]int {
	events := make(map[string]int)
	results, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.GetAllUserEventsRow, error) {
		return queries.GetAllUserEvents(ctx, userID)
	})
	if err != nil {
		s.slog().Warn("allUserEventsBestEffort database error", "userID", userID, "error", err)
		return events
	}

	for _, result := range results {
		events[result.Event] = int(result.Count)
	}
	return events
}
