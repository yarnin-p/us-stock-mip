package boundary

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A headline often announces something that has not happened yet: results due
// next week, a split effective on a named day, a regulatory decision date. Such
// an item is not a catalyst today — it is a dated appointment. Treating it as
// present-tense news both invents a catalyst that has not fired and loses the
// date on which it will. ExtractSchedule pulls that date out so the item can be
// parked on a watchlist and re-surface when it actually matters.

// Scheduled catalyst kinds, ordered from most to least price-moving.
const (
	ScheduleEarnings   = "SCHEDULED_EARNINGS"
	ScheduleRegulatory = "SCHEDULED_REGULATORY"
	ScheduleCorporate  = "SCHEDULED_CORPORATE_ACTION"
	ScheduleMeeting    = "SCHEDULED_MEETING"
	ScheduleGeneric    = "SCHEDULED_EVENT"
)

// ScheduledEvent is a dated appointment a headline announces.
type ScheduledEvent struct {
	// EffectiveAt is midnight ET on the announced day. The headline rarely
	// carries a time, so the day is the honest resolution; a consumer that
	// needs an intraday moment must resolve it from the filing itself.
	EffectiveAt time.Time
	Kind        string
	// Matched is the phrase the date was read from, kept so a human can audit
	// a wrong extraction without re-running the parser.
	Matched string
}

var (
	monthNames = map[string]time.Month{
		"january": time.January, "jan": time.January,
		"february": time.February, "feb": time.February,
		"march": time.March, "mar": time.March,
		"april": time.April, "apr": time.April,
		"may":  time.May,
		"june": time.June, "jun": time.June,
		"july": time.July, "jul": time.July,
		"august": time.August, "aug": time.August,
		"september": time.September, "sep": time.September,
		"sept":    time.September,
		"october": time.October, "oct": time.October,
		"november": time.November, "nov": time.November,
		"december": time.December, "dec": time.December,
	}
	// "August 12, 2026" / "Aug 12" / "August 12th"
	textualDate = regexp.MustCompile(
		`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*\.?\s+` +
			`(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?\b`,
	)
	// "8/12/2026" and "8-12-2026"
	numericDate = regexp.MustCompile(
		`\b(\d{1,2})[/-](\d{1,2})(?:[/-](\d{2,4}))?\b`,
	)
	// Only phrases that genuinely point forward should arm a watchlist entry.
	// "reported results for the quarter ended June 30" is past tense and must
	// not be mistaken for an appointment.
	forwardCue = regexp.MustCompile(
		`(?i)\b(will|to)\s+(report|announce|host|hold|present|release)\b|` +
			`\b(scheduled|expected|slated|set)\s+(for|to|on)\b|` +
			`\beffective\s+(on\s+)?\b|` +
			`\b(pdufa|target\s+action)\s+date\b|` +
			`\bupcoming\b|\bwill\s+be\s+held\b|\bdeadline\b`,
	)
	// Securities class-action solicitations announce a "deadline" for every
	// listed company and dominate any date-bearing headline feed. They are a
	// lawyer's filing cut-off, not a company event, and would otherwise fill
	// the watchlist with names that have no scheduled catalyst at all.
	solicitation = regexp.MustCompile(
		`(?i)\b(rosen|bragar\s+eagel|kaplan\s+fox|pomerantz|glancy|` +
			`levi\s*&\s*korsinsky|robbins\s+geller|schall|bronstein|` +
			`faruqi|kahn\s+swick|johnson\s+fistel|gross\s+law)\b|` +
			`\bclass\s+action\b|\blead\s+plaintiff\b|` +
			`\binvestor\s+counsel\b|\blaw\s+firm\b|` +
			`\bsecurities\s+fraud\b|\binvestors\s+with\s+losses\b`,
	)
	pastCue = regexp.MustCompile(
		`(?i)\b(reported|announced|posted|completed|closed|ended|` +
			`recorded|delivered|achieved)\b`,
	)
)

// ExtractSchedule reads a forward-dated event out of a headline. It returns
// false unless the text both points forward and names a day strictly after the
// publication date, so present-tense news is never converted into a fake
// appointment.
func ExtractSchedule(
	headline string,
	publishedAt time.Time,
	location *time.Location,
) (ScheduledEvent, bool) {
	if location == nil {
		location = time.UTC
	}
	text := strings.TrimSpace(headline)
	if text == "" {
		return ScheduledEvent{}, false
	}
	if solicitation.MatchString(text) {
		return ScheduledEvent{}, false
	}
	cue := forwardCue.FindString(text)
	if cue == "" {
		return ScheduledEvent{}, false
	}
	// A headline can carry both a backward report and a forward date. Only a
	// clause with no past-tense verb before the cue is treated as forward.
	if index := strings.Index(text, cue); index > 0 &&
		pastCue.MatchString(text[:index]) {
		return ScheduledEvent{}, false
	}
	published := publishedAt.In(location)
	day, matched, ok := parseDate(text, published, location)
	if !ok || !day.After(published) {
		return ScheduledEvent{}, false
	}
	return ScheduledEvent{
		EffectiveAt: day,
		Kind:        classifySchedule(text),
		Matched:     matched,
	}, true
}

func classifySchedule(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "earning") ||
		strings.Contains(lower, "quarter") ||
		strings.Contains(lower, "results") ||
		strings.Contains(lower, "q1") || strings.Contains(lower, "q2") ||
		strings.Contains(lower, "q3") || strings.Contains(lower, "q4"):
		return ScheduleEarnings
	case strings.Contains(lower, "pdufa") ||
		strings.Contains(lower, "fda") ||
		strings.Contains(lower, "target action") ||
		strings.Contains(lower, "advisory committee"):
		return ScheduleRegulatory
	case strings.Contains(lower, "split") ||
		strings.Contains(lower, "dividend") ||
		strings.Contains(lower, "record date") ||
		strings.Contains(lower, "lockup") || strings.Contains(lower, "lock-up") ||
		strings.Contains(lower, "delisting") ||
		strings.Contains(lower, "uplist"):
		return ScheduleCorporate
	case strings.Contains(lower, "meeting") ||
		strings.Contains(lower, "conference") ||
		strings.Contains(lower, "webcast") ||
		strings.Contains(lower, "shareholder"):
		return ScheduleMeeting
	default:
		return ScheduleGeneric
	}
}

// parseDate returns midnight on the first date the text names. A headline that
// omits the year means the next occurrence, so a December headline naming
// "January 8" resolves into the following year rather than ten months back.
func parseDate(
	text string,
	published time.Time,
	location *time.Location,
) (time.Time, string, bool) {
	if match := textualDate.FindStringSubmatch(text); match != nil {
		month, known := monthNames[strings.ToLower(match[1])]
		if known {
			day, err := strconv.Atoi(match[2])
			if err == nil && day >= 1 && day <= 31 {
				year := published.Year()
				if match[3] != "" {
					if parsed, convErr := strconv.Atoi(match[3]); convErr == nil {
						year = parsed
					}
				}
				candidate := time.Date(
					year, month, day, 0, 0, 0, 0, location,
				)
				if match[3] == "" && candidate.Before(published) {
					candidate = candidate.AddDate(1, 0, 0)
				}
				if candidate.Month() == month && candidate.Day() == day {
					return candidate, match[0], true
				}
			}
		}
	}
	if match := numericDate.FindStringSubmatch(text); match != nil {
		month, monthErr := strconv.Atoi(match[1])
		day, dayErr := strconv.Atoi(match[2])
		if monthErr == nil && dayErr == nil &&
			month >= 1 && month <= 12 && day >= 1 && day <= 31 {
			year := published.Year()
			if match[3] != "" {
				parsed, convErr := strconv.Atoi(match[3])
				if convErr != nil {
					return time.Time{}, "", false
				}
				if parsed < 100 {
					parsed += 2000
				}
				year = parsed
			}
			candidate := time.Date(
				year, time.Month(month), day, 0, 0, 0, 0, location,
			)
			if match[3] == "" && candidate.Before(published) {
				candidate = candidate.AddDate(1, 0, 0)
			}
			if candidate.Month() == time.Month(month) && candidate.Day() == day {
				return candidate, match[0], true
			}
		}
	}
	return time.Time{}, "", false
}

// WatchEntry is a scheduled catalyst waiting for its date.
type WatchEntry struct {
	Ticker        string
	EffectiveDate time.Time
	Kind          string
	AnnouncedAt   time.Time
	Headline      string
}
