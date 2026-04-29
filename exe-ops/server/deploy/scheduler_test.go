package deploy

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestNextDeployTime(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")

	// Helper to create a time in ET.
	etTime := func(y, mon, d, h, min int) time.Time {
		return time.Date(y, time.Month(mon), d, h, min, 0, 0, et)
	}

	s := &Scheduler{
		nowFunc: time.Now,
	}

	tests := []struct {
		name string
		now  time.Time
		want string // expected next deploy in "2006-01-02 15:04" format (ET)
	}{
		{
			name: "Monday 8:45 AM ET → 9:00 AM ET",
			now:  etTime(2024, 3, 4, 8, 45), // Mon
			want: "2024-03-04 09:00",
		},
		{
			name: "Monday 9:15 AM ET → 9:30 AM ET",
			now:  etTime(2024, 3, 4, 9, 15),
			want: "2024-03-04 09:30",
		},
		{
			name: "Monday 5:00 PM ET (still in window for PT) → 5:30 PM ET",
			now:  etTime(2024, 3, 4, 17, 0),
			want: "2024-03-04 17:30",
		},
		{
			name: "Friday 9:30 PM ET (after 6pm PT) → Monday 9:00 AM ET",
			now:  etTime(2024, 3, 8, 21, 30), // Fri
			want: "2024-03-11 09:00",         // Mon
		},
		{
			name: "Saturday → Monday 9:00 AM ET",
			now:  etTime(2024, 3, 9, 10, 0), // Sat
			want: "2024-03-11 09:00",        // Mon
		},
		{
			name: "Sunday → Monday 9:00 AM ET",
			now:  etTime(2024, 3, 10, 10, 0), // Sun
			want: "2024-03-11 09:00",         // Mon
		},
		{
			name: "New Year's Day 2024 (Monday) → Tuesday 9:00 AM ET",
			now:  etTime(2024, 1, 1, 10, 0),
			want: "2024-01-02 09:00",
		},
		{
			name: "July 3 2020 (Friday, observed for July 4 Sat) → Monday 9:00 AM ET",
			now:  etTime(2020, 7, 3, 10, 0),
			want: "2020-07-06 09:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.nextDeployTime(tt.now)
			gotStr := got.In(et).Format("2006-01-02 15:04")
			if gotStr != tt.want {
				t.Errorf("nextDeployTime(%v) = %v, want %v",
					tt.now.Format("Mon 2006-01-02 15:04 MST"),
					gotStr,
					tt.want)
			}
		})
	}
}

func TestIsDeployableTime(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	pt, _ := time.LoadLocation("America/Los_Angeles")

	etTime := func(y, mon, d, h, min int) time.Time {
		return time.Date(y, time.Month(mon), d, h, min, 0, 0, et)
	}

	s := &Scheduler{}

	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"Monday 9:00 AM ET", etTime(2024, 3, 4, 9, 0), true},
		{"Monday 9:30 AM ET", etTime(2024, 3, 4, 9, 30), true},
		{"Monday 5:00 PM ET", etTime(2024, 3, 4, 17, 0), true},
		{"Monday 8:59 AM ET (before window)", etTime(2024, 3, 4, 8, 59), false},
		{"Monday 9:30 PM ET (after 6pm PT)", etTime(2024, 3, 4, 21, 30), false},
		{"Saturday 10:00 AM ET", etTime(2024, 3, 9, 10, 0), false},
		{"Sunday 10:00 AM ET", etTime(2024, 3, 10, 10, 0), false},
		{"New Year 2024 (holiday)", etTime(2024, 1, 1, 10, 0), false},
		{"Thanksgiving 2024", etTime(2024, 11, 28, 10, 0), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.isDeployableTime(tt.t, et, pt)
			if got != tt.want {
				t.Errorf("isDeployableTime(%v) = %v, want %v",
					tt.t.Format("Mon 2006-01-02 15:04 MST"), got, tt.want)
			}
		})
	}
}

func TestAnnounceFirstLast_FirstDeploy(t *testing.T) {
	notifier := &fakeNotifier{}
	et, _ := time.LoadLocation("America/New_York")
	// Monday 9:00 AM ET — first deploy slot of the day.
	now := time.Date(2024, 3, 4, 9, 0, 0, 0, et)

	s := &Scheduler{
		notifier: notifier,
		channel:  "ship",
		services: []string{"exed"},
		nowFunc:  func() time.Time { return now },
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:  true,
	}

	s.announceFirstLast()

	var gotFirst bool
	for _, msg := range notifier.messages {
		if strings.Contains(msg, "First CD deploy") {
			gotFirst = true
			if !strings.Contains(msg, "• exed") {
				t.Errorf("first-of-day message should list services, got %q", msg)
			}
		}
	}
	if !gotFirst {
		t.Errorf("expected first-of-day message, got %v", notifier.messages)
	}

	// Calling again on the same day should not re-announce.
	notifier.messages = nil
	s.announceFirstLast()
	for _, msg := range notifier.messages {
		if strings.Contains(msg, "First CD deploy") {
			t.Error("first-of-day message should not repeat on the same day")
		}
	}
}

func TestAnnounceFirstLast_LastDeploy(t *testing.T) {
	notifier := &fakeNotifier{}
	et, _ := time.LoadLocation("America/New_York")
	pt, _ := time.LoadLocation("America/Los_Angeles")

	// Find the last deploy slot: 6:00 PM PT on Monday.
	// In ET (EST, March 4 2024), that's 9:00 PM.
	lastSlot := time.Date(2024, 3, 4, 18, 0, 0, 0, pt)

	s := &Scheduler{
		notifier:           notifier,
		channel:            "ship",
		services:           []string{"exed"},
		nowFunc:            func() time.Time { return lastSlot },
		log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:            true,
		announcedFirstDate: lastSlot.In(et).Format("2006-01-02"), // suppress first
	}

	s.announceFirstLast()

	var gotLast bool
	for _, msg := range notifier.messages {
		if strings.Contains(msg, "Last CD deploy") {
			gotLast = true
			if !strings.Contains(msg, "• exed") {
				t.Errorf("last-of-day message should list services, got %q", msg)
			}
		}
	}
	if !gotLast {
		t.Errorf("expected last-of-day message, got %v", notifier.messages)
	}

	// Should not repeat.
	notifier.messages = nil
	s.announceFirstLast()
	for _, msg := range notifier.messages {
		if strings.Contains(msg, "Last CD deploy") {
			t.Error("last-of-day message should not repeat on the same day")
		}
	}
}

func TestAnnounceFirstLast_MidDay(t *testing.T) {
	notifier := &fakeNotifier{}
	et, _ := time.LoadLocation("America/New_York")
	// Monday noon ET — not the last deploy, and first already announced.
	now := time.Date(2024, 3, 4, 12, 0, 0, 0, et)

	s := &Scheduler{
		notifier:           notifier,
		channel:            "ship",
		services:           []string{"exed"},
		nowFunc:            func() time.Time { return now },
		log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:            true,
		announcedFirstDate: now.In(et).Format("2006-01-02"),
	}

	s.announceFirstLast()

	// No first or last message expected.
	for _, msg := range notifier.messages {
		if strings.Contains(msg, "First CD deploy") || strings.Contains(msg, "Last CD deploy") {
			t.Errorf("unexpected announcement at mid-day: %s", msg)
		}
	}
}

func TestAnnounceFirstLast_MultipleServices(t *testing.T) {
	notifier := &fakeNotifier{}
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2024, 3, 4, 9, 0, 0, 0, et)

	s := &Scheduler{
		notifier: notifier,
		channel:  "ship",
		services: []string{"exed", "exeprox", "metricsd"},
		nowFunc:  func() time.Time { return now },
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:  true,
	}

	s.announceFirstLast()

	if len(notifier.messages) == 0 {
		t.Fatal("expected at least one message")
	}
	msg := notifier.messages[0]
	for _, svc := range []string{"exed", "exeprox", "metricsd"} {
		if !strings.Contains(msg, "• "+svc) {
			t.Errorf("message should list %q, got %q", svc, msg)
		}
	}
}

func TestAnnounceFirstLast_ResetsOnEnable(t *testing.T) {
	notifier := &fakeNotifier{}
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2024, 3, 4, 12, 0, 0, 0, et)

	s := &Scheduler{
		notifier:           notifier,
		channel:            "ship",
		nowFunc:            func() time.Time { return now },
		log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		wakeC:              make(chan struct{}, 1),
		enabled:            false,
		announcedFirstDate: now.In(et).Format("2006-01-02"),
	}

	s.Enable()

	// After enable, announced dates should be cleared.
	s.mu.Lock()
	first := s.announcedFirstDate
	s.mu.Unlock()
	if first != "" {
		t.Error("expected announcedFirstDate to be cleared after Enable")
	}
}

func TestSkipReason(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	s := &Scheduler{nowFunc: time.Now}

	tests := []struct {
		name string
		now  time.Time
		want string
	}{
		{"mid-window weekday", time.Date(2024, 3, 4, 12, 0, 0, 0, et), ""},
		{"Saturday", time.Date(2024, 3, 9, 12, 0, 0, 0, et), "weekend"},
		{"Sunday", time.Date(2024, 3, 10, 12, 0, 0, 0, et), "weekend"},
		{"before window", time.Date(2024, 3, 4, 7, 0, 0, 0, et), "outside deploy window"},
		{"New Year 2024", time.Date(2024, 1, 1, 12, 0, 0, 0, et), "New Year's Day"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.skipReason(tt.now)
			if got != tt.want {
				t.Errorf("skipReason(%v) = %q, want %q", tt.now.Format("Mon 2006-01-02 15:04"), got, tt.want)
			}
		})
	}
}

type fakeNotifier struct {
	topics   []string
	messages []string
}

func (f *fakeNotifier) CDSetTopic(channel, topic string) {
	f.topics = append(f.topics, channel+": "+topic)
}

func (f *fakeNotifier) CDPostMessage(channel, text string) {
	f.messages = append(f.messages, channel+": "+text)
}

func TestNotifyDeploy_OutOfBand(t *testing.T) {
	notifier := &fakeNotifier{}
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2024, 3, 4, 12, 15, 0, 0, et) // Mon noon ET, within window

	s := &Scheduler{
		notifier: notifier,
		channel:  "ship",
		nowFunc:  func() time.Time { return now },
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:  true,
	}

	// Simulate a human-initiated exed deploy.
	st := Status{
		ID:          "d-123",
		Process:     "exed",
		State:       "done",
		SHA:         "abcdef1234567890abcdef1234567890abcdef12",
		InitiatedBy: "bryan@exe.dev",
		StartedAt:   now.Add(-2 * time.Minute),
	}
	s.NotifyDeploy(st)

	// Scheduler should have recorded the deploy.
	status := s.Status()
	if status.LastDeploy == nil {
		t.Fatal("expected lastDeploy to be set")
	}
	if status.LastDeploy.SHA != st.SHA {
		t.Errorf("lastDeploy.SHA = %q, want %q", status.LastDeploy.SHA, st.SHA)
	}
	if status.LastDeploy.DeployID != "d-123" {
		t.Errorf("lastDeploy.DeployID = %q, want %q", status.LastDeploy.DeployID, "d-123")
	}

	// Topic should have been updated.
	if len(notifier.topics) == 0 {
		t.Fatal("expected topic update")
	}
	lastTopic := notifier.topics[len(notifier.topics)-1]
	if !strings.Contains(lastTopic, "abcdef123456") {
		t.Errorf("topic should contain short SHA, got %q", lastTopic)
	}
}

func TestNotifyDeploy_IgnoresCDInitiated(t *testing.T) {
	notifier := &fakeNotifier{}

	s := &Scheduler{
		notifier: notifier,
		channel:  "ship",
		nowFunc:  time.Now,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:  true,
	}

	// Simulate a CD-initiated deploy (should be ignored).
	st := Status{
		ID:          "d-456",
		Process:     "exed",
		State:       "done",
		SHA:         "abcdef1234567890abcdef1234567890abcdef12",
		InitiatedBy: "exe-ops",
	}
	s.NotifyDeploy(st)

	if s.Status().LastDeploy != nil {
		t.Error("expected lastDeploy to remain nil for CD-initiated deploy")
	}
	if len(notifier.topics) != 0 {
		t.Error("expected no topic update for CD-initiated deploy")
	}
}

func TestNotifyDeploy_IgnoresNonExed(t *testing.T) {
	notifier := &fakeNotifier{}

	s := &Scheduler{
		notifier: notifier,
		channel:  "ship",
		nowFunc:  time.Now,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:  true,
	}

	// Simulate a non-exed deploy.
	st := Status{
		ID:          "d-789",
		Process:     "exeprox",
		State:       "done",
		SHA:         "abcdef1234567890abcdef1234567890abcdef12",
		InitiatedBy: "bryan@exe.dev",
	}
	s.NotifyDeploy(st)

	if s.Status().LastDeploy != nil {
		t.Error("expected lastDeploy to remain nil for non-exed deploy")
	}
}

func TestNotifyDeploy_IgnoresFailedDeploy(t *testing.T) {
	notifier := &fakeNotifier{}

	s := &Scheduler{
		notifier: notifier,
		channel:  "ship",
		nowFunc:  time.Now,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:  true,
	}

	// Simulate a failed exed deploy.
	st := Status{
		ID:          "d-fail",
		Process:     "exed",
		State:       "failed",
		SHA:         "abcdef1234567890abcdef1234567890abcdef12",
		InitiatedBy: "bryan@exe.dev",
	}
	s.NotifyDeploy(st)

	if s.Status().LastDeploy != nil {
		t.Error("expected lastDeploy to remain nil for failed deploy")
	}
}

func TestNotifyDeploy_DisabledScheduler(t *testing.T) {
	notifier := &fakeNotifier{}
	et, _ := time.LoadLocation("America/New_York")
	now := time.Date(2024, 3, 4, 12, 15, 0, 0, et)

	s := &Scheduler{
		notifier: notifier,
		channel:  "ship",
		nowFunc:  func() time.Time { return now },
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		enabled:  false, // CD disabled
	}

	st := Status{
		ID:          "d-disabled",
		Process:     "exed",
		State:       "done",
		SHA:         "abcdef1234567890abcdef1234567890abcdef12",
		InitiatedBy: "bryan@exe.dev",
		StartedAt:   now.Add(-2 * time.Minute),
	}
	s.NotifyDeploy(st)

	// Should still record the deploy (so next CD enable shows correct last deploy).
	if s.Status().LastDeploy == nil {
		t.Fatal("expected lastDeploy to be set even when CD disabled")
	}
	if s.Status().LastDeploy.SHA != st.SHA {
		t.Errorf("lastDeploy.SHA = %q, want %q", s.Status().LastDeploy.SHA, st.SHA)
	}
	// But should NOT update topic when disabled.
	if len(notifier.topics) != 0 {
		t.Errorf("expected no topic update when CD disabled, got %v", notifier.topics)
	}
}
