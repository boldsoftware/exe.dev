package deploy

import (
	"testing"
	"time"
)

func TestUSFederalHolidayName(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	date := func(y, m, d int) time.Time {
		return time.Date(y, time.Month(m), d, 12, 0, 0, 0, loc)
	}

	// Spot-check that names come back.
	if name := USFederalHolidayName(date(2024, 12, 25)); name != "Christmas Day" {
		t.Errorf("Christmas = %q", name)
	}
	if name := USFederalHolidayName(date(2024, 11, 28)); name != "Thanksgiving" {
		t.Errorf("Thanksgiving = %q", name)
	}
	if name := USFederalHolidayName(date(2024, 3, 12)); name != "" {
		t.Errorf("regular day = %q, want empty", name)
	}
}

func TestIsUSFederalHoliday(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	date := func(y, m, d int) time.Time {
		return time.Date(y, time.Month(m), d, 12, 0, 0, 0, loc)
	}

	tests := []struct {
		name string
		date time.Time
		want bool
	}{
		// Fixed-date holidays
		{"New Year 2024", date(2024, 1, 1), true},
		{"Juneteenth 2024", date(2024, 6, 19), true},
		{"Independence Day 2024", date(2024, 7, 4), true},
		{"Veterans Day 2024", date(2024, 11, 11), true},
		{"Christmas 2024", date(2024, 12, 25), true},

		// Weekend observations (Saturday → Friday, Sunday → Monday)
		{"New Year 2022 observed (Sat→Fri)", date(2021, 12, 31), true},  // Jan 1 2022 is Sat
		{"New Year 2023 observed (Sun→Mon)", date(2023, 1, 2), true},    // Jan 1 2023 is Sun
		{"July 4 2020 observed (Sat→Fri)", date(2020, 7, 3), true},      // July 4 2020 is Sat
		{"Christmas 2022 observed (Sun→Mon)", date(2022, 12, 26), true}, // Dec 25 2022 is Sun

		// Floating holidays
		{"MLK Day 2024 (3rd Mon Jan)", date(2024, 1, 15), true},
		{"Presidents Day 2024 (3rd Mon Feb)", date(2024, 2, 19), true},
		{"Memorial Day 2024 (last Mon May)", date(2024, 5, 27), true},
		{"Labor Day 2024 (1st Mon Sep)", date(2024, 9, 2), true},
		{"Indigenous Peoples' Day 2024 (2nd Mon Oct)", date(2024, 10, 14), true},
		{"Thanksgiving 2024 (4th Thu Nov)", date(2024, 11, 28), true},

		// Not holidays
		{"Regular Tuesday", date(2024, 3, 12), false},
		{"Regular Friday", date(2024, 3, 15), false},
		{"Wrong Monday in January", date(2024, 1, 8), false}, // 2nd Mon, not 3rd
		{"Wrong Monday in May", date(2024, 5, 20), false},    // 3rd Mon, not last
		{"Day before New Year", date(2024, 12, 31), false},
		{"Day after Christmas", date(2024, 12, 26), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsUSFederalHoliday(tt.date)
			if got != tt.want {
				t.Errorf("IsUSFederalHoliday(%v) = %v, want %v", tt.date.Format("2006-01-02 Mon"), got, tt.want)
			}
		})
	}
}
