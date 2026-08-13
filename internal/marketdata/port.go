package marketdata

import "context"

// Source answers the price of a symbol on demand. Its signature matches the
// quote port the bracket engine already declares, so an adapter written against
// this package satisfies that engine without the engine changing.
type Source interface {
	LastPrice(ctx context.Context, ticker string) (float64, error)
}

// TickHandler receives one observed print. Returning an error tells the streamer
// the tick was not consumed; a streamer logs and continues rather than dropping
// the subscription, because one engine refusing a tick is not a feed failure.
type TickHandler func(ctx context.Context, tick Tick) error

// Streamer pushes prints as they happen for a chosen set of symbols.
//
// Subscribe replaces the set rather than adding to it. Venues bill and throttle
// by subscription count, and the caller — which knows exactly which positions
// are live — is the only layer that can keep the set to the minimum. An additive
// API would leave symbols subscribed after the position closed, and nothing
// downstream would know they were stale.
type Streamer interface {
	// Subscribe makes symbols the complete set this streamer follows. Passing an
	// empty set is legal and means "follow nothing", which is what a flat book
	// should cost.
	Subscribe(ctx context.Context, tickers []string) error
	// OnTick registers the handler invoked for every print. Calling it a second
	// time replaces the handler, so a restart cannot end up with two engines
	// ratcheting the same stop.
	OnTick(handler TickHandler)
	// Run drives the subscription until ctx ends. It returns nil on a clean
	// shutdown so a caller can distinguish "asked to stop" from "feed died".
	Run(ctx context.Context) error
}

// StreamingSource is the shape an adapter reaches when it serves both halves of
// the contract: a live subscription that also answers on demand from its own
// most recent tick. Orchestrators should depend on Source or Streamer, never on
// this composite — it exists to name what a complete adapter provides.
type StreamingSource interface {
	Source
	Streamer
}
