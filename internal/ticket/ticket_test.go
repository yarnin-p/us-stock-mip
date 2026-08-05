package ticket_test

import (
	"strings"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/ticket"
)

func limits() ticket.Limits {
	return ticket.Limits{
		MaxRiskPerTicket: 2000, MaxNotional: 40000,
		MinSharePrice: 0.20, MaxSharePrice: 100,
		MaxStopDistance: 0.10, MaxDailyLoss: 6000, MaxTicketsPerDay: 6,
	}
}

func TestBuildSizesFromTheRiskBudget(t *testing.T) {
	got, err := ticket.Build(ticket.Request{
		Ticker: "abcd", Side: ticket.SideBuy,
		Entry: 10, Stop: 9.50, Target: 11.50, RiskAmount: 2000,
	}, limits())
	if err != nil {
		t.Fatal(err)
	}
	// 2000 risk over 0.50 a share is 4000 shares, but 40000 of notional only
	// covers 4000 at 10 — both bind at once, so the cap must be reported.
	if got.Shares != 4000 {
		t.Fatalf("shares = %d, want 4000", got.Shares)
	}
	if got.RiskPerShare != 0.5 || got.ActualRisk != 2000 {
		t.Fatalf("risk = %v per share, %v total", got.RiskPerShare, got.ActualRisk)
	}
	if got.RewardRisk != 3 {
		t.Fatalf("reward:risk = %v, want 3", got.RewardRisk)
	}
	if got.Ticker != "ABCD" {
		t.Fatalf("ticker = %q, want normalised", got.Ticker)
	}
}

// The rounded share count risks slightly less than the budget. Reporting the
// requested figure would overstate how much of the day's allowance is spent.
func TestBuildReportsTheRiskTheSharesActuallyCarry(t *testing.T) {
	got, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 10, Stop: 9.70, RiskAmount: 1000,
	}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Shares != 3333 {
		t.Fatalf("shares = %d, want 3333", got.Shares)
	}
	if got.ActualRisk >= got.RiskAmount {
		t.Fatalf("actual risk %v must sit under the budget %v",
			got.ActualRisk, got.RiskAmount)
	}
}

// The stop is the leg that never gets placed in time by hand. A ticket without
// one defeats the entire purpose, so it is refused rather than defaulted.
func TestBuildRefusesATicketWithoutAStop(t *testing.T) {
	_, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy, Entry: 10, RiskAmount: 500,
	}, limits())
	if err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("error = %v, want a stop to be required", err)
	}
}

func TestBuildRefusesAStopOnTheWrongSide(t *testing.T) {
	if _, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 10, Stop: 10.50, RiskAmount: 500,
	}, limits()); err == nil {
		t.Fatal("a long stop above the entry must be refused")
	}
	if _, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideSell,
		Entry: 10, Stop: 9.50, RiskAmount: 500,
	}, limits()); err == nil {
		t.Fatal("a short stop below the entry must be refused")
	}
}

func TestBuildEnforcesEveryCeiling(t *testing.T) {
	cases := map[string]struct {
		request ticket.Request
		mutate  func(*ticket.Limits)
		wants   string
	}{
		"risk per ticket": {
			request: ticket.Request{
				Ticker: "ABCD", Side: ticket.SideBuy,
				Entry: 10, Stop: 9.5, RiskAmount: 5000,
			},
			wants: "per-ticket ceiling",
		},
		"stop too far": {
			request: ticket.Request{
				Ticker: "ABCD", Side: ticket.SideBuy,
				Entry: 10, Stop: 8, RiskAmount: 500,
			},
			wants: "ceiling",
		},
		"share price floor": {
			request: ticket.Request{
				Ticker: "ABCD", Side: ticket.SideBuy,
				Entry: 0.10, Stop: 0.095, RiskAmount: 500,
			},
			wants: "minimum share price",
		},
		"daily loss": {
			request: ticket.Request{
				Ticker: "ABCD", Side: ticket.SideBuy,
				Entry: 10, Stop: 9.5, RiskAmount: 2000,
			},
			mutate: func(l *ticket.Limits) { l.DailyLossUsed = 5000 },
			wants:  "past the",
		},
		"tickets per day": {
			request: ticket.Request{
				Ticker: "ABCD", Side: ticket.SideBuy,
				Entry: 10, Stop: 9.5, RiskAmount: 500,
			},
			mutate: func(l *ticket.Limits) { l.TicketsToday = 6 },
			wants:  "ceiling is 6",
		},
		"kill switch": {
			request: ticket.Request{
				Ticker: "ABCD", Side: ticket.SideBuy,
				Entry: 10, Stop: 9.5, RiskAmount: 500,
			},
			mutate: func(l *ticket.Limits) { l.KillSwitch = true },
			wants:  "kill switch",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			applied := limits()
			if testCase.mutate != nil {
				testCase.mutate(&applied)
			}
			_, err := ticket.Build(testCase.request, applied)
			if err == nil {
				t.Fatal("expected the ceiling to reject this ticket")
			}
			if !strings.Contains(err.Error(), testCase.wants) {
				t.Fatalf("error %q does not name the ceiling %q",
					err, testCase.wants)
			}
		})
	}
}

// Notional is a separate ceiling from risk: a tight stop sizes a large position
// on a small risk budget, and that position can still be too big to hold.
func TestBuildCapsBySizeAndSaysSo(t *testing.T) {
	got, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 10, Stop: 9.99, RiskAmount: 2000,
	}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Shares != 4000 || got.CappedBy != "MAX_NOTIONAL" {
		t.Fatalf("ticket = %#v, want a notional cap at 4000 shares", got)
	}
	if got.ActualRisk >= got.RiskAmount {
		t.Fatalf("a capped ticket risks less, got %v", got.ActualRisk)
	}
}

func TestBuildRejectsATargetOnTheWrongSideOrTooThin(t *testing.T) {
	if _, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 10, Stop: 9.5, Target: 9.8, RiskAmount: 500,
	}, limits()); err == nil {
		t.Fatal("a long target below the entry must be refused")
	}
	strict := limits()
	strict.MinRewardRisk = 2
	if _, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 10, Stop: 9.5, Target: 10.5, RiskAmount: 500,
	}, strict); err == nil {
		t.Fatal("a 1:1 target must fail a 2:1 minimum")
	}
}

func TestBuildRefusesWhenTheBudgetCannotBuyAShare(t *testing.T) {
	if _, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 50, Stop: 45, RiskAmount: 1,
	}, limits()); err == nil {
		t.Fatal("a budget under one share of risk must be refused")
	}
}

func TestBuildSizesAShortFromTheStopAbove(t *testing.T) {
	got, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideSell,
		Entry: 10, Stop: 10.50, Target: 9, RiskAmount: 1000,
	}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Shares != 2000 || got.RiskPerShare != 0.5 {
		t.Fatalf("short ticket = %#v", got)
	}
	if got.RewardRisk != 2 {
		t.Fatalf("reward:risk = %v, want 2", got.RewardRisk)
	}
}

// A trader who already knows the size should not have to express it as a risk
// budget and let it be divided back out. The risk becomes an outcome, and the
// ceilings still measure it.
func TestBuildAcceptsAnExplicitShareCount(t *testing.T) {
	got, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 10, Stop: 9.5, Target: 11.5, Shares: 500,
	}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Shares != 500 {
		t.Fatalf("shares = %d, want the 500 that were asked for", got.Shares)
	}
	if got.ActualRisk != 250 {
		t.Fatalf("risk = %v, want 500 shares x 0.50 = 250", got.ActualRisk)
	}
	if got.RewardRisk != 3 {
		t.Fatalf("reward:risk = %v, want 3", got.RewardRisk)
	}
}

// The ceilings are what protect the account, so they must bind a size that was
// typed just as firmly as one that was derived.
func TestBuildAppliesCeilingsToAnExplicitShareCount(t *testing.T) {
	if _, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 10, Stop: 9.5, Shares: 100_000,
	}, limits()); err == nil {
		t.Fatal("a typed size over the risk ceiling must be refused")
	}
	capped, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy,
		Entry: 10, Stop: 9.99, Shares: 5000,
	}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if capped.Shares != 4000 || capped.CappedBy != "MAX_NOTIONAL" {
		t.Fatalf("ticket = %#v, want the notional cap to bind", capped)
	}
}

func TestBuildStillNeedsASizeOrABudget(t *testing.T) {
	if _, err := ticket.Build(ticket.Request{
		Ticker: "ABCD", Side: ticket.SideBuy, Entry: 10, Stop: 9.5,
	}, limits()); err == nil {
		t.Fatal("a ticket with neither a size nor a budget must be refused")
	}
}
