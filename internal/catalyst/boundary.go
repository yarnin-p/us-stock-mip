package catalyst

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

type BoundaryConfig struct {
	MaxCandidates       int
	TotalNotionalTHB    float64
	USDTHB              float64
	NewsLookback        time.Duration
	MinCatalystStrength float64
	MinVolume           float64
	MinPrice            float64
	MaxPrice            float64
	MaxSpread           float64
	QuoteMaxAge         time.Duration
	SignalMaxAge        time.Duration
	StopLoss            float64
	TrailActivation     float64
	TrailDistance       float64
	ProfitLockFloor     float64
	ExitFeeMinimum      float64
	ExitFeePerShare     float64
	SlippageReserve     float64
	MinimumNetProfit    float64
}

func DefaultBoundaryConfig() BoundaryConfig {
	return BoundaryConfig{
		MaxCandidates: 3, TotalNotionalTHB: 100_000, USDTHB: 33.6,
		NewsLookback: 8 * time.Hour, MinCatalystStrength: 0.75,
		MinVolume: 25_000, MinPrice: 0.20, MaxPrice: 100,
		MaxSpread: 0.03, QuoteMaxAge: 30 * time.Second,
		SignalMaxAge: 2 * time.Minute,
		StopLoss:     0.06, TrailActivation: 0.08,
		TrailDistance: 0.04, ProfitLockFloor: 0.015,
		ExitFeeMinimum: 0.03, ExitFeePerShare: 0.006,
		SlippageReserve: 0.005, MinimumNetProfit: 0.01,
	}
}

type Candidate struct {
	Ticker           string                `json:"ticker"`
	News             intelligence.NewsItem `json:"news"`
	Bid              float64               `json:"bid"`
	BidSize          float64               `json:"bid_size"`
	Ask              float64               `json:"ask"`
	AskSize          float64               `json:"ask_size"`
	QuoteObservedAt  time.Time             `json:"quote_observed_at"`
	Price            float64               `json:"price"`
	Volume           float64               `json:"volume"`
	ChangeRatio      float64               `json:"change_ratio"`
	SignalObservedAt time.Time             `json:"signal_observed_at"`
}

type Selection struct {
	Candidate
	Rank           int                             `json:"rank"`
	Score          float64                         `json:"score"`
	Quantity       int64                           `json:"quantity"`
	AllocatedUSD   float64                         `json:"allocated_usd"`
	Spread         float64                         `json:"spread"`
	BuyerPressure  float64                         `json:"buyer_pressure"`
	Classification intelligence.NewsClassification `json:"classification"`
	Reasons        []string                        `json:"reasons"`
}

type Selector struct {
	config BoundaryConfig
}

func NewSelector(config BoundaryConfig) (*Selector, error) {
	if config.SignalMaxAge == 0 {
		config.SignalMaxAge = 2 * time.Minute
	}
	if config.ExitFeeMinimum == 0 &&
		config.ExitFeePerShare == 0 &&
		config.SlippageReserve == 0 &&
		config.MinimumNetProfit == 0 {
		defaults := DefaultBoundaryConfig()
		config.ExitFeeMinimum = defaults.ExitFeeMinimum
		config.ExitFeePerShare = defaults.ExitFeePerShare
		config.SlippageReserve = defaults.SlippageReserve
		config.MinimumNetProfit = defaults.MinimumNetProfit
	}
	if config.MaxCandidates < 1 || config.MaxCandidates > 3 {
		return nil, errors.New(
			"boundary max candidates must be between 1 and 3",
		)
	}
	if config.TotalNotionalTHB <= 0 || config.USDTHB <= 0 ||
		math.IsNaN(config.TotalNotionalTHB) || math.IsNaN(config.USDTHB) ||
		math.IsInf(config.TotalNotionalTHB, 0) ||
		math.IsInf(config.USDTHB, 0) {
		return nil, errors.New("boundary notional and FX rate must be positive")
	}
	if config.NewsLookback < time.Hour ||
		config.NewsLookback > 7*24*time.Hour ||
		config.MinCatalystStrength <= 0 ||
		config.MinCatalystStrength > 1 ||
		config.MinVolume < 0 ||
		config.MinPrice <= 0 ||
		config.MaxPrice <= config.MinPrice ||
		config.MaxSpread <= 0 ||
		config.MaxSpread >= 1 ||
		config.QuoteMaxAge <= 0 ||
		config.SignalMaxAge <= 0 ||
		config.StopLoss <= 0 ||
		config.StopLoss >= 1 ||
		config.TrailActivation <= 0 ||
		config.TrailActivation >= 1 ||
		config.TrailDistance <= 0 ||
		config.TrailDistance >= 1 ||
		config.ProfitLockFloor <= 0 ||
		config.ProfitLockFloor >= config.TrailActivation ||
		config.ExitFeeMinimum < 0 ||
		config.ExitFeePerShare < 0 ||
		config.SlippageReserve < 0 ||
		config.SlippageReserve >= 1 ||
		config.MinimumNetProfit < 0 ||
		!finite(
			config.ExitFeeMinimum,
			config.ExitFeePerShare,
			config.SlippageReserve,
			config.MinimumNetProfit,
		) {
		return nil, errors.New("invalid boundary shadow configuration")
	}
	return &Selector{config: config}, nil
}

func (selector *Selector) Config() BoundaryConfig {
	return selector.config
}

func (selector *Selector) Select(
	now time.Time,
	candidates []Candidate,
) []Selection {
	now = now.UTC()
	eligible := make([]Selection, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Ticker = strings.ToUpper(strings.TrimSpace(candidate.Ticker))
		classification := intelligence.ClassifyNews(candidate.News)
		if candidate.Ticker == "" ||
			!classification.Tradeable ||
			classification.Negative ||
			classification.Strength < selector.config.MinCatalystStrength ||
			candidate.News.PublishedAt.After(now) ||
			candidate.News.PublishedAt.Before(
				now.Add(-selector.config.NewsLookback),
			) ||
			candidate.News.AvailableAt.After(now) ||
			candidate.Bid <= 0 ||
			candidate.Ask <= 0 ||
			candidate.Bid > candidate.Ask ||
			candidate.Ask < selector.config.MinPrice ||
			candidate.Ask > selector.config.MaxPrice ||
			candidate.Volume < selector.config.MinVolume ||
			candidate.QuoteObservedAt.IsZero() ||
			now.Sub(candidate.QuoteObservedAt) > selector.config.QuoteMaxAge ||
			candidate.SignalObservedAt.IsZero() ||
			now.Sub(candidate.SignalObservedAt) > selector.config.SignalMaxAge {
			continue
		}
		spread := (candidate.Ask - candidate.Bid) /
			((candidate.Ask + candidate.Bid) / 2)
		if spread > selector.config.MaxSpread {
			continue
		}
		pressure := 0.5
		if total := candidate.BidSize + candidate.AskSize; total > 0 {
			pressure = candidate.BidSize / total
		}
		volumeScore := math.Min(
			math.Max(
				math.Log10(max(candidate.Volume, 1))-
					math.Log10(max(selector.config.MinVolume, 1)),
				0,
			)/2,
			1,
		)
		bookScore := math.Max(
			0,
			(1-spread/selector.config.MaxSpread)*0.6+
				pressure*0.4,
		)
		freshnessScore := 0.0
		availableAt := candidate.News.AvailableAt
		if availableAt.IsZero() {
			availableAt = candidate.News.PublishedAt
		}
		age := now.Sub(availableAt)
		switch {
		case age <= 2*time.Hour:
			freshnessScore = 1
		case age <= 8*time.Hour:
			freshnessScore = 0.5
		}
		score := classification.Strength*75 +
			volumeScore*10 +
			bookScore*10 +
			freshnessScore*5
		eligible = append(eligible, Selection{
			Candidate: candidate, Score: score,
			Spread: spread, BuyerPressure: pressure,
			Classification: classification,
			Reasons: append(
				append([]string(nil), classification.Reasons...),
				"fresh Webull top-of-book",
				"minimum live volume passed",
			),
		})
	}
	sort.SliceStable(eligible, func(left, right int) bool {
		if eligible[left].Score != eligible[right].Score {
			return eligible[left].Score > eligible[right].Score
		}
		if !eligible[left].News.PublishedAt.Equal(
			eligible[right].News.PublishedAt,
		) {
			return eligible[left].News.PublishedAt.After(
				eligible[right].News.PublishedAt,
			)
		}
		return eligible[left].Ticker < eligible[right].Ticker
	})
	if len(eligible) > selector.config.MaxCandidates {
		eligible = eligible[:selector.config.MaxCandidates]
	}
	if len(eligible) == 0 {
		return eligible
	}
	allocation := selector.config.TotalNotionalTHB /
		selector.config.USDTHB /
		float64(len(eligible))
	result := make([]Selection, 0, len(eligible))
	for _, item := range eligible {
		quantity := int64(math.Floor(allocation / item.Ask))
		if quantity < 1 {
			continue
		}
		item.Rank = len(result) + 1
		item.Quantity = quantity
		item.AllocatedUSD = float64(quantity) * item.Ask
		result = append(result, item)
	}
	return result
}

func IsEntryWindow(now time.Time) bool {
	eastern, ok := eastern(now)
	if !ok ||
		eastern.Weekday() == time.Saturday ||
		eastern.Weekday() == time.Sunday {
		return false
	}
	seconds := eastern.Hour()*60*60 + eastern.Minute()*60 + eastern.Second()
	return seconds >= 15*60*60+55*60 && seconds < 16*60*60
}

func IsForcedExitWindow(now time.Time) bool {
	eastern, ok := eastern(now)
	if !ok ||
		eastern.Weekday() == time.Saturday ||
		eastern.Weekday() == time.Sunday {
		return false
	}
	seconds := eastern.Hour()*60*60 + eastern.Minute()*60 + eastern.Second()
	return seconds >= 19*60*60+55*60 && seconds < 20*60*60
}

func TradingDate(now time.Time) time.Time {
	eastern, ok := eastern(now)
	if !ok {
		return time.Time{}
	}
	year, month, day := eastern.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, eastern.Location())
}

func eastern(now time.Time) (time.Time, bool) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, false
	}
	return now.In(location), true
}

type Position struct {
	Ticker     string    `json:"ticker"`
	Quantity   float64   `json:"quantity"`
	EntryPrice float64   `json:"entry_price"`
	HighPrice  float64   `json:"high_price"`
	ActiveStop float64   `json:"active_stop"`
	CostFloor  float64   `json:"cost_floor"`
	EnteredAt  time.Time `json:"entered_at"`
	Exiting    bool      `json:"exiting"`
}

type ExitDecision struct {
	Exit       bool    `json:"exit"`
	LimitPrice float64 `json:"limit_price"`
	StopPrice  float64 `json:"stop_price"`
	Reason     string  `json:"reason"`
}

func EvaluateExit(
	config BoundaryConfig,
	position Position,
	bid float64,
	now time.Time,
) (Position, ExitDecision) {
	if position.EntryPrice <= 0 || position.Quantity <= 0 || bid <= 0 {
		return position, ExitDecision{}
	}
	position.HighPrice = max(position.HighPrice, bid, position.EntryPrice)
	hardStop := position.EntryPrice * (1 - config.StopLoss)
	position.ActiveStop = max(position.ActiveStop, hardStop)
	highGain := position.HighPrice/position.EntryPrice - 1
	if highGain >= config.TrailActivation {
		profitFloor := max(
			position.EntryPrice*(1+config.ProfitLockFloor),
			position.CostFloor,
		)
		trailing := position.HighPrice * (1 - config.TrailDistance)
		position.ActiveStop = max(
			position.ActiveStop,
			profitFloor,
			trailing,
		)
	}
	if IsForcedExitWindow(now) {
		return position, ExitDecision{
			Exit: true, LimitPrice: bid, StopPrice: position.ActiveStop,
			Reason: "END_OF_AFTER_HOURS",
		}
	}
	if bid > position.ActiveStop {
		return position, ExitDecision{
			StopPrice: position.ActiveStop,
		}
	}
	reason := "HARD_STOP"
	if highGain >= config.TrailActivation {
		reason = "PROFIT_LOCK_OR_TRAIL"
	}
	return position, ExitDecision{
		Exit: true, LimitPrice: bid, StopPrice: position.ActiveStop,
		Reason: reason,
	}
}

func FeeSafeCostFloor(
	config BoundaryConfig,
	entryPrice float64,
	quantity float64,
) float64 {
	if entryPrice <= 0 || quantity <= 0 {
		return 0
	}
	exitFee := max(
		config.ExitFeeMinimum,
		quantity*config.ExitFeePerShare,
	)
	exitFee = math.Ceil(exitFee*100) / 100
	reserve := entryPrice * quantity * config.SlippageReserve
	totalCosts := exitFee + reserve + config.MinimumNetProfit
	price := entryPrice + totalCosts/quantity
	scale := 100.0
	if price < 1 {
		scale = 10_000
	}
	return math.Ceil(price*scale) / scale
}

func finite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}
