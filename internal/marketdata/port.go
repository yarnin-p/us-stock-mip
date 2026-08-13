package marketdata

import "context"

// Provider is a venue that can be watched. Subscribe takes one symbol because
// that is the unit the domain reasons about — a position is opened and closed one
// name at a time, and tying a subscription's lifetime to a position's makes
// "stop paying for this feed" a cancel rather than a set difference computed
// somewhere else. An adapter that must talk to its venue in batches is free to
// multiplex internally; that is its problem, not the caller's.
type Provider interface {
	Subscribe(ctx context.Context, ticker string) (Subscription, error)
}

// Subscription is a live feed for one symbol, shaped like the standard library's
// scanners: range the channel, then ask why it ended.
//
// Errors do not travel on the channel. A tick is a price and nothing else, so a
// consumer ranging over Ticks never has to unwrap a union type on the hot path.
// A transport failure closes the channel and is reported by Err, which means the
// loop that reads prices and the code that handles outages stay separate.
type Subscription interface {
	// Ticks delivers every print until the feed ends, then closes. Calling it
	// twice returns the same channel.
	Ticks() <-chan Tick
	// Err reports why Ticks closed. It is nil for a clean shutdown — a cancelled
	// context or a Close — and is only meaningful once the channel is drained.
	Err() error
	// Close releases the subscription. It is safe to call more than once, so a
	// deferred Close beside an explicit one is not a bug.
	Close() error
}

// TickHandler consumes one print. It names the shape an orchestrator exposes so a
// composition root can hand a subscription to one without either side importing
// the other.
type TickHandler func(ctx context.Context, tick Tick) error

// Source answers the price of a symbol on demand, for the read paths that want a
// number rather than a stream — a screen rendering an open position, say. It is
// deliberately separate from Provider: an orchestrator that pulls prices has put
// the venue's cadence inside itself, and no orchestrator should depend on this.
type Source interface {
	LastPrice(ctx context.Context, ticker string) (float64, error)
}
