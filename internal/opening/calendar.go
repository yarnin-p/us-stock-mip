package opening

import "time"

var extraordinaryClosures = map[string]struct{}{
	"2001-09-11": {},
	"2001-09-12": {},
	"2001-09-13": {},
	"2001-09-14": {},
	"2004-06-11": {},
	"2007-01-02": {},
	"2012-10-29": {},
	"2012-10-30": {},
	"2018-12-05": {},
	"2025-01-09": {},
}

// IsTradingDay reports whether the NYSE/Nasdaq regular session is scheduled
// for the supplied civil date. Early-close days still count as trading days.
func IsTradingDay(date time.Time) bool {
	year, month, day := date.Date()
	civil := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if civil.Weekday() == time.Saturday || civil.Weekday() == time.Sunday {
		return false
	}
	if _, closed := extraordinaryClosures[civil.Format(time.DateOnly)]; closed {
		return false
	}

	holidays := []time.Time{
		observedFixedHoliday(year, time.January, 1),
		nthWeekday(year, time.February, time.Monday, 3),
		easterSunday(year).AddDate(0, 0, -2),
		lastWeekday(year, time.May, time.Monday),
		observedFixedHoliday(year, time.July, 4),
		nthWeekday(year, time.September, time.Monday, 1),
		nthWeekday(year, time.November, time.Thursday, 4),
		observedFixedHoliday(year, time.December, 25),
	}
	if year >= 1998 {
		holidays = append(holidays, nthWeekday(year, time.January, time.Monday, 3))
	}
	if year >= 2022 {
		holidays = append(
			holidays,
			observedFixedHoliday(year, time.June, 19),
		)
	}
	for _, holiday := range holidays {
		if civil.Equal(holiday) {
			return false
		}
	}
	return true
}

func observedFixedHoliday(year int, month time.Month, day int) time.Time {
	holiday := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	switch holiday.Weekday() {
	case time.Saturday:
		return holiday.AddDate(0, 0, -1)
	case time.Sunday:
		return holiday.AddDate(0, 0, 1)
	default:
		return holiday
	}
}

func nthWeekday(
	year int,
	month time.Month,
	weekday time.Weekday,
	n int,
) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	offset := (int(weekday) - int(first.Weekday()) + 7) % 7
	return first.AddDate(0, 0, offset+(n-1)*7)
}

func lastWeekday(year int, month time.Month, weekday time.Weekday) time.Time {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
	offset := (int(last.Weekday()) - int(weekday) + 7) % 7
	return last.AddDate(0, 0, -offset)
}

// Meeus/Jones/Butcher Gregorian computus.
func easterSunday(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := time.Month((h + l - 7*m + 114) / 31)
	day := (h+l-7*m+114)%31 + 1
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
