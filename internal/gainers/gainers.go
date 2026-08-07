// Package gainers ranks what moved in a session and, more importantly, says
// why.
//
// A ranked list of tickers answers nothing by itself. The same +40% means one
// thing on a two-million-share float that rotated eighty times and something
// else entirely on a large cap that drifted up on a broker note, and only the
// first kind is worth a trader's attention the next morning. So every entry
// carries the evidence that produced it — float, rotation, relative volume,
// the news item if there was one — and a set of tags that make the history
// queryable by cause rather than only readable by eye.
//
// The reasons are deliberately not a score. A single number would hide exactly
// the distinction the history exists to preserve: two names can share a rank
// and have nothing in common.
package gainers

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

// Session is the window a move is measured over.
type Session string

const (
	SessionPreMarket  Session = "PRE_MARKET"
	SessionRegular    Session = "REGULAR"
	SessionAfterHours Session = "AFTER_HOURS"
)

// Sessions returns the three windows in the order a trading day runs.
func Sessions() []Session {
	return []Session{SessionPreMarket, SessionRegular, SessionAfterHours}
}

// Valid reports whether a session string is one this package ranks.
func (session Session) Valid() bool {
	switch session {
	case SessionPreMarket, SessionRegular, SessionAfterHours:
		return true
	}
	return false
}

// Reference sources name what a session's move is measured from. Pre-market
// and regular are measured from the prior regular close; after-hours from the
// same day's regular close, because that is the price an after-hours move
// actually departs from.
const (
	SourcePriorClose   = "prior_regular_close"
	SourceRegularClose = "regular_close"
)

// Candidate is one ticker's session behaviour plus everything known about it
// at the time. Fields left at zero are treated as unknown rather than as zero:
// a missing float must not read as a float of nothing.
type Candidate struct {
	Ticker          string
	ReferencePrice  float64
	ReferenceSource string
	High            float64
	Low             float64
	Close           float64
	Volume          float64
	AverageVolume   float64
	FloatShares     float64
	MarketCap       float64
	Price52WeekHigh float64
	Price52WeekLow  float64

	HasNews           bool
	NewsTitle         string
	NewsPublishedAt   time.Time
	NewsCatalystScore float64
}

// Entry is a ranked gainer with its evidence and its reasons.
type Entry struct {
	Rank            int
	Ticker          string
	ReferencePrice  float64
	ReferenceSource string
	High            float64
	Low             float64
	Close           float64
	// ChangeRatio ranks the list: where the session finished.
	ChangeRatio float64
	// MaxChangeRatio is the best the session offered. A trader who exits into
	// strength sees this number, not the close, so both are kept.
	MaxChangeRatio float64

	Volume          float64
	AverageVolume   float64
	RelativeVolume  float64
	FloatShares     float64
	FloatRotation   float64
	MarketCap       float64
	Price52WeekHigh float64
	Price52WeekLow  float64

	HasNews           bool
	NewsTitle         string
	NewsPublishedAt   time.Time
	NewsCatalystScore float64

	Reasons []string
}

// Reason tags. They describe the evidence, not a verdict.
const (
	ReasonExtremeRotation = "EXTREME_ROTATION"
	ReasonHighRotation    = "HIGH_ROTATION"
	ReasonMicroFloat      = "MICRO_FLOAT"
	ReasonLowFloat        = "LOW_FLOAT"
	ReasonExtremeVolume   = "EXTREME_RVOL"
	ReasonHighVolume      = "HIGH_RVOL"
	ReasonNewsCatalyst    = "NEWS_CATALYST"
	ReasonStrongCatalyst  = "STRONG_CATALYST"
	ReasonNoStoredNews    = "NO_STORED_NEWS"
	ReasonSubDollar       = "SUB_DOLLAR"
	ReasonNanoCap         = "NANO_CAP"
	ReasonFadedFromHigh   = "FADED_FROM_HIGH"
	ReasonClosedAtHigh    = "CLOSED_AT_HIGH"
	ReasonNewHigh52Week   = "NEW_52W_HIGH"
	ReasonNearLow52Week   = "NEAR_52W_LOW"
)

// Thresholds are the cut points the tags use. The rotation and relative-volume
// numbers are the ones the after-hours boundary study measured, not taste:
// rotation at or above 2.0 lifted the spike rate from 11% to 46%.
type Thresholds struct {
	ExtremeRotation float64
	HighRotation    float64
	MicroFloat      float64
	LowFloat        float64
	ExtremeVolume   float64
	HighVolume      float64
	StrongCatalyst  float64
	SubDollar       float64
	NanoCap         float64
	// FadeFromHigh marks a session that gave back this much of its high.
	FadeFromHigh float64
	// CloseAtHigh marks a session that finished within this much of its high.
	CloseAtHigh float64
	// NearLow52Week marks a name trading this close to its yearly low.
	NearLow52Week float64
}

// DefaultThresholds returns the measured cut points.
func DefaultThresholds() Thresholds {
	return Thresholds{
		ExtremeRotation: 5.0,
		HighRotation:    2.0,
		MicroFloat:      2_000_000,
		LowFloat:        10_000_000,
		ExtremeVolume:   20.0,
		HighVolume:      5.0,
		StrongCatalyst:  0.75,
		SubDollar:       1.0,
		NanoCap:         50_000_000,
		FadeFromHigh:    0.30,
		CloseAtHigh:     0.05,
		NearLow52Week:   0.10,
	}
}

// Config bounds a ranking run.
type Config struct {
	// Limit is how many gainers to keep. Zero keeps them all.
	Limit int
	// MinChange excludes names that barely moved; a "gainer" that rose one
	// percent teaches nothing and crowds out the ones that did.
	MinChange float64
	MinPrice  float64
	MaxPrice  float64
	// MinVolume keeps out names whose move happened on a handful of shares,
	// where the percentage is real but untradeable.
	MinVolume  float64
	Thresholds Thresholds
}

// DefaultConfig returns a sensible ranking configuration.
func DefaultConfig() Config {
	return Config{
		Limit:      30,
		MinChange:  0.10,
		MinPrice:   0.10,
		MaxPrice:   1000,
		MinVolume:  50_000,
		Thresholds: DefaultThresholds(),
	}
}

func (config Config) validate() error {
	if config.Limit < 0 {
		return errors.New("gainers limit must not be negative")
	}
	if config.MaxPrice > 0 && config.MinPrice > config.MaxPrice {
		return errors.New("gainers minimum price exceeds the maximum")
	}
	return nil
}

// Rank turns a session's candidates into the ranked, explained list.
//
// Ordering is by where the session closed, not by the high. A name that
// touched +200% and closed flat was a different event from one that held its
// gain, and ranking on the high would put the first above the second every
// time.
func Rank(candidates []Candidate, config Config) ([]Entry, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if config.Thresholds == (Thresholds{}) {
		config.Thresholds = DefaultThresholds()
	}
	entries := make([]Entry, 0, len(candidates))
	for _, candidate := range candidates {
		entry, ok := build(candidate, config)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(first, second int) bool {
		if entries[first].ChangeRatio != entries[second].ChangeRatio {
			return entries[first].ChangeRatio > entries[second].ChangeRatio
		}
		// A tie on the close is broken by rotation, which is the evidence that
		// separates a crowded move from a thin one.
		return entries[first].FloatRotation > entries[second].FloatRotation
	})
	if config.Limit > 0 && len(entries) > config.Limit {
		entries = entries[:config.Limit]
	}
	for index := range entries {
		entries[index].Rank = index + 1
	}
	return entries, nil
}

func build(candidate Candidate, config Config) (Entry, bool) {
	ticker := strings.ToUpper(strings.TrimSpace(candidate.Ticker))
	if ticker == "" || candidate.ReferencePrice <= 0 || candidate.Close <= 0 {
		return Entry{}, false
	}
	if math.IsNaN(candidate.Close) || math.IsInf(candidate.Close, 0) {
		return Entry{}, false
	}
	change := candidate.Close/candidate.ReferencePrice - 1
	if change < config.MinChange {
		return Entry{}, false
	}
	if config.MinPrice > 0 && candidate.Close < config.MinPrice {
		return Entry{}, false
	}
	if config.MaxPrice > 0 && candidate.Close > config.MaxPrice {
		return Entry{}, false
	}
	if config.MinVolume > 0 && candidate.Volume < config.MinVolume {
		return Entry{}, false
	}

	entry := Entry{
		Ticker:          ticker,
		ReferencePrice:  candidate.ReferencePrice,
		ReferenceSource: candidate.ReferenceSource,
		High:            candidate.High, Low: candidate.Low, Close: candidate.Close,
		ChangeRatio: change,
		Volume:      candidate.Volume, AverageVolume: candidate.AverageVolume,
		FloatShares: candidate.FloatShares, MarketCap: candidate.MarketCap,
		Price52WeekHigh: candidate.Price52WeekHigh,
		Price52WeekLow:  candidate.Price52WeekLow,
		HasNews:         candidate.HasNews, NewsTitle: candidate.NewsTitle,
		NewsPublishedAt:   candidate.NewsPublishedAt,
		NewsCatalystScore: candidate.NewsCatalystScore,
	}
	if candidate.High > 0 {
		entry.MaxChangeRatio = candidate.High/candidate.ReferencePrice - 1
	}
	if candidate.FloatShares > 0 && candidate.Volume > 0 {
		entry.FloatRotation = candidate.Volume / candidate.FloatShares
	}
	if candidate.AverageVolume > 0 && candidate.Volume > 0 {
		entry.RelativeVolume = candidate.Volume / candidate.AverageVolume
	}
	entry.Reasons = reasonsFor(entry, config.Thresholds)
	return entry, true
}

// reasonsFor derives the tags. Order is deliberate: the strongest evidence
// first, so a truncated display still shows what mattered.
func reasonsFor(entry Entry, thresholds Thresholds) []string {
	reasons := make([]string, 0, 6)

	switch {
	case entry.FloatRotation >= thresholds.ExtremeRotation:
		reasons = append(reasons, ReasonExtremeRotation)
	case entry.FloatRotation >= thresholds.HighRotation:
		reasons = append(reasons, ReasonHighRotation)
	}
	switch {
	case entry.FloatShares > 0 && entry.FloatShares <= thresholds.MicroFloat:
		reasons = append(reasons, ReasonMicroFloat)
	case entry.FloatShares > 0 && entry.FloatShares <= thresholds.LowFloat:
		reasons = append(reasons, ReasonLowFloat)
	}
	switch {
	case entry.RelativeVolume >= thresholds.ExtremeVolume:
		reasons = append(reasons, ReasonExtremeVolume)
	case entry.RelativeVolume >= thresholds.HighVolume:
		reasons = append(reasons, ReasonHighVolume)
	}

	// The distinction between "no news" and "no stored news" is the one the
	// evidence discipline turns on: the feed's silence is a fact about the
	// feed, not about the world.
	switch {
	case entry.HasNews && entry.NewsCatalystScore >= thresholds.StrongCatalyst:
		reasons = append(reasons, ReasonStrongCatalyst)
	case entry.HasNews:
		reasons = append(reasons, ReasonNewsCatalyst)
	default:
		reasons = append(reasons, ReasonNoStoredNews)
	}

	if entry.Close > 0 && entry.Close < thresholds.SubDollar {
		reasons = append(reasons, ReasonSubDollar)
	}
	if entry.MarketCap > 0 && entry.MarketCap <= thresholds.NanoCap {
		reasons = append(reasons, ReasonNanoCap)
	}

	if entry.High > 0 && entry.Close > 0 {
		giveback := 1 - entry.Close/entry.High
		switch {
		case giveback >= thresholds.FadeFromHigh:
			reasons = append(reasons, ReasonFadedFromHigh)
		case giveback <= thresholds.CloseAtHigh:
			reasons = append(reasons, ReasonClosedAtHigh)
		}
	}
	if entry.Price52WeekHigh > 0 && entry.High >= entry.Price52WeekHigh {
		reasons = append(reasons, ReasonNewHigh52Week)
	}
	if entry.Price52WeekLow > 0 &&
		entry.Close <= entry.Price52WeekLow*(1+thresholds.NearLow52Week) {
		reasons = append(reasons, ReasonNearLow52Week)
	}
	return reasons
}
