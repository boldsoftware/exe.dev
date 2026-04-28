package deploy

import (
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
