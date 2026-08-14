package execution

import (
	"context"
	"testing"
	"time"
)

/* A paper order that fills after it was submitted has to be noticed.
 *
 * Nothing noticed it. SyncExecutionBrokerOrders returns early on anything that is
 * not live, and the paper venue fills a resting order inside Observe without telling
 * the service -- so an order that did not fill on arrival sat SUBMITTED for the life
 * of the process, however long ago its price had been reached.
 *
 * That is invisible while nothing is built on fills. It stops being invisible the
 * moment something waits for one: a bracket waiting to place its stop would wait for
 * ever, in the mode this is rehearsed in, which is the mode where a mistake is
 * supposed to be free.
 */
func TestReconcileSeesAPaperFillThatHappenedAfterSubmission(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 14, 30, 0, 0, time.UTC)
	venue, err := NewPaperAdapterWithFees(0, 0)
	if err != nil {
		t.Fatalf("paper venue: %v", err)
	}
	repository := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR",
		BuyingPower: 10_000, PortfolioEquity: 10_000,
	}}
	service, err := NewService(repository, ServiceOptions{
		Mode: ModePaper, PaperAdapter: venue,
		Limits: Limits{
			MaxPositionValue: 2_000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500, ApprovalTTL: time.Minute,
		},
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}

	// The venue has to be watching a price before it will rest anything: with no
	// prints it behaves like a fill-on-arrival venue, on the reasoning that resting
	// forever is worse than filling at the price you named (paper.go). One print
	// above the limit is what turns it into a venue that waits.
	venue.Observe("ONFO", 4.10)

	// A limit under the market: the paper venue rests it rather than filling on
	// arrival, which is the case the live reconciler never covered.
	order, err := service.Create(ctx, CreateOrderInput{
		Ticker: "ONFO", Side: "BUY", Quantity: 10, LimitPrice: 3.90,
		OrderType: "LIMIT", TimeInForce: "DAY", Reason: "reconcile test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	order = submitForTest(t, service, order)
	if order.FilledQuantity != 0 {
		t.Fatalf(
			"the order filled on arrival at %.0f shares; this test needs one that rests",
			order.FilledQuantity,
		)
	}

	// The price comes to the order. The venue fills it and tells nobody.
	venue.Observe("ONFO", 3.88)

	before, err := service.Order(ctx, order.ID)
	if err != nil {
		t.Fatalf("reading before: %v", err)
	}
	if before.FilledQuantity != 0 {
		t.Fatalf(
			"the service already knew about the fill without being asked (%.0f); "+
				"this test no longer covers what it was written for",
			before.FilledQuantity,
		)
	}

	after, err := service.Reconcile(ctx, order.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if after.FilledQuantity != 10 {
		t.Fatalf("filled quantity = %.0f, want 10", after.FilledQuantity)
	}
	if after.State != StateFilled {
		t.Fatalf("state = %s, want FILLED", after.State)
	}
	if !(after.AverageFillPrice > 0) {
		t.Fatalf(
			"average fill price = %.4f: a fill nobody can price is no use to a bracket "+
				"that has to set a stop from it", after.AverageFillPrice,
		)
	}
}

/* The sweep that calls this runs every second and cannot know what it has already
 * seen, so calling it twice for one fill must not book the fill twice. */
func TestReconcileTwiceBooksOneFill(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 14, 30, 0, 0, time.UTC)
	venue, err := NewPaperAdapterWithFees(0, 0)
	if err != nil {
		t.Fatalf("paper venue: %v", err)
	}
	repository := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR",
		BuyingPower: 10_000, PortfolioEquity: 10_000,
	}}
	service, err := NewService(repository, ServiceOptions{
		Mode: ModePaper, PaperAdapter: venue,
		Limits: Limits{
			MaxPositionValue: 2_000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500, ApprovalTTL: time.Minute,
		},
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	venue.Observe("ONFO", 4.10)
	order, err := service.Create(ctx, CreateOrderInput{
		Ticker: "ONFO", Side: "BUY", Quantity: 10, LimitPrice: 3.90,
		OrderType: "LIMIT", TimeInForce: "DAY", Reason: "reconcile test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	order = submitForTest(t, service, order)
	venue.Observe("ONFO", 3.88)

	first, err := service.Reconcile(ctx, order.ID)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	second, err := service.Reconcile(ctx, order.ID)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if second.FilledQuantity != first.FilledQuantity {
		t.Fatalf(
			"filled quantity moved from %.0f to %.0f on a second reading of the same "+
				"fill", first.FilledQuantity, second.FilledQuantity,
		)
	}
}

/* Live rows belong to the broker pollers. A second writer would race them over the
 * same fill, so this returns what it read and changes nothing. */
func TestReconcileLeavesLiveOrdersToTheirPollers(t *testing.T) {
	ctx := context.Background()
	repository := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR",
		BuyingPower: 10_000, PortfolioEquity: 10_000,
	}}
	repository.order = Order{
		ID: 99, ClientOrderID: "mip-live", Mode: ModeLive, Ticker: "ONFO",
		Quantity: 10, State: StateSubmitted,
	}
	venue, err := NewPaperAdapterWithFees(0, 0)
	if err != nil {
		t.Fatalf("venue: %v", err)
	}
	service, err := NewService(repository, ServiceOptions{
		Mode: ModeLive, LiveAdapter: venue, LiveAccountID: "acct-1",
		Limits: Limits{
			MaxPositionValue: 2_000, MaxCapitalAllocation: 0.5, MaxDailyLoss: 500,
		},
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	order, err := service.Reconcile(ctx, 99)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if order.State != StateSubmitted || order.FilledQuantity != 0 {
		t.Fatalf(
			"a live order was rewritten: state %s, filled %.0f",
			order.State, order.FilledQuantity,
		)
	}
}

// submitForTest walks the approval dance so a test can get to a working order
// without restating it three times. The dance itself is covered elsewhere.
func submitForTest(t *testing.T, service *Service, order Order) Order {
	t.Helper()
	ctx := context.Background()
	previewed, err := service.Preview(ctx, order.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	approval, err := service.Approve(ctx, previewed.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	submitted, err := service.Submit(
		ctx, order.ID, approval.ConfirmationToken, approval.ConfirmationText,
	)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return submitted
}

/* A permission granted at creation has to survive to the step that re-checks it.
 *
 * Risk is measured again at preview and at approval, against the account as it stands
 * rather than as it stood when the order was written -- that is the point of measuring
 * it more than once. revalidate rebuilt its input from the stored order, and
 * AllowScaleIn had nowhere to be stored, so it came back false every time: every order
 * that needed it passed creation and was refused a step later for a position it had
 * just been told it could add to.
 *
 * Nothing noticed because nothing sent one. The autonomous runner does not scale in,
 * and the terminal could not buy at all.
 */
func TestScaleInSurvivesTheSecondRiskCheck(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 14, 30, 0, 0, time.UTC)
	venue, err := NewPaperAdapterWithFees(0, 0)
	if err != nil {
		t.Fatalf("venue: %v", err)
	}
	// A position is already held, which is what makes the permission load-bearing.
	repository := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR",
		BuyingPower: 100_000, PortfolioEquity: 100_000,
		ExistingQuantity: 10, AverageCost: 100,
	}}
	service, err := NewService(repository, ServiceOptions{
		Mode: ModePaper, PaperAdapter: venue,
		Limits: Limits{
			MaxPositionValue: 50_000, MaxCapitalAllocation: 0.9,
			MaxDailyLoss: 5_000, MaxRiskPerTrade: 5_000, ApprovalTTL: time.Minute,
		},
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}

	order, err := service.Create(ctx, CreateOrderInput{
		Ticker: "NVDA", Side: "BUY", Quantity: 4, LimitPrice: 228,
		OrderType: "LIMIT", TimeInForce: "DAY", AllowScaleIn: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if order.State == StateRejected {
		t.Fatalf(
			"creation refused a permitted scale-in: %+v", order.Risk.Violations,
		)
	}

	previewed, err := service.Preview(ctx, order.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if previewed.State == StateRejected {
		t.Fatalf(
			"the second risk check refused what the first allowed: %+v -- the "+
				"permission did not survive being stored",
			previewed.Risk.Violations,
		)
	}
}
