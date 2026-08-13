package bracket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
)

// Feed is told which symbols currently have a live bracket. The service reports
// an open and a close as they happen, so nothing has to poll a table to discover
// what should be subscribed -- and a symbol stops costing a subscription the
// moment its last position closes.
//
// It is deliberately fire-and-forget. Opening a bracket must not fail because a
// market-data connection is unavailable: the position is already at the broker,
// and refusing to record it would be worse than protecting it a second late.
type Feed interface {
	Watch(ticker string)
	Unwatch(ticker string)
}

// Supervisor keeps exactly one price subscription alive per watched symbol and
// hands every print to a tick handler.
//
// Symbols are reference counted. Two brackets on one name share a subscription
// and the second close is the one that drops it, because a venue charges per
// subscription and a position that is still open must never lose its feed to a
// sibling's exit.
type Supervisor struct {
	provider marketdata.Provider
	handler  marketdata.TickHandler
	logger   *slog.Logger

	// lifetime bounds every subscription. It is held rather than passed because a
	// Watch call arrives from a request whose own context ends when the HTTP
	// handler returns, and a feed must outlive the click that started it.
	lifetime context.Context

	mutex   sync.Mutex
	watched map[string]*watch
	closed  bool
	running sync.WaitGroup
}

type watch struct {
	count  int
	cancel context.CancelFunc
}

// SupervisorOptions wires the supervisor. Lifetime, Provider and Handler are
// required.
type SupervisorOptions struct {
	// Lifetime is the process-scoped context every subscription derives from.
	Lifetime context.Context
	Provider marketdata.Provider
	Handler  marketdata.TickHandler
	Logger   *slog.Logger
}

func NewSupervisor(options SupervisorOptions) (*Supervisor, error) {
	if options.Lifetime == nil {
		return nil, errors.New("bracket supervisor requires a lifetime context")
	}
	if options.Provider == nil {
		return nil, errors.New("bracket supervisor requires a market data provider")
	}
	if options.Handler == nil {
		return nil, errors.New("bracket supervisor requires a tick handler")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{
		provider: options.Provider,
		handler:  options.Handler,
		logger:   logger,
		lifetime: options.Lifetime,
		watched:  make(map[string]*watch),
	}, nil
}

// Resume re-attaches a feed to positions that were already live when the process
// last stopped. It is the only read of the whole book the supervisor makes: after
// it returns, every change arrives through Watch and Unwatch.
func (supervisor *Supervisor) Resume(
	ctx context.Context, repository Repository, mode string,
) error {
	records, err := repository.OpenBrackets(ctx, mode)
	if err != nil {
		return fmt.Errorf("resuming bracket feeds: %w", err)
	}
	for _, record := range records {
		supervisor.Watch(record.Ticker)
	}
	supervisor.logger.Info(
		"bracket feeds resumed", "mode", mode, "brackets", len(records),
		"symbols", supervisor.Watching(),
	)
	return nil
}

// Watch subscribes to a symbol, or notes one more holder of an existing
// subscription. A failure to subscribe is logged rather than returned: the caller
// is a position that already exists, and the alternative to a late feed is no
// record of the trade at all.
func (supervisor *Supervisor) Watch(ticker string) {
	key := normaliseTicker(ticker)
	if key == "" {
		return
	}
	supervisor.mutex.Lock()
	if supervisor.closed {
		supervisor.mutex.Unlock()
		return
	}
	if existing, found := supervisor.watched[key]; found {
		existing.count++
		supervisor.mutex.Unlock()
		return
	}
	streamCtx, cancel := context.WithCancel(supervisor.lifetime)
	supervisor.watched[key] = &watch{count: 1, cancel: cancel}
	supervisor.mutex.Unlock()

	subscription, err := supervisor.provider.Subscribe(streamCtx, key)
	if err != nil {
		cancel()
		supervisor.drop(key)
		supervisor.logger.Error(
			"bracket feed did not start; the position is unprotected by trailing",
			"ticker", key, "error", err,
		)
		return
	}
	supervisor.running.Add(1)
	go supervisor.consume(streamCtx, key, subscription)
}

// Unwatch releases one holder of a symbol's subscription and cancels it when the
// last one lets go.
func (supervisor *Supervisor) Unwatch(ticker string) {
	key := normaliseTicker(ticker)
	if key == "" {
		return
	}
	supervisor.mutex.Lock()
	entry, found := supervisor.watched[key]
	if !found {
		supervisor.mutex.Unlock()
		return
	}
	entry.count--
	if entry.count > 0 {
		supervisor.mutex.Unlock()
		return
	}
	delete(supervisor.watched, key)
	supervisor.mutex.Unlock()
	entry.cancel()
}

// Watching reports how many symbols hold a subscription, for a health endpoint
// that needs to show the feed is attached to what it should be.
func (supervisor *Supervisor) Watching() int {
	supervisor.mutex.Lock()
	defer supervisor.mutex.Unlock()
	return len(supervisor.watched)
}

// Close cancels every subscription and waits for the readers to finish, so a
// shutdown cannot leave a goroutine handing ticks to a closed store.
func (supervisor *Supervisor) Close() error {
	supervisor.mutex.Lock()
	if supervisor.closed {
		supervisor.mutex.Unlock()
		return nil
	}
	supervisor.closed = true
	cancels := make([]context.CancelFunc, 0, len(supervisor.watched))
	for key, entry := range supervisor.watched {
		cancels = append(cancels, entry.cancel)
		delete(supervisor.watched, key)
	}
	supervisor.mutex.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	supervisor.running.Wait()
	return nil
}

// consume ranges the subscription and hands each print onward. A handler error is
// logged and the loop continues: one bracket the engine could not move is not a
// reason to stop watching the symbol it belongs to.
func (supervisor *Supervisor) consume(
	ctx context.Context, ticker string, subscription marketdata.Subscription,
) {
	defer supervisor.running.Done()
	defer func() { _ = subscription.Close() }()

	for tick := range subscription.Ticks() {
		if err := supervisor.handler(ctx, tick); err != nil {
			supervisor.logger.Error(
				"bracket engine refused a tick",
				"ticker", ticker, "price", tick.Price, "error", err,
			)
		}
	}
	if err := subscription.Err(); err != nil {
		supervisor.logger.Error(
			"bracket feed ended with an error; trailing has stopped for this symbol",
			"ticker", ticker, "error", err,
		)
		return
	}
	supervisor.logger.Info("bracket feed ended", "ticker", ticker)
}

// drop removes a symbol's entry without cancelling, for the path where the
// subscription never started.
func (supervisor *Supervisor) drop(ticker string) {
	supervisor.mutex.Lock()
	defer supervisor.mutex.Unlock()
	delete(supervisor.watched, ticker)
}

func normaliseTicker(ticker string) string {
	return strings.ToUpper(strings.TrimSpace(ticker))
}
