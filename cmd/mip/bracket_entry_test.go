package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/bracket"
	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

/* A refusal has to come back as a refusal, from whichever step refused.
 *
 * The gate runs at create, at preview and at approve, and it measures against the
 * account as it stands rather than as it stood a moment earlier -- so an order can
 * pass one step and be turned down at the next. That is not a corner case; it is
 * what happened the first time this was pointed at a real account, where the buy was
 * created and then refused at preview for a position that already existed.
 *
 * The previewed order was being discarded -- `if _, err := Preview(...)` -- so the
 * refusal walked into Approve and came back as "invalid order transition REJECTED ->
 * APPROVED". That is a state machine complaining about itself. An operator reading it
 * learns nothing about which ceiling they hit or what to change, which is the only
 * thing a refusal is for.
 */
func TestBuyReportsARefusalThatArrivesAtPreview(t *testing.T) {
	ctx := context.Background()
	venue, err := execution.NewPaperAdapterWithFees(0, 0)
	if err != nil {
		t.Fatalf("venue: %v", err)
	}
	// Room at create, none by preview. The account moved in between, which is the
	// whole reason the gate runs more than once.
	repository := &memoryOrders{
		order: &execution.Order{},
		snapshots: []execution.RiskSnapshot{
			{
				SymbolExists: true, Session: "REGULAR",
				BuyingPower: 100_000, PortfolioEquity: 100_000,
			},
			{
				SymbolExists: true, Session: "REGULAR",
				BuyingPower: 1, PortfolioEquity: 100_000,
			},
		},
	}
	service, err := execution.NewService(repository, execution.ServiceOptions{
		Mode: execution.ModePaper, PaperAdapter: venue,
		Limits: execution.Limits{
			MaxPositionValue: 5_000, MaxCapitalAllocation: 0.9,
			MaxDailyLoss: 500, MaxRiskPerTrade: 500, ApprovalTTL: time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}

	ticket, err := bracketEntryOrders{execution: service}.Buy(ctx, bracket.EntryRequest{
		Ticker: "NVDA", Quantity: 4, LimitPrice: 228,
		TimeInForce: "DAY", Reason: "bracket entry test", ScaleIn: true,
	})
	if err != nil {
		if strings.Contains(err.Error(), "invalid order transition") {
			t.Fatalf(
				"a refusal came back as a state machine complaint: %v -- an operator "+
					"reading that cannot tell which ceiling they hit", err,
			)
		}
		t.Fatalf("buy: %v", err)
	}
	if len(ticket.Refusals) == 0 {
		t.Fatal("the gate refused at preview and nothing said why")
	}
	if ticket.Ref != 0 {
		t.Fatalf("a refused entry came back with order ref %d", ticket.Ref)
	}
}

/* The refusal every account will meet first: the ceiling is already too low when the
 * order is created. Kept beside the preview case because they take different routes
 * out of Buy and only one of them was ever covered. */
func TestBuyReportsARefusalThatArrivesAtCreate(t *testing.T) {
	ctx := context.Background()
	venue, err := execution.NewPaperAdapterWithFees(0, 0)
	if err != nil {
		t.Fatalf("venue: %v", err)
	}
	repository := &memoryOrders{order: &execution.Order{}}
	service, err := execution.NewService(repository, execution.ServiceOptions{
		Mode: execution.ModePaper, PaperAdapter: venue,
		Limits: execution.Limits{
			MaxPositionValue: 5, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500, MaxRiskPerTrade: 5, ApprovalTTL: time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	ticket, err := bracketEntryOrders{execution: service}.Buy(ctx, bracket.EntryRequest{
		Ticker: "NVDA", Quantity: 4, LimitPrice: 228, TimeInForce: "DAY",
	})
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	if len(ticket.Refusals) == 0 {
		t.Fatal("the gate refused at create and nothing said why")
	}
}

/* The execution service's repository, in memory.
 *
 * Here rather than in a shared helper because the code under test is the adapter in
 * this package, and a test that needs a database is a test that stops being run.
 */
type memoryOrders struct {
	order *execution.Order
	// snapshots are handed out in order, and the last one repeats. That is how the
	// account moving between two gate checks is expressed without a clock or a race.
	snapshots []execution.RiskSnapshot
	asked     int
}

func (orders *memoryOrders) RiskSnapshot(
	context.Context, string, execution.Mode, string,
) (execution.RiskSnapshot, error) {
	if len(orders.snapshots) == 0 {
		return execution.RiskSnapshot{
			SymbolExists: true, Session: "REGULAR",
			BuyingPower: 100_000, PortfolioEquity: 100_000,
		}, nil
	}
	index := orders.asked
	if index >= len(orders.snapshots) {
		index = len(orders.snapshots) - 1
	}
	orders.asked++
	return orders.snapshots[index], nil
}

func (orders *memoryOrders) DefaultBrokerAccount(context.Context) (string, error) {
	return "acct-1", nil
}

func (orders *memoryOrders) CreateExecutionOrder(
	_ context.Context, order execution.Order, _ execution.Transition,
) (execution.Order, error) {
	order.ID = 1
	*orders.order = order
	return order, nil
}

func (orders *memoryOrders) ExecutionOrder(
	_ context.Context, _ int64,
) (execution.Order, error) {
	return *orders.order, nil
}

func (orders *memoryOrders) ExecutionOrders(
	context.Context,
) ([]execution.Order, error) {
	return []execution.Order{*orders.order}, nil
}

func (orders *memoryOrders) ExecutionTransitions(
	context.Context, int64,
) ([]execution.Transition, error) {
	return nil, nil
}

func (orders *memoryOrders) TransitionExecutionOrder(
	_ context.Context, order execution.Order, _ execution.State,
	_ execution.Transition, _ *execution.Fill,
) (execution.Order, error) {
	*orders.order = order
	return order, nil
}

func (orders *memoryOrders) ExecutionPositions(
	context.Context,
) ([]execution.Position, error) {
	return nil, nil
}

func (orders *memoryOrders) ExecutionTransactions(
	context.Context,
) ([]execution.Transaction, error) {
	return nil, nil
}

func (orders *memoryOrders) ExecutionDailyPnL(
	context.Context,
) ([]execution.DailyPnL, error) {
	return nil, nil
}
