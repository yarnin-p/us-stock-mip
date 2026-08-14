package bracket

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

// stubProtector is the broker the protective orders go to.
type stubProtector struct {
	mutex     sync.Mutex
	placed    []execution.BrokerOrderRequest
	cancelled []string
	moved     []execution.ModifyOrderRequest
	// failStop and failTarget make the two halves fail independently, because the
	// whole design of Arm is that they are not equally important.
	failStop, failTarget error
}

func (protector *stubProtector) PlaceOrder(
	_ context.Context, order execution.BrokerOrderRequest,
) (execution.Submission, error) {
	protector.mutex.Lock()
	defer protector.mutex.Unlock()
	if order.OrderType == "STOP_LOSS" && protector.failStop != nil {
		return execution.Submission{}, protector.failStop
	}
	if order.OrderType == "LIMIT" && protector.failTarget != nil {
		return execution.Submission{}, protector.failTarget
	}
	protector.placed = append(protector.placed, order)
	return execution.Submission{BrokerOrderID: order.ClientOrderID}, nil
}

func (protector *stubProtector) CancelOrder(
	_ context.Context, _, clientOrderID string,
) error {
	protector.mutex.Lock()
	defer protector.mutex.Unlock()
	protector.cancelled = append(protector.cancelled, clientOrderID)
	return nil
}

func (protector *stubProtector) ModifyOrder(
	_ context.Context, request execution.ModifyOrderRequest,
) (string, error) {
	protector.mutex.Lock()
	defer protector.mutex.Unlock()
	protector.moved = append(protector.moved, request)
	return request.ClientOrderID, nil
}

func (protector *stubProtector) orders() []execution.BrokerOrderRequest {
	protector.mutex.Lock()
	defer protector.mutex.Unlock()
	return append([]execution.BrokerOrderRequest(nil), protector.placed...)
}

type stubAccounts struct {
	id  string
	err error
}

func (accounts stubAccounts) DefaultBrokerAccount(
	context.Context,
) (string, error) {
	return accounts.id, accounts.err
}

func armService(
	t *testing.T, record Record, protector BrokerOrders, accounts AccountSource,
) (*Service, *stubRepository) {
	t.Helper()
	repository := newStubRepository(record)
	// A test with no venue gets the one that refuses, not a nil. That is the same
	// thing production gets when credentials are missing, so a test for "arming
	// without a broker" exercises the real path rather than a shape only tests see.
	if protector == nil {
		protector = NoBroker("this test wired no venue")
	}
	if accounts == nil {
		accounts = stubAccounts{id: "acct-1"}
	}
	service, err := NewService(repository, protector, accounts, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return service.WithLogger(quietLogger()), repository
}

func pendingRecord() Record {
	record := activeRecord()
	record.State = StatePending
	record.EntryPrice = 0
	record.StopPrice = 0
	record.TargetPrice = 0
	record.StopOrderID = ""
	record.TargetOrderID = ""
	record.AccountID = ""
	return record
}

// This is the whole point of Arm. Before it existed a bracket stayed PENDING, and
// the engine skips anything that is not ACTIVE, so no ladder ever ran.
func TestArmingPlacesBothOrdersAndTurnsTheBracketOn(t *testing.T) {
	record := pendingRecord()
	protector := &stubProtector{}
	service, repository := armService(
		t, record, protector, stubAccounts{id: "acct-9"},
	)

	armed, err := service.Arm(context.Background(), record.ID, ArmInput{
		FillPrice: 10.20, Note: "ซื้อมือที่โบรก",
	})
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	if armed.State != StateActive {
		t.Fatalf("state = %s; the engine skips anything that is not ACTIVE", armed.State)
	}
	orders := protector.orders()
	if len(orders) != 2 {
		t.Fatalf("placed %d orders, want a stop and a target: %+v", len(orders), orders)
	}
	var stop, target execution.BrokerOrderRequest
	for _, order := range orders {
		if order.OrderType == "STOP_LOSS" {
			stop = order
		} else {
			target = order
		}
	}
	if stop.Side != "SELL" || !(stop.StopPrice > 0) || stop.StopPrice >= 10.20 {
		t.Fatalf("stop = %+v; it has to sit below the fill", stop)
	}
	if target.Side != "SELL" || !(target.LimitPrice > 10.20) {
		t.Fatalf("target = %+v; it has to sit above the fill", target)
	}
	// Every later amendment carries the account, and the broker refuses one without.
	for _, order := range orders {
		if order.AccountID != "acct-9" {
			t.Fatalf("%s order has account %q", order.OrderType, order.AccountID)
		}
	}
	stored := repository.stored(t, record.ID)
	if stored.AccountID != "acct-9" {
		t.Fatalf("stored account = %q; trailing would be refused for want of one",
			stored.AccountID)
	}
	if stored.StopOrderID == "" || stored.TargetOrderID == "" {
		t.Fatalf("order handles = %q / %q; there would be nothing to amend",
			stored.StopOrderID, stored.TargetOrderID)
	}
	// Levels come from the fill, not from the price that was hoped for.
	if stored.EntryPrice != 10.20 || stored.HighWater != 10.20 {
		t.Fatalf("entry = %v, high = %v; want both at the fill",
			stored.EntryPrice, stored.HighWater)
	}
}

// The stop is the half that cannot be got wrong. A position that believes it is
// protected and is not is worse than one that was never armed.
func TestAFailedStopLeavesTheBracketPendingRatherThanArmed(t *testing.T) {
	record := pendingRecord()
	protector := &stubProtector{failStop: errors.New("the venue refused the stop")}
	service, repository := armService(
		t, record, protector, stubAccounts{id: "acct-9"},
	)

	_, err := service.Arm(context.Background(), record.ID, ArmInput{FillPrice: 10.20})
	if err == nil {
		t.Fatal("a bracket must not arm when its stop did not place")
	}
	if !strings.Contains(err.Error(), "unprotected") {
		t.Errorf("the error should say the position is unprotected: %v", err)
	}
	if stored := repository.stored(t, record.ID); stored.State != StatePending {
		t.Fatalf("state = %s, want it left PENDING", stored.State)
	}
	if orders := protector.orders(); len(orders) != 0 {
		t.Fatalf("placed %+v after the stop failed; the target is pointless alone", orders)
	}
}

// A missing target costs an exit taken by hand. That is worth arming for, and worth
// saying out loud, because a screen showing a target with no order behind it is
// discovered at the moment it was needed.
func TestAFailedTargetStillArmsAndSaysSo(t *testing.T) {
	record := pendingRecord()
	protector := &stubProtector{failTarget: errors.New("limit rejected")}
	service, repository := armService(
		t, record, protector, stubAccounts{id: "acct-9"},
	)

	armed, err := service.Arm(context.Background(), record.ID, ArmInput{FillPrice: 10.20})
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	if armed.State != StateActive {
		t.Fatalf("state = %s; the stop is in, so the bracket is live", armed.State)
	}
	stored := repository.stored(t, record.ID)
	if stored.StopOrderID == "" {
		t.Fatal("the stop handle was lost")
	}
	if stored.TargetOrderID != "" {
		t.Fatalf("target handle = %q, but no target was placed", stored.TargetOrderID)
	}
	if !strings.Contains(stored.Note, "target order failed") {
		t.Fatalf("note = %q; nothing tells the operator the upside is unguarded",
			stored.Note)
	}
}

func TestArmingRefusesWithoutWhatItNeeds(t *testing.T) {
	for name, testCase := range map[string]struct {
		protector BrokerOrders
		accounts  AccountSource
		input     ArmInput
		wants     string
	}{
		"no broker wired": {
			input: ArmInput{FillPrice: 10}, wants: "the position is unprotected",
		},
		"no fill price": {
			protector: &stubProtector{}, accounts: stubAccounts{id: "a"},
			input: ArmInput{}, wants: "price that filled",
		},
		"no account": {
			protector: &stubProtector{}, accounts: stubAccounts{id: "  "},
			input: ArmInput{FillPrice: 10}, wants: "no broker account",
		},
		"account lookup failed": {
			protector: &stubProtector{},
			accounts:  stubAccounts{err: errors.New("db down")},
			input:     ArmInput{FillPrice: 10}, wants: "resolving the broker account",
		},
	} {
		record := pendingRecord()
		service, repository := armService(
			t, record, testCase.protector, testCase.accounts,
		)
		_, err := service.Arm(context.Background(), record.ID, testCase.input)
		if err == nil {
			t.Errorf("%s: expected a refusal", name)
			continue
		}
		if !strings.Contains(err.Error(), testCase.wants) {
			t.Errorf("%s: error %q does not mention %q", name, err, testCase.wants)
		}
		if stored := repository.stored(t, record.ID); stored.State != StatePending {
			t.Errorf("%s: state = %s, want PENDING", name, stored.State)
		}
	}
}

// A partial fill protected as though it were whole puts more stock into the market
// on the way out than is held.
func TestArmingProtectsWhatWasActuallyBought(t *testing.T) {
	record := pendingRecord()
	protector := &stubProtector{}
	service, repository := armService(
		t, record, protector, stubAccounts{id: "acct-9"},
	)
	if _, err := service.Arm(context.Background(), record.ID, ArmInput{
		FillPrice: 10.20, Quantity: 40,
	}); err != nil {
		t.Fatalf("arm: %v", err)
	}
	for _, order := range protector.orders() {
		if order.Quantity != 40 {
			t.Fatalf("%s order covers %v of 40 held", order.OrderType, order.Quantity)
		}
	}
	if stored := repository.stored(t, record.ID); stored.Quantity != 40 {
		t.Fatalf("stored quantity = %v, want 40", stored.Quantity)
	}
}

func TestABracketCanOnlyBeArmedOnce(t *testing.T) {
	record := pendingRecord()
	protector := &stubProtector{}
	service, _ := armService(t, record, protector, stubAccounts{id: "acct-9"})
	ctx := context.Background()
	if _, err := service.Arm(ctx, record.ID, ArmInput{FillPrice: 10.20}); err != nil {
		t.Fatalf("arm: %v", err)
	}
	if _, err := service.Arm(ctx, record.ID, ArmInput{FillPrice: 11.00}); err == nil {
		t.Fatal("arming twice would place a second pair of orders over the first")
	}
	if orders := protector.orders(); len(orders) != 2 {
		t.Fatalf("placed %d orders across two attempts", len(orders))
	}
}

// A stop-limit is the only protective shape that can be legal outside the regular
// session, and it is only that if both prices go out together.
func TestAStopLimitCarriesTheLimitItReleases(t *testing.T) {
	record := pendingRecord()
	protector := &stubProtector{}
	service, _ := armService(t, record, protector, stubAccounts{id: "acct-9"})
	service, err := service.WithStopShape(StopShape{
		OrderType: "STOP_LOSS_LIMIT", LimitOffsetPercent: 0.02,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if _, err := service.Arm(
		context.Background(), record.ID, ArmInput{FillPrice: 10.00},
	); err != nil {
		t.Fatalf("arm: %v", err)
	}
	var stop execution.BrokerOrderRequest
	for _, order := range protector.orders() {
		if order.OrderType == "STOP_LOSS_LIMIT" {
			stop = order
		}
	}
	if stop.OrderType != "STOP_LOSS_LIMIT" {
		t.Fatalf("no stop-limit was placed: %+v", protector.orders())
	}
	if !(stop.StopPrice > 0) || !(stop.LimitPrice > 0) {
		t.Fatalf("stop-limit = trigger %v limit %v; both are required",
			stop.StopPrice, stop.LimitPrice)
	}
	// Below the trigger, or the released order can never fill.
	if stop.LimitPrice > stop.StopPrice {
		t.Fatalf("limit %v is above the trigger %v", stop.LimitPrice, stop.StopPrice)
	}
	want := roundToCent(stop.StopPrice * 0.98)
	if stop.LimitPrice != want {
		t.Fatalf("limit = %v, want %v (2%% under the trigger)", stop.LimitPrice, want)
	}
}

// The engine has to amend with the shape the service placed. Sending a plain stop
// amendment against a stop-limit drops the limit the broker is holding.
func TestTheEngineAmendsAStopLimitWithBothPrices(t *testing.T) {
	record := activeRecord()
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: modifier,
		StopShape: StopShape{OrderType: "STOP_LOSS_LIMIT", LimitOffsetPercent: 0.02},
		Logger:    quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	// Far enough up to ratchet the stop.
	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, 14.00),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	found := false
	for _, call := range modifier.calls() {
		if call.OrderType != "STOP_LOSS_LIMIT" {
			continue
		}
		found = true
		if !(call.StopPrice > 0) || !(call.LimitPrice > 0) {
			t.Fatalf("amendment = trigger %v limit %v; both must move together",
				call.StopPrice, call.LimitPrice)
		}
		if call.LimitPrice > call.StopPrice {
			t.Fatalf("limit %v above trigger %v", call.LimitPrice, call.StopPrice)
		}
	}
	if !found {
		t.Fatalf("the stop was amended as something else: %+v", modifier.calls())
	}
}

func TestAnImpossibleStopShapeIsRefused(t *testing.T) {
	for name, shape := range map[string]StopShape{
		"unknown type":            {OrderType: "TRAILING_STOP_LOSS"},
		"offset on a market stop": {OrderType: "STOP_LOSS", LimitOffsetPercent: 0.02},
		"offset too far":          {OrderType: "STOP_LOSS_LIMIT", LimitOffsetPercent: 0.9},
		"negative offset":         {OrderType: "STOP_LOSS_LIMIT", LimitOffsetPercent: -0.01},
	} {
		if err := shape.Validate(); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
		if _, err := NewEngine(EngineOptions{
			Repository: newStubRepository(), Modifier: &stubModifier{},
			StopShape: shape, Logger: quietLogger(),
		}); err == nil {
			t.Errorf("%s: the engine accepted it", name)
		}
	}
}
