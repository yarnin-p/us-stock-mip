package burst

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
)

/* The scanner: a watchlist, a feed, and the detector between them.
 *
 * It subscribes to every name on the list and pushes each print through the detector.
 * That is the whole job. It does not rank, score, or decide -- an alert is a claim
 * that something is moving right now, and what to do about it belongs to the person
 * reading it.
 *
 * The watchlist is refreshed on a timer rather than held for the session. Names enter
 * and leave the book as prices and volumes change, and a list pinned at start-up
 * slowly stops describing the market it is watching.
 */

// Watchlist answers which names to watch. Declared here beside its consumer, phrased
// in this package's words: it asks for symbols, not for a query.
type Watchlist interface {
	Symbols(ctx context.Context, limit int) ([]string, error)
}

// Sink receives alerts. Errors are the sink's own business -- a screen that failed to
// draw must not stop the next symbol being watched -- so this returns nothing.
type Sink interface {
	Burst(Alert)
}

// SinkFunc adapts a function to a Sink.
type SinkFunc func(Alert)

func (fn SinkFunc) Burst(alert Alert) { fn(alert) }

type ScannerOptions struct {
	Provider  marketdata.Provider
	Watchlist Watchlist
	Detector  *Detector
	Sink      Sink
	Logger    *slog.Logger
	// Limit caps the watchlist. The measured list is a few hundred names; the cap is
	// here so a filter that stops filtering cannot quietly subscribe to the whole
	// tape and exhaust the venue's quota.
	Limit int
	// Refresh is how often the watchlist is rebuilt. Default 15 minutes.
	Refresh time.Duration
}

type Scanner struct {
	provider  marketdata.Provider
	watchlist Watchlist
	detector  *Detector
	sink      Sink
	logger    *slog.Logger
	limit     int
	refresh   time.Duration

	mutex   sync.Mutex
	running map[string]context.CancelFunc
	alerts  int
}

func NewScanner(options ScannerOptions) (*Scanner, error) {
	if options.Provider == nil || options.Watchlist == nil ||
		options.Detector == nil || options.Sink == nil {
		return nil, errors.New(
			"a burst scanner needs a feed, a watchlist, a detector and somewhere to " +
				"put the alerts: without all four it watches nothing or tells nobody",
		)
	}
	scanner := &Scanner{
		provider: options.Provider, watchlist: options.Watchlist,
		detector: options.Detector, sink: options.Sink,
		logger: options.Logger, limit: options.Limit, refresh: options.Refresh,
		running: make(map[string]context.CancelFunc),
	}
	if scanner.logger == nil {
		scanner.logger = slog.Default()
	}
	if scanner.limit <= 0 {
		scanner.limit = 500
	}
	if scanner.refresh <= 0 {
		scanner.refresh = 15 * time.Minute
	}
	return scanner, nil
}

// Run watches until the context ends.
func (scanner *Scanner) Run(ctx context.Context) {
	if err := scanner.Sync(ctx); err != nil && ctx.Err() == nil {
		scanner.logger.Error("could not build the first watchlist", "error", err)
	}
	ticker := time.NewTicker(scanner.refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			scanner.stopAll()
			return
		case <-ticker.C:
			if err := scanner.Sync(ctx); err != nil && ctx.Err() == nil {
				scanner.logger.Error("could not refresh the watchlist", "error", err)
			}
		}
	}
}

/* Sync brings the running subscriptions into line with the watchlist.
 *
 * Names already being watched are left alone rather than torn down and rebuilt: a
 * resubscription drops whatever the detector had learned about that symbol's recent
 * low, and the moment after a refresh is exactly when a burst would be missed.
 */
func (scanner *Scanner) Sync(ctx context.Context) error {
	symbols, err := scanner.watchlist.Symbols(ctx, scanner.limit)
	if err != nil {
		return fmt.Errorf("reading the watchlist: %w", err)
	}
	if len(symbols) == 0 {
		return errors.New(
			"the watchlist is empty; nothing is being watched and no alert can fire",
		)
	}
	wanted := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		wanted[symbol] = true
	}

	scanner.mutex.Lock()
	var added, dropped int
	for symbol, stop := range scanner.running {
		if wanted[symbol] {
			continue
		}
		stop()
		delete(scanner.running, symbol)
		scanner.detector.Forget(symbol)
		dropped++
	}
	for symbol := range wanted {
		if _, ok := scanner.running[symbol]; ok {
			continue
		}
		symbolCtx, stop := context.WithCancel(ctx)
		scanner.running[symbol] = stop
		added++
		go scanner.watch(symbolCtx, symbol)
	}
	watching := len(scanner.running)
	scanner.mutex.Unlock()

	scanner.logger.Info(
		"burst watchlist synchronised",
		"watching", watching, "added", added, "dropped", dropped,
	)
	return nil
}

func (scanner *Scanner) watch(ctx context.Context, symbol string) {
	subscription, err := scanner.provider.Subscribe(ctx, symbol)
	if err != nil {
		if ctx.Err() == nil {
			scanner.logger.Warn("could not subscribe", "ticker", symbol, "error", err)
		}
		scanner.mutex.Lock()
		delete(scanner.running, symbol)
		scanner.mutex.Unlock()
		return
	}
	for tick := range subscription.Ticks() {
		alert := scanner.detector.Observe(tick.Ticker, tick.Price, tick.ObservedAt)
		if alert == nil {
			continue
		}
		scanner.mutex.Lock()
		scanner.alerts++
		scanner.mutex.Unlock()
		scanner.logger.Info(
			"burst",
			"ticker", alert.Ticker, "gain", alert.Gain, "price", alert.Price,
			"low", alert.Low, "took", alert.At.Sub(alert.LowAt).String(),
		)
		scanner.sink.Burst(*alert)
	}
	if err := subscription.Err(); err != nil && ctx.Err() == nil {
		scanner.logger.Warn("a feed ended", "ticker", symbol, "error", err)
	}
}

func (scanner *Scanner) stopAll() {
	scanner.mutex.Lock()
	defer scanner.mutex.Unlock()
	for symbol, stop := range scanner.running {
		stop()
		delete(scanner.running, symbol)
	}
}

// Health reports what the scanner is doing, for a screen that has to be able to say
// whether silence means a quiet market or a dead feed.
func (scanner *Scanner) Health() (watching int, alerts int) {
	scanner.mutex.Lock()
	defer scanner.mutex.Unlock()
	return len(scanner.running), scanner.alerts
}
