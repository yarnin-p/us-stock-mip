package execution

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryRepository struct {
	nextID          int64
	order           Order
	transitions     []Transition
	positions       []Position
	snapshot        RiskSnapshot
	snapshotAccount string
}

type ambiguousLiveAdapter struct{}

type definitiveBrokerError struct{}

func (definitiveBrokerError) Error() string {
	return "broker rejected invalid order price"
}

func (definitiveBrokerError) DefinitiveSubmissionFailure() bool {
	return true
}

type rejectedLiveAdapter struct {
	ambiguousLiveAdapter
}

func (rejectedLiveAdapter) PlaceOrder(
	context.Context, BrokerOrderRequest,
) (Submission, error) {
	return Submission{}, definitiveBrokerError{}
}

func (ambiguousLiveAdapter) PreviewOrder(
	context.Context, BrokerOrderRequest,
) (Preview, error) {
	return Preview{EstimatedCost: 10}, nil
}
func (ambiguousLiveAdapter) PlaceOrder(
	context.Context, BrokerOrderRequest,
) (Submission, error) {
	return Submission{}, errors.New("timeout after request write")
}
func (ambiguousLiveAdapter) CancelOrder(context.Context, string, string) error {
	return nil
}
func (ambiguousLiveAdapter) GetOrders(
	context.Context, string,
) ([]BrokerOrder, error) {
	return nil, nil
}
func (ambiguousLiveAdapter) GetPositions(
	context.Context, string,
) ([]BrokerPosition, error) {
	return nil, nil
}
func (ambiguousLiveAdapter) GetFills(context.Context, string) ([]Fill, error) {
	return nil, nil
}

func (repo *memoryRepository) RiskSnapshot(
	_ context.Context, _ string, _ Mode, accountID string,
) (RiskSnapshot, error) {
	repo.snapshotAccount = accountID
	return repo.snapshot, nil
}
func (repo *memoryRepository) DefaultBrokerAccount(context.Context) (string, error) {
	return "paper-account", nil
}
func (repo *memoryRepository) CreateExecutionOrder(
	_ context.Context, order Order, transition Transition,
) (Order, error) {
	repo.nextID++
	order.ID = repo.nextID
	transition.ID = int64(len(repo.transitions) + 1)
	transition.OrderID = order.ID
	repo.order = order
	repo.transitions = append(repo.transitions, transition)
	return order, nil
}
func (repo *memoryRepository) ExecutionOrder(
	_ context.Context, id int64,
) (Order, error) {
	if id != repo.order.ID {
		return Order{}, ErrNotFound
	}
	return repo.order, nil
}
func (repo *memoryRepository) ExecutionOrders(context.Context) ([]Order, error) {
	return []Order{repo.order}, nil
}
func (repo *memoryRepository) ExecutionTransitions(
	context.Context, int64,
) ([]Transition, error) {
	return repo.transitions, nil
}
func (repo *memoryRepository) TransitionExecutionOrder(
	_ context.Context, order Order, expected State, transition Transition,
	fill *Fill,
) (Order, error) {
	if repo.order.State != expected {
		return Order{}, ErrConflict
	}
	transition.ID = int64(len(repo.transitions) + 1)
	transition.OrderID = order.ID
	repo.order = order
	repo.transitions = append(repo.transitions, transition)
	if fill != nil {
		repo.positions = []Position{{
			Mode: order.Mode, Ticker: order.Ticker, Quantity: fill.Quantity,
			AverageCost: fill.Price, CurrentPrice: fill.Price,
			UpdatedAt: fill.FilledAt,
		}}
	}
	return order, nil
}
func (repo *memoryRepository) ExecutionPositions(context.Context) ([]Position, error) {
	return repo.positions, nil
}
func (repo *memoryRepository) ExecutionTransactions(
	context.Context,
) ([]Transaction, error) {
	return nil, nil
}
func (repo *memoryRepository) ExecutionDailyPnL(
	context.Context,
) ([]DailyPnL, error) {
	return nil, nil
}

func TestPaperOrderRequiresPreviewAndOneTimeHumanApproval(t *testing.T) {
	now := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	repo := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", BuyingPower: 10_000,
		PortfolioEquity: 10_000,
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModePaper, Limits: Limits{
			MaxPositionValue: 2_000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500, ApprovalTTL: time.Minute,
		},
		Clock: func() time.Time { return now },
		Random: func(destination []byte) error {
			for index := range destination {
				destination[index] = byte(index + 1)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(context.Background(), CreateOrderInput{
		Ticker: "opk", Side: "buy", Quantity: 500, LimitPrice: 1.62,
		Reason: "momentum continuation",
	})
	if err != nil || order.State != StateCreated {
		t.Fatalf("create: order=%#v err=%v", order, err)
	}
	if _, err := service.Submit(context.Background(), order.ID, "", ""); !errors.Is(
		err, ErrConflict,
	) {
		t.Fatalf("submit before preview error = %v", err)
	}
	order, err = service.Preview(context.Background(), order.ID)
	if err != nil || order.State != StatePreviewed || order.EstimatedCost != 810 {
		t.Fatalf("preview: order=%#v err=%v", order, err)
	}
	approval, err := service.Approve(context.Background(), order.ID)
	if err != nil || approval.Order.State != StateApproved {
		t.Fatalf("approve: approval=%#v err=%v", approval, err)
	}
	if _, err := service.Submit(
		context.Background(), order.ID, "wrong", approval.ConfirmationText,
	); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("wrong token error = %v", err)
	}
	order, err = service.Submit(
		context.Background(), order.ID,
		approval.ConfirmationToken, approval.ConfirmationText,
	)
	if err != nil || order.State != StateFilled {
		t.Fatalf("submit: order=%#v err=%v", order, err)
	}
	if len(repo.transitions) != 5 {
		t.Fatalf("transitions = %#v", repo.transitions)
	}
	if _, err := service.Submit(
		context.Background(), order.ID,
		approval.ConfirmationToken, approval.ConfirmationText,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused approval error = %v", err)
	}
	positions, _ := service.Positions(context.Background())
	if len(positions) != 1 || positions[0].Quantity != 500 ||
		positions[0].AverageCost != 1.62 {
		t.Fatalf("positions = %#v", positions)
	}
}

func TestRejectedOrderCannotBePreviewed(t *testing.T) {
	repo := &memoryRepository{snapshot: RiskSnapshot{
		Session: "OVERNIGHT", BuyingPower: 100, PortfolioEquity: 100,
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModePaper, Limits: Limits{MaxPositionValue: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(context.Background(), CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 500, LimitPrice: 1.62,
	})
	if err != nil {
		t.Fatal(err)
	}
	if order.State != StateRejected || order.Risk.Allowed {
		t.Fatalf("order = %#v", order)
	}
	if _, err := service.Preview(context.Background(), order.ID); !errors.Is(
		err, ErrConflict,
	) {
		t.Fatalf("preview rejected order error = %v", err)
	}
}

func TestAmbiguousLiveSubmissionRemainsReconcilable(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	repo := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", BuyingPower: 10000,
		PortfolioEquity: 10000,
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModeLive, LiveAdapter: ambiguousLiveAdapter{},
		Limits: Limits{
			MaxPositionValue: 1000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500,
		},
		Clock: func() time.Time { return now },
		Random: func(destination []byte) error {
			for index := range destination {
				destination[index] = byte(index + 1)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(context.Background(), CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 1, LimitPrice: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Preview(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.Approve(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Submit(
		context.Background(), order.ID,
		approval.ConfirmationToken, approval.ConfirmationText,
	)
	if err == nil || order.State != StateSubmitted ||
		repo.order.State != StateSubmitted {
		t.Fatalf("order=%#v persisted=%#v err=%v", order, repo.order, err)
	}
	if repo.transitions[len(repo.transitions)-1].ToState != StateSubmitted {
		t.Fatalf("transitions = %#v", repo.transitions)
	}
	if repo.snapshotAccount != "paper-account" ||
		order.AccountID != "paper-account" {
		t.Fatalf(
			"risk account=%q order account=%q",
			repo.snapshotAccount, order.AccountID,
		)
	}
}

func TestApprovalCannotBeReissued(t *testing.T) {
	repo := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", BuyingPower: 10000,
		PortfolioEquity: 10000,
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModePaper,
		Limits: Limits{
			MaxPositionValue: 1000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(context.Background(), CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 1, LimitPrice: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Preview(context.Background(), order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Approve(context.Background(), order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Approve(context.Background(), order.ID); !errors.Is(
		err, ErrConflict,
	) {
		t.Fatalf("second approval error = %v", err)
	}
}

func TestDefinitiveLiveBrokerRejectionFailsOrder(t *testing.T) {
	repo := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "PRE_MARKET", BuyingPower: 10000,
		PortfolioEquity: 10000, ExistingQuantity: 10, AverageCost: 5.63,
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModeLive, LiveAdapter: rejectedLiveAdapter{},
		Limits: Limits{
			MaxPositionValue: 1000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500, AllowedSessions: []string{"PRE_MARKET"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(context.Background(), CreateOrderInput{
		Ticker: "STFS", Side: "SELL", Quantity: 10, LimitPrice: 5.38,
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Preview(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.Approve(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Submit(
		context.Background(),
		order.ID,
		approval.ConfirmationToken,
		approval.ConfirmationText,
	)
	if err == nil || order.State != StateFailed || repo.order.State != StateFailed {
		t.Fatalf("order=%#v persisted=%#v err=%v", order, repo.order, err)
	}
}

func TestLiveCancelWaitsForBrokerConfirmation(t *testing.T) {
	repo := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", BuyingPower: 10000,
		PortfolioEquity: 10000,
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModeLive, LiveAdapter: ambiguousLiveAdapter{},
		Limits: Limits{
			MaxPositionValue: 1000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(context.Background(), CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 1, LimitPrice: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Preview(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.Approve(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Submit(
		context.Background(), order.ID,
		approval.ConfirmationToken, approval.ConfirmationText,
	)
	if err == nil || order.State != StateSubmitted {
		t.Fatalf("submit order=%#v err=%v", order, err)
	}
	order, err = service.Cancel(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.State != StateSubmitted {
		t.Fatalf("cancel should await broker, order=%#v", order)
	}
	last := repo.transitions[len(repo.transitions)-1]
	if last.FromState == nil || *last.FromState != StateSubmitted ||
		last.ToState != StateSubmitted {
		t.Fatalf("cancel transition=%#v", last)
	}
}

func TestAutomaticStrategySubmissionBypassesPerOrderHumanApproval(t *testing.T) {
	repo := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", BuyingPower: 10000,
		PortfolioEquity: 10000,
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModePaper, AutomaticTrading: true,
		Limits: Limits{
			MaxPositionValue: 1000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(context.Background(), CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 100, LimitPrice: 1.95,
		Reason: "AUTO pullback reclaimed",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Preview(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.SubmitAutomatic(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.State != StateFilled {
		t.Fatalf("automatic order = %#v", order)
	}
	if len(repo.transitions) < 4 ||
		repo.transitions[2].Actor != "SYSTEM" ||
		repo.transitions[2].ToState != StateApproved {
		t.Fatalf("transitions = %#v", repo.transitions)
	}
}

func TestShadowModeUsesSimulatedAdapterWithoutBrokerAccount(t *testing.T) {
	repo := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR",
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModeShadow, AutomaticTrading: true,
		Limits: Limits{
			MaxPositionValue: 1000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500, DefaultBuyingPower: 10_000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(context.Background(), CreateOrderInput{
		Ticker: "SPRC", Side: "BUY", Quantity: 10, LimitPrice: 7,
		Reason: "shadow pullback reclaimed",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Preview(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.SubmitAutomatic(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.Mode != ModeShadow || order.State != StateFilled {
		t.Fatalf("shadow order = %#v", order)
	}
	if order.AccountID != "" || repo.snapshotAccount != "" {
		t.Fatalf(
			"shadow broker account = order %q snapshot %q",
			order.AccountID,
			repo.snapshotAccount,
		)
	}
}

func TestShadowProtectiveStopDoesNotFillWhenSubmitted(t *testing.T) {
	repo := &memoryRepository{snapshot: RiskSnapshot{
		SymbolExists: true, Session: "REGULAR",
		ExistingQuantity: 14,
	}}
	service, err := NewService(repo, ServiceOptions{
		Mode: ModeShadow, AutomaticTrading: true,
		Limits: Limits{
			MaxPositionValue: 1000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500, DefaultBuyingPower: 10_000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := service.Create(t.Context(), CreateOrderInput{
		Ticker: "AMIX", Side: "SELL", OrderType: "STOP_LOSS",
		Quantity: 14, LimitPrice: 4.82, StopPrice: 4.82,
		TimeInForce: "GTC", Reason: "shadow protective stop",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.Preview(t.Context(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	order, err = service.SubmitAutomatic(t.Context(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.State != StateSubmitted {
		t.Fatalf(
			"shadow protective stop state = %s, want %s",
			order.State,
			StateSubmitted,
		)
	}
	if order.FilledQuantity != 0 || order.AverageFillPrice != 0 {
		t.Fatalf(
			"shadow protective stop filled %.4f @ %.4f before trigger",
			order.FilledQuantity,
			order.AverageFillPrice,
		)
	}
}
