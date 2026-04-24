package execore

import (
	"testing"
	"time"
)

func TestBillingPeriod_CalendarMonth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		now       time.Time
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "mid month",
			now:       time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
			wantStart: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "first day",
			now:       time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			wantStart: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "last day of december",
			now:       time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC),
			wantStart: time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := calendarMonthPeriod(tc.now)
			if !start.Equal(tc.wantStart) {
				t.Errorf("start: got %v, want %v", start, tc.wantStart)
			}
			if !end.Equal(tc.wantEnd) {
				t.Errorf("end: got %v, want %v", end, tc.wantEnd)
			}
		})
	}
}

func TestBillingPeriod_Anchored(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		now       time.Time
		anchorDay int
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "anchor on 15th, now is 20th",
			now:       time.Date(2024, 6, 20, 0, 0, 0, 0, time.UTC),
			anchorDay: 15,
			wantStart: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 7, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "anchor on 15th, now is 10th (before anchor this month)",
			now:       time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC),
			anchorDay: 15,
			wantStart: time.Date(2024, 5, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "anchor on 31st in february (clamp to 29)",
			now:       time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC),
			anchorDay: 31,
			wantStart: time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := anchoredMonthPeriod(tc.now, tc.anchorDay)
			if !start.Equal(tc.wantStart) {
				t.Errorf("start: got %v, want %v", start, tc.wantStart)
			}
			if !end.Equal(tc.wantEnd) {
				t.Errorf("end: got %v, want %v", end, tc.wantEnd)
			}
		})
	}
}
