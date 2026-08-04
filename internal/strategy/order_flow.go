package strategy

import (
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

type TradeTick struct {
	Ticker     string
	Price      float64
	Size       float64
	Side       string
	ObservedAt time.Time
}

type OrderFlow struct {
	QuoteUpdates          int     `json:"quote_updates"`
	TradeTicks            int     `json:"trade_ticks"`
	AggressiveBuyRatio    float64 `json:"aggressive_buy_ratio"`
	ClassifiedVolumeRatio float64 `json:"classified_volume_ratio"`
	UptickRatio           float64 `json:"uptick_ratio"`
	AverageBookPressure   float64 `json:"average_book_pressure"`
	PriceVelocity         float64 `json:"price_velocity"`
}

type OrderFlowTracker struct {
	mutex  sync.Mutex
	window time.Duration
	quotes []Quote
	trades []TradeTick
}

func NewOrderFlowTracker(window time.Duration) (*OrderFlowTracker, error) {
	if window <= 0 {
		return nil, ErrInvalidOrderFlowWindow
	}
	return &OrderFlowTracker{window: window}, nil
}

func (tracker *OrderFlowTracker) ObserveQuote(quote Quote) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.quotes = append(tracker.quotes, quote)
	slices.SortStableFunc(tracker.quotes, func(left, right Quote) int {
		return left.ObservedAt.Compare(right.ObservedAt)
	})
	tracker.prune(quote.ObservedAt)
}

func (tracker *OrderFlowTracker) ObserveTrade(tick TradeTick) bool {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	for _, existing := range tracker.trades {
		if existing.ObservedAt.Equal(tick.ObservedAt) &&
			existing.Price == tick.Price &&
			existing.Size == tick.Size &&
			strings.EqualFold(existing.Side, tick.Side) {
			return false
		}
	}
	tracker.trades = append(tracker.trades, tick)
	slices.SortStableFunc(tracker.trades, func(left, right TradeTick) int {
		return left.ObservedAt.Compare(right.ObservedAt)
	})
	tracker.prune(tick.ObservedAt)
	return true
}

func (tracker *OrderFlowTracker) Snapshot(now time.Time) OrderFlow {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.prune(now)
	result := OrderFlow{
		QuoteUpdates: len(tracker.quotes),
		TradeTicks:   len(tracker.trades),
	}
	for _, quote := range tracker.quotes {
		result.AverageBookPressure += quote.BidSize /
			max(quote.BidSize+quote.AskSize, 1)
	}
	if len(tracker.quotes) > 0 {
		result.AverageBookPressure /= float64(len(tracker.quotes))
	}
	var buyVolume, sellVolume, totalVolume float64
	lastSide := ""
	for index, tick := range tracker.trades {
		if tick.Size > 0 {
			totalVolume += tick.Size
		}
		side := normalizeTradeSide(tick.Side)
		if side == "" && index > 0 {
			previous := tracker.trades[index-1].Price
			switch {
			case tick.Price > previous:
				side = "BUY"
			case tick.Price < previous:
				side = "SELL"
			default:
				side = lastSide
			}
		}
		switch side {
		case "BUY":
			buyVolume += tick.Size
		case "SELL":
			sellVolume += tick.Size
		}
		if side != "" {
			lastSide = side
		}
	}
	if totalVolume > 0 {
		labeledVolume := buyVolume + sellVolume
		result.AggressiveBuyRatio = buyVolume / totalVolume
		result.ClassifiedVolumeRatio = labeledVolume / totalVolume
	}
	if len(tracker.trades) > 1 {
		var upticks, changes float64
		for index := 1; index < len(tracker.trades); index++ {
			previous := tracker.trades[index-1].Price
			current := tracker.trades[index].Price
			if current == previous {
				continue
			}
			changes++
			if current > previous {
				upticks++
			}
		}
		if changes > 0 {
			result.UptickRatio = upticks / changes
		}
		first := tracker.trades[0].Price
		last := tracker.trades[len(tracker.trades)-1].Price
		if first > 0 {
			result.PriceVelocity = last/first - 1
		}
	}
	return result
}

func (tracker *OrderFlowTracker) prune(now time.Time) {
	cutoff := now.Add(-tracker.window)
	firstQuote := 0
	for firstQuote < len(tracker.quotes) &&
		tracker.quotes[firstQuote].ObservedAt.Before(cutoff) {
		firstQuote++
	}
	tracker.quotes = tracker.quotes[firstQuote:]
	firstTrade := 0
	for firstTrade < len(tracker.trades) &&
		tracker.trades[firstTrade].ObservedAt.Before(cutoff) {
		firstTrade++
	}
	tracker.trades = tracker.trades[firstTrade:]
}

func normalizeTradeSide(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "B", "BUY", "BUYER", "BID":
		return "BUY"
	case "S", "SELL", "SELLER", "ASK":
		return "SELL"
	default:
		return ""
	}
}

var ErrInvalidOrderFlowWindow = &orderFlowError{
	message: "order-flow window must be positive",
}

type orderFlowError struct{ message string }

func (err *orderFlowError) Error() string { return err.message }

func finiteOrderFlow(flow OrderFlow) bool {
	values := []float64{
		flow.AggressiveBuyRatio,
		flow.ClassifiedVolumeRatio,
		flow.UptickRatio,
		flow.AverageBookPressure,
		flow.PriceVelocity,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return flow.QuoteUpdates >= 0 && flow.TradeTicks >= 0
}
