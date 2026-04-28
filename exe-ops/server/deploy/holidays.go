package deploy

import (
	"time"
)

// USFederalHolidayName returns the name of the holiday if the given date
// is a US federal holiday (in America/New_York), or "" if it is not.
// For fixed-date holidays that fall on Saturday, Friday is observed;
// for Sunday, Monday is observed.
func USFederalHolidayName(t time.Time) string {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return ""
	}
	t = t.In(loc)
	month, day := t.Month(), t.Day()
	weekday := t.Weekday()

	// Fixed-date holiday checker with weekend observation.
	check := func(m time.Month, d int) bool {
		if month == m && day == d {
			return true
		}
		if weekday == time.Friday {
			nextDay := t.AddDate(0, 0, 1)
			if nextDay.Month() == m && nextDay.Day() == d && nextDay.Weekday() == time.Saturday {
				return true
			}
		}
		if weekday == time.Monday {
			prevDay := t.AddDate(0, 0, -1)
			if prevDay.Month() == m && prevDay.Day() == d && prevDay.Weekday() == time.Sunday {
				return true
			}
		}
		return false
	}

	if check(time.January, 1) {
		return "New Year's Day"
	}
	if check(time.June, 19) {
		return "Juneteenth"
	}
	if check(time.July, 4) {
		return "Independence Day"
	}
	if check(time.November, 11) {
		return "Veterans Day"
	}
	if check(time.December, 25) {
		return "Christmas Day"
	}

	if month == time.January && weekday == time.Monday && nthWeekdayOfMonth(t) == 3 {
		return "Martin Luther King Jr. Day"
	}
	if month == time.February && weekday == time.Monday && nthWeekdayOfMonth(t) == 3 {
		return "Presidents' Day"
	}
	if month == time.May && weekday == time.Monday && isLastWeekdayOfMonth(t) {
		return "Memorial Day"
	}
	if month == time.September && weekday == time.Monday && nthWeekdayOfMonth(t) == 1 {
		return "Labor Day"
	}
	if month == time.October && weekday == time.Monday && nthWeekdayOfMonth(t) == 2 {
		return "Indigenous Peoples' Day"
	}
	if month == time.November && weekday == time.Thursday && nthWeekdayOfMonth(t) == 4 {
		return "Thanksgiving"
	}

	return ""
}

// IsUSFederalHoliday returns true if the given date is a US federal holiday.
func IsUSFederalHoliday(t time.Time) bool {
	return USFederalHolidayName(t) != ""
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
