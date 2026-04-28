package deploy

import (
	"time"
)

// IsUSFederalHoliday returns true if the given date (in the America/New_York
// timezone) is a US federal holiday. For fixed-date holidays that fall on
// Saturday, Friday is observed; for Sunday, Monday is observed.
func IsUSFederalHoliday(t time.Time) bool {
	// Normalize to America/New_York for consistency with the scheduler window.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		// Fallback: shouldn't happen, but treat as not a holiday if TZ fails.
		return false
	}
	t = t.In(loc)
	month, day := t.Month(), t.Day()
	weekday := t.Weekday()

	// Fixed-date holidays with weekend observation rules.
	check := func(m time.Month, d int) bool {
		if month == m && day == d {
			return true
		}
		// Saturday observance: Friday before (might be in previous month/year).
		if weekday == time.Friday {
			nextDay := t.AddDate(0, 0, 1)
			if nextDay.Month() == m && nextDay.Day() == d && nextDay.Weekday() == time.Saturday {
				return true
			}
		}
		// Sunday observance: Monday after (might be in next month/year).
		if weekday == time.Monday {
			prevDay := t.AddDate(0, 0, -1)
			if prevDay.Month() == m && prevDay.Day() == d && prevDay.Weekday() == time.Sunday {
				return true
			}
		}
		return false
	}

	// New Year's Day (Jan 1)
	if check(time.January, 1) {
		return true
	}
	// Juneteenth (June 19)
	if check(time.June, 19) {
		return true
	}
	// Independence Day (July 4)
	if check(time.July, 4) {
		return true
	}
	// Veterans Day (Nov 11)
	if check(time.November, 11) {
		return true
	}
	// Christmas (Dec 25)
	if check(time.December, 25) {
		return true
	}

	// Floating holidays (Nth weekday of month).
	// MLK Day: 3rd Monday in January
	if month == time.January && weekday == time.Monday {
		if nthWeekdayOfMonth(t) == 3 {
			return true
		}
	}
	// Presidents' Day: 3rd Monday in February
	if month == time.February && weekday == time.Monday {
		if nthWeekdayOfMonth(t) == 3 {
			return true
		}
	}
	// Memorial Day: Last Monday in May
	if month == time.May && weekday == time.Monday {
		if isLastWeekdayOfMonth(t) {
			return true
		}
	}
	// Labor Day: 1st Monday in September
	if month == time.September && weekday == time.Monday {
		if nthWeekdayOfMonth(t) == 1 {
			return true
		}
	}
	// Columbus Day: 2nd Monday in October
	if month == time.October && weekday == time.Monday {
		if nthWeekdayOfMonth(t) == 2 {
			return true
		}
	}
	// Thanksgiving: 4th Thursday in November
	if month == time.November && weekday == time.Thursday {
		if nthWeekdayOfMonth(t) == 4 {
			return true
		}
	}

	return false
}

// nthWeekdayOfMonth returns which occurrence of the weekday this is in the
// month (1-based). E.g., the second Monday of the month returns 2.
func nthWeekdayOfMonth(t time.Time) int {
	return (t.Day()-1)/7 + 1
}

// isLastWeekdayOfMonth returns true if t is the last occurrence of its
// weekday in the month.
func isLastWeekdayOfMonth(t time.Time) bool {
	weekday := t.Weekday()
	next := t.AddDate(0, 0, 7)
	return next.Month() != t.Month() || next.Weekday() != weekday
}
