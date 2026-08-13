// Package marketdata owns the price feed contract the trading orchestrators
// depend on, so a running strategy never names the venue it is reading from.
//
// The contract is deliberately two shapes rather than one. A trailing engine
// woken by a timer asks "what is it now" and wants a pull; an engine that must
// not miss a high needs every print pushed to it. Both are the same feed, and
// an adapter is expected to serve both: subscribe once, cache the newest tick,
// answer LastPrice from that cache, and hand the tick onward. Splitting the two
// into separate packages would force a caller to choose a venue twice.
//
// The book is out of scope on purpose. Trailing follows price, and a port that
// promised depth would oblige every adapter — including the synthetic one used
// in tests — to invent it.
package marketdata

import (
	"errors"
	"math"
	"time"
)

// Tick is one observed price for one symbol. It carries the observation time
// because a feed that reconnects can deliver an older print after a newer one,
// and only the consumer knows whether that matters.
type Tick struct {
	Ticker     string    `json:"ticker"`
	Price      float64   `json:"price"`
	ObservedAt time.Time `json:"observed_at"`
}

// ErrNoPrice reports that a symbol is subscribed but has not printed yet. It is
// distinct from a transport failure: the caller should wait, not retry or
// reconnect.
var ErrNoPrice = errors.New("marketdata: no price observed yet")

// Validate rejects a tick that would corrupt a high-water mark. A zero,
// negative, infinite or NaN price must never reach an engine that ratchets a
// stop from it.
func (tick Tick) Validate() error {
	if tick.Ticker == "" {
		return errors.New("marketdata: tick has no ticker")
	}
	if tick.Price <= 0 || math.IsInf(tick.Price, 0) || math.IsNaN(tick.Price) {
		return errors.New("marketdata: tick price must be a positive number")
	}
	if tick.ObservedAt.IsZero() {
		return errors.New("marketdata: tick has no observation time")
	}
	return nil
}

// Newer reports whether this tick supersedes other. Ticks for different symbols
// are never comparable, so the answer is false rather than an error: a fan-out
// loop should skip, not stop.
func (tick Tick) Newer(other Tick) bool {
	if tick.Ticker != other.Ticker {
		return false
	}
	return tick.ObservedAt.After(other.ObservedAt)
}
