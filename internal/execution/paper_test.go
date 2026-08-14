package execution

import (
	"context"
	"testing"
)

func TestPaperAdapterAppliesConservativeSellFees(t *testing.T) {
	adapter, err := NewPaperAdapterWithFees(0.03, 0.006)
	if err != nil {
		t.Fatal(err)
	}
	buy, err := adapter.PreviewOrder(context.Background(), BrokerOrderRequest{
		Side: "BUY", Quantity: 10, LimitPrice: 5,
	})
	if err != nil || buy.EstimatedFee != 0 {
		t.Fatalf("buy preview = %#v err=%v", buy, err)
	}
	sellRequest := BrokerOrderRequest{
		ClientOrderID: "paper-fee", Side: "SELL",
		Quantity: 10, LimitPrice: 5,
	}
	sell, err := adapter.PreviewOrder(context.Background(), sellRequest)
	if err != nil || sell.EstimatedFee != 0.06 {
		t.Fatalf("sell preview = %#v err=%v", sell, err)
	}
	submission, err := adapter.PlaceOrder(context.Background(), sellRequest)
	if err != nil || len(submission.Fills) != 1 ||
		submission.Fills[0].Fee != 0.06 {
		t.Fatalf("sell submission = %#v err=%v", submission, err)
	}
	minimum, err := adapter.PreviewOrder(
		context.Background(),
		BrokerOrderRequest{Side: "SELL", Quantity: 1, LimitPrice: 5},
	)
	if err != nil || minimum.EstimatedFee != 0.03 {
		t.Fatalf("minimum-fee preview = %#v err=%v", minimum, err)
	}
}

func TestPaperAdapterRejectsInvalidFeeConfiguration(t *testing.T) {
	if _, err := NewPaperAdapterWithFees(-0.01, 0.006); err == nil {
		t.Fatal("expected negative minimum fee to fail")
	}
	if _, err := NewPaperAdapterWithFees(0.03, -0.006); err == nil {
		t.Fatal("expected negative per-share fee to fail")
	}
}

// The fake venue only means something if it waits. Before Observe existed it filled
// every limit on arrival and never fired a stop, so a protective target was born
// filled and the exits -- the only part worth forward testing -- were the one part
// that could not be.
func TestThePaperVenueRestsAProtectiveOrderUntilThePriceReachesIt(t *testing.T) {
	adapter := NewPaperAdapter()
	// A price first, so the venue is watching rather than taking the order on trust.
	adapter.Observe("RMCF", 1.58)

	if _, err := adapter.PlaceOrder(context.Background(), BrokerOrderRequest{
		AccountID: "a", ClientOrderID: "bracket-1-stop", Ticker: "RMCF",
		Side: "SELL", OrderType: "STOP_LOSS", TimeInForce: "GTC",
		Quantity: 1700, StopPrice: 1.50,
	}); err != nil {
		t.Fatalf("place stop: %v", err)
	}
	if _, err := adapter.PlaceOrder(context.Background(), BrokerOrderRequest{
		AccountID: "a", ClientOrderID: "bracket-1-target", Ticker: "RMCF",
		Side: "SELL", OrderType: "LIMIT", TimeInForce: "GTC",
		Quantity: 1700, LimitPrice: 2.00,
	}); err != nil {
		t.Fatalf("place target: %v", err)
	}
	for _, id := range []string{"bracket-1-stop", "bracket-1-target"} {
		outcome, err := adapter.OrderOutcome(context.Background(), "a", id)
		if err != nil {
			t.Fatal(err)
		}
		if !outcome.Working || outcome.Filled {
			t.Fatalf("%s is %+v on arrival; a protective order has to rest", id, outcome)
		}
	}

	// Up, but not to the target.
	adapter.Observe("RMCF", 1.90)
	if outcome, _ := adapter.OrderOutcome(
		context.Background(), "a", "bracket-1-target",
	); outcome.Filled {
		t.Fatal("the target filled at 1.90 without reaching 2.00")
	}

	// Down through the stop.
	adapter.Observe("RMCF", 1.44)
	outcome, err := adapter.OrderOutcome(context.Background(), "a", "bracket-1-stop")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Filled || outcome.Working {
		t.Fatalf("stop = %+v after a print through it", outcome)
	}
	// A stop fills at the print, not at its level. Pretending otherwise makes every
	// paper run flatter than reality, which is the opposite of useful.
	if outcome.FilledPrice != 1.44 {
		t.Fatalf("stop filled at %v, want the print at 1.44", outcome.FilledPrice)
	}
	if outcome.FilledQuantity != 1700 {
		t.Fatalf("stop filled %v of 1700", outcome.FilledQuantity)
	}
}

// Trailing that does not move the resting order is trailing that changes nothing
// about the price the run exits at.
func TestAmendingAPaperStopMovesWhereItActuallyFills(t *testing.T) {
	adapter := NewPaperAdapter()
	adapter.Observe("RMCF", 1.58)
	if _, err := adapter.PlaceOrder(context.Background(), BrokerOrderRequest{
		AccountID: "a", ClientOrderID: "bracket-2-stop", Ticker: "RMCF",
		Side: "SELL", OrderType: "STOP_LOSS", TimeInForce: "GTC",
		Quantity: 100, StopPrice: 1.40,
	}); err != nil {
		t.Fatalf("place: %v", err)
	}
	// The engine ratchets it up under a new high.
	if _, err := adapter.ModifyOrder(context.Background(), ModifyOrderRequest{
		AccountID: "a", ClientOrderID: "bracket-2-stop", Ticker: "RMCF",
		OrderType: "STOP_LOSS", TimeInForce: "GTC", Quantity: 100, StopPrice: 1.70,
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	// A price that the old stop would have survived and the new one must not.
	adapter.Observe("RMCF", 1.65)
	outcome, err := adapter.OrderOutcome(context.Background(), "a", "bracket-2-stop")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Filled {
		t.Fatal("the amended stop did not fire; trailing had no effect on the exit")
	}
}

// With no feed there is nothing to wait for, and an order resting forever would be
// worse than one filled at the price it named. This is what keeps the existing
// entry flows working.
func TestAnUnwatchedPaperVenueStillFillsOnRequest(t *testing.T) {
	adapter := NewPaperAdapter()
	submission, err := adapter.PlaceOrder(context.Background(), BrokerOrderRequest{
		AccountID: "a", ClientOrderID: "entry-1", Ticker: "BIVI",
		Side: "BUY", OrderType: "LIMIT", TimeInForce: "DAY",
		Quantity: 100, LimitPrice: 2.92,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if submission.State != StateFilled || len(submission.Fills) != 1 {
		t.Fatalf("submission = %+v, want an immediate fill", submission)
	}
}

// An order the market has already passed fills now rather than resting behind it.
func TestAPaperOrderAlreadyThroughItsPriceFillsAtOnce(t *testing.T) {
	adapter := NewPaperAdapter()
	adapter.Observe("BIVI", 2.50)
	submission, err := adapter.PlaceOrder(context.Background(), BrokerOrderRequest{
		AccountID: "a", ClientOrderID: "entry-2", Ticker: "BIVI",
		Side: "BUY", OrderType: "LIMIT", TimeInForce: "DAY",
		Quantity: 100, LimitPrice: 2.92,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if submission.State != StateFilled {
		t.Fatalf("a buy limit above the market should fill: %+v", submission)
	}
}
