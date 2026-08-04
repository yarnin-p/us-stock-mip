// Package market owns U.S. equity session classification shared by discovery,
// strategy, risk, and broker execution.
package market

import (
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/opening"
)

type Session string

const (
	SessionClosed     Session = "CLOSED"
	SessionOvernight  Session = "OVERNIGHT"
	SessionPreMarket  Session = "PRE_MARKET"
	SessionRegular    Session = "REGULAR"
	SessionAfterHours Session = "AFTER_HOURS"
)

func SessionAt(now time.Time) Session {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return SessionClosed
	}
	local := now.In(location)
	minute := local.Hour()*60 + local.Minute()

	if minute < 4*60 {
		if opening.IsTradingDay(local) {
			return SessionOvernight
		}
		return SessionClosed
	}
	if minute >= 20*60 {
		if opening.IsTradingDay(local.AddDate(0, 0, 1)) {
			return SessionOvernight
		}
		return SessionClosed
	}
	if !opening.IsTradingDay(local) {
		return SessionClosed
	}
	switch {
	case minute < 9*60+30:
		return SessionPreMarket
	case minute < 16*60:
		return SessionRegular
	default:
		return SessionAfterHours
	}
}

func (session Session) Tradable() bool {
	return session != SessionClosed
}

// WebullOrderSession returns the exact support_trading_session value expected
// by Webull for an order submitted during this market session.
func (session Session) WebullOrderSession() string {
	switch session {
	case SessionOvernight:
		return "NIGHT"
	case SessionPreMarket, SessionRegular, SessionAfterHours:
		return "ALL"
	default:
		return ""
	}
}

// ScreenerRankTypes provides fresh discovery windows for the current session.
// Webull has no overnight-specific screener, so overnight discovery uses the
// latest after-market and daily leaders and refreshes them with overnight data.
func (session Session) ScreenerRankTypes() []string {
	switch session {
	case SessionOvernight:
		return []string{"AFTER_MARKET", "DAY_1"}
	case SessionPreMarket:
		return []string{"PRE_MARKET"}
	case SessionRegular:
		return []string{"MIN_3", "DAY_1"}
	case SessionAfterHours:
		return []string{"AFTER_MARKET"}
	default:
		return nil
	}
}

func TradingDateAt(now time.Time) time.Time {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return now.UTC()
	}
	local := now.In(location)
	if local.Hour() >= 20 {
		local = local.AddDate(0, 0, 1)
	}
	for !opening.IsTradingDay(local) {
		local = local.AddDate(0, 0, 1)
	}
	return time.Date(
		local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location,
	)
}
