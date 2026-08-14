package bracket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

/* The bridge between a buy filling and a stop existing.
 *
 * It is a sweep over durable state, not a callback on a fill. A callback fires once
 * and is gone: a process that dies in the half-second between the fill and the stop
 * comes back knowing nothing, and the position it just opened is held with nothing
 * behind it and nothing looking for it. This reads the database instead, so the
 * work is picked up again by whoever is running a second later -- including a
 * different process, on a different machine, after a crash.
 *
 * The notification exists only to make it fast. Nudge is an optimisation; deleting
 * it would cost a second of latency and change no outcome.
 */
type EntryWatcher struct {
	service    *Service
	repository Repository
	orders     EntryOrders
	mode       string
	logger     *slog.Logger
	interval   time.Duration
	// retryAfter is how long an UNPROTECTED bracket waits before the stop is tried
	// again. Immediately would hammer a venue that is refusing for a reason -- an
	// unsettled fill, a session that takes no stops -- and each refusal costs a round
	// trip during the exact minutes the position is bare.
	retryAfter time.Duration
	// deadline is when a buy that has not filled is given up on. A limit that never
	// reaches its price is not a failure, but a plan left working overnight becomes a
	// position nobody decided to take when the market opens.
	deadline time.Duration
	now      func() time.Time
	wake     chan struct{}
}

// EntryWatcherOptions wires the watcher. Repository, orders and service are
// required; the durations have working defaults.
type EntryWatcherOptions struct {
	Service    *Service
	Repository Repository
	Orders     EntryOrders
	Mode       string
	Logger     *slog.Logger
	Interval   time.Duration
	RetryAfter time.Duration
	Deadline   time.Duration
	Now        func() time.Time
}

func NewEntryWatcher(options EntryWatcherOptions) (*EntryWatcher, error) {
	if options.Service == nil || options.Repository == nil || options.Orders == nil {
		return nil, errors.New(
			"an entry watcher needs a service, a repository and an order path: " +
				"without all three a filled buy is never protected",
		)
	}
	watcher := &EntryWatcher{
		service: options.Service, repository: options.Repository,
		orders: options.Orders, mode: options.Mode,
		logger:     options.Logger,
		interval:   options.Interval,
		retryAfter: options.RetryAfter,
		deadline:   options.Deadline,
		now:        options.Now,
		wake:       make(chan struct{}, 1),
	}
	if watcher.logger == nil {
		watcher.logger = slog.Default()
	}
	if watcher.interval <= 0 {
		watcher.interval = time.Second
	}
	if watcher.retryAfter <= 0 {
		watcher.retryAfter = 15 * time.Second
	}
	if watcher.deadline <= 0 {
		watcher.deadline = 6 * time.Hour
	}
	if watcher.now == nil {
		watcher.now = func() time.Time { return time.Now().UTC() }
	}
	return watcher, nil
}

// Nudge asks for a sweep now rather than at the next tick. It never blocks: the
// channel holds one, and a nudge that arrives while one is already pending is the
// same request twice.
func (watcher *EntryWatcher) Nudge() {
	select {
	case watcher.wake <- struct{}{}:
	default:
	}
}

// Run sweeps until the context ends. Every test drives Sweep directly; nothing in
// the suite waits on this timer.
func (watcher *EntryWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(watcher.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-watcher.wake:
		}
		if err := watcher.Sweep(ctx); err != nil && ctx.Err() == nil {
			watcher.logger.Error("the entry sweep failed", "error", err)
		}
	}
}

/* Sweep decides what every unfinished entry needs, and is the whole of the bridge.
 *
 * One bracket's trouble must not stop the others: a venue that will not answer about
 * one order is logged and skipped, because the next bracket in the list may be a
 * filled position waiting for a stop.
 */
func (watcher *EntryWatcher) Sweep(ctx context.Context) error {
	records, err := watcher.repository.UnsettledEntryBrackets(ctx, watcher.mode)
	if err != nil {
		return fmt.Errorf("reading unsettled entries: %w", err)
	}
	for _, record := range records {
		if record.EntryOrderRef == 0 {
			// Nothing to ask about. A bracket reaches this only by being written by
			// hand or by an older build, and asking about order zero would fail every
			// second for ever.
			continue
		}
		status, err := watcher.orders.Entry(ctx, record.EntryOrderRef)
		if err != nil {
			// A venue that will not answer is a reason to try again, not to abandon a
			// position. Logged once per sweep rather than escalated: the next tick is
			// a second away.
			watcher.logger.Warn(
				"could not read the entry order",
				"bracket_id", record.ID, "ticker", record.Ticker,
				"order_ref", record.EntryOrderRef, "error", err,
			)
			continue
		}
		if err := watcher.act(ctx, record, status); err != nil {
			watcher.logger.Error(
				"acting on an entry failed",
				"bracket_id", record.ID, "ticker", record.Ticker, "error", err,
			)
		}
	}
	return nil
}

func (watcher *EntryWatcher) act(
	ctx context.Context, record Record, status EntryStatus,
) error {
	switch {
	// Stock is held with nothing behind it. Nothing else in this function matters as
	// much, so it is answered first, and on every sweep until it stops being true.
	case record.State == StateUnprotected:
		if watcher.now().Sub(record.UpdatedAt) < watcher.retryAfter {
			return nil
		}
		_, err := watcher.service.ArmFromEntry(ctx, record.ID, EntryFill{
			Price:    status.AverageFillPrice,
			Quantity: status.FilledQuantity,
			Settled:  status.Done,
		})
		return err

	// Something filled. Protection goes on what is held now; if more fills later the
	// PROTECTED case below resizes it.
	case record.State == StateWorking && status.FilledQuantity > 0:
		_, err := watcher.service.ArmFromEntry(ctx, record.ID, EntryFill{
			Price:    status.AverageFillPrice,
			Quantity: status.FilledQuantity,
			Settled:  status.Done,
		})
		return err

	// The venue is finished and nothing filled. Not a failure -- a limit that never
	// reached its price is a limit doing its job -- so the plan goes back to being a
	// plan.
	case record.State == StateWorking && status.Done:
		_, err := watcher.service.AbandonEntry(
			ctx, record.ID, "the buy ended without filling: "+status.State,
		)
		return err

	// Still working, and out of time. Cancelling is a request; the case above books
	// the result once the venue confirms it.
	case record.State == StateWorking && watcher.overdue(record):
		watcher.logger.Warn(
			"giving up on an entry that has not filled",
			"bracket_id", record.ID, "ticker", record.Ticker,
			"sent_at", record.EntrySentAt, "deadline", watcher.deadline,
		)
		return watcher.orders.CancelEntry(ctx, record.EntryOrderRef)

	// More of the entry filled after the protection went on. The stop now covers
	// less stock than is held, and the difference is guarded by nothing.
	case record.State == StateProtected &&
		status.FilledQuantity > record.Quantity:
		_, err := watcher.service.ResizeProtection(ctx, record.ID, EntryFill{
			Price:    status.AverageFillPrice,
			Quantity: status.FilledQuantity,
			Settled:  status.Done,
		})
		return err

	// Protected, and the venue is done with the buy. Stop asking.
	case record.State == StateProtected && status.Done:
		_, err := watcher.repository.SaveEntryLink(ctx, record.ID, EntryLink{
			Ref: record.EntryOrderRef, ClientOrderID: record.EntryOrderID,
			State: StateProtected, Settled: true,
		}, AdjustmentRecord{
			BracketID: record.ID, Trigger: TriggerEntrySent,
			LastPrice: record.EntryPrice, HighWater: record.HighWater,
			Applied: true,
			Reason: fmt.Sprintf(
				"the entry is complete at %.0f shares", status.FilledQuantity,
			),
		})
		return err
	}
	return nil
}

func (watcher *EntryWatcher) overdue(record Record) bool {
	if record.EntrySentAt == nil {
		return false
	}
	return watcher.now().Sub(*record.EntrySentAt) > watcher.deadline
}
