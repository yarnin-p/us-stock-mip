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
