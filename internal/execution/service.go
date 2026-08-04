package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/market"
)

var (
	ErrNotFound         = errors.New("execution order not found")
	ErrConflict         = errors.New("execution order state conflict")
	ErrApprovalRequired = errors.New("valid human approval is required")
	ErrAutoTradingOff   = errors.New("automatic trading is disabled")
	executionTicker     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)
)

type Service struct {
	repository    Repository
	adapters      map[Mode]BrokerAdapter
	mode          Mode
	risk          RiskEngine
	limits        Limits
	clock         func() time.Time
	random        func([]byte) error
	automatic     bool
	liveAccountID string
	automaticMu   sync.Mutex
}

type ServiceOptions struct {
	Mode             Mode
	Limits           Limits
	PaperAdapter     BrokerAdapter
	LiveAdapter      BrokerAdapter
	AutomaticTrading bool
	LiveAccountID    string
	Clock            func() time.Time
	Random           func([]byte) error
}

type Approval struct {
	Order             Order  `json:"order"`
	ConfirmationToken string `json:"confirmation_token"`
	ConfirmationText  string `json:"confirmation_text"`
}

func NewService(repository Repository, options ServiceOptions) (*Service, error) {
	if repository == nil {
		return nil, errors.New("execution repository is required")
	}
	if options.Mode == "" {
		options.Mode = ModePaper
	}
	if options.Mode != ModePaper &&
		options.Mode != ModeShadow &&
		options.Mode != ModeLive {
		return nil, fmt.Errorf("unsupported trading mode %q", options.Mode)
	}
	if options.PaperAdapter == nil {
		options.PaperAdapter = NewPaperAdapter()
	}
	if options.Mode == ModeLive && options.LiveAdapter == nil {
		return nil, errors.New("live broker adapter is required in live mode")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Random == nil {
		options.Random = func(destination []byte) error {
			_, err := rand.Read(destination)
			return err
		}
	}
	if options.Limits.ApprovalTTL <= 0 {
		options.Limits.ApprovalTTL = 5 * time.Minute
	}
	return &Service{
		repository: repository,
		adapters: map[Mode]BrokerAdapter{
			ModePaper:  options.PaperAdapter,
			ModeShadow: options.PaperAdapter,
			ModeLive:   options.LiveAdapter,
		},
		mode: options.Mode, risk: NewRiskEngine(options.Limits),
		limits: options.Limits, clock: options.Clock, random: options.Random,
		automatic:     options.AutomaticTrading,
		liveAccountID: strings.TrimSpace(options.LiveAccountID),
	}, nil
}

func (service *Service) Mode() Mode {
	return service.mode
}

func (service *Service) Create(
	ctx context.Context, input CreateOrderInput,
) (Order, error) {
	input.Ticker = strings.ToUpper(strings.TrimSpace(input.Ticker))
	input.Side = strings.ToUpper(strings.TrimSpace(input.Side))
	input.OrderType = strings.ToUpper(strings.TrimSpace(input.OrderType))
	input.TimeInForce = strings.ToUpper(strings.TrimSpace(input.TimeInForce))
	if input.OrderType == "" {
		input.OrderType = "LIMIT"
	}
	if input.TimeInForce == "" {
		input.TimeInForce = "DAY"
	}
	if !executionTicker.MatchString(input.Ticker) {
		return Order{}, errors.New("invalid ticker")
	}
	if input.TimeInForce != "DAY" && input.TimeInForce != "GTC" {
		return Order{}, errors.New("time_in_force must be DAY or GTC")
	}
	switch input.OrderType {
	case "LIMIT":
		if input.StopPrice != 0 {
			return Order{}, errors.New("limit order must not include stop_price")
		}
	case "STOP_LOSS":
		if input.Side != "SELL" {
			return Order{}, errors.New("protective stop must be a sell order")
		}
		if input.StopPrice <= 0 {
			return Order{}, errors.New("stop-loss order requires positive stop_price")
		}
	default:
		return Order{}, errors.New("order_type must be LIMIT or STOP_LOSS")
	}
	accountID := ""
	var err error
	if service.mode == ModeLive {
		accountID, err = service.brokerAccount(ctx)
		if err != nil {
			return Order{}, fmt.Errorf("loading broker account: %w", err)
		}
		if accountID == "" {
			return Order{}, errors.New("no recently synchronized broker account is available")
		}
	}
	snapshot, err := service.repository.RiskSnapshot(
		ctx, input.Ticker, service.mode, accountID,
	)
	if err != nil {
		return Order{}, fmt.Errorf("loading risk snapshot: %w", err)
	}
	if service.mode != ModeLive {
		if snapshot.BuyingPower <= 0 && service.limits.DefaultBuyingPower > 0 {
			snapshot.BuyingPower = service.limits.DefaultBuyingPower
		}
		if snapshot.PortfolioEquity <= 0 && service.limits.DefaultBuyingPower > 0 {
			snapshot.PortfolioEquity = service.limits.DefaultBuyingPower
		}
	}
	risk := service.risk.ValidateAt(input, snapshot, service.clock().UTC())
	now := service.clock().UTC()
	clientOrderID, err := service.identifier("mip")
	if err != nil {
		return Order{}, fmt.Errorf("generating client order ID: %w", err)
	}
	state := StateCreated
	if !risk.Allowed {
		state = StateRejected
	}
	order := Order{
		ClientOrderID: clientOrderID, Mode: service.mode,
		AccountID: accountID,
		Ticker:    input.Ticker, Side: input.Side, OrderType: input.OrderType,
		TimeInForce: input.TimeInForce, Quantity: input.Quantity,
		LimitPrice: input.LimitPrice, StopPrice: input.StopPrice, State: state,
		EstimatedCost: risk.EstimatedCost, Risk: risk, Reason: input.Reason,
		AIScore: input.AIScore, CatalystScore: input.CatalystScore,
		CreatedAt: now, UpdatedAt: now,
	}
	transition := Transition{
		ToState: state, Actor: "USER", CreatedAt: now,
		Metadata: map[string]any{"risk": risk},
	}
	if !risk.Allowed {
		transition.Reason = "risk validation rejected the order"
	}
	return service.repository.CreateExecutionOrder(ctx, order, transition)
}

func (service *Service) Preview(ctx context.Context, id int64) (Order, error) {
	order, err := service.repository.ExecutionOrder(ctx, id)
	if err != nil {
		return Order{}, err
	}
	if err := ValidateTransition(order.State, StatePreviewed); err != nil {
		return Order{}, fmt.Errorf("%w: %w", ErrConflict, err)
	}
	if err := service.ensureOrderMode(order); err != nil {
		return Order{}, err
	}
	order, allowed, err := service.revalidate(ctx, order, StateCreated)
	if err != nil || !allowed {
		return order, err
	}
	preview, err := service.adapter(order.Mode).PreviewOrder(
		ctx,
		service.brokerRequest(order),
	)
	if err != nil {
		return Order{}, fmt.Errorf("previewing order: %w", err)
	}
	order.State = StatePreviewed
	order.EstimatedCost = preview.EstimatedCost
	order.EstimatedFee = preview.EstimatedFee
	order.UpdatedAt = service.clock().UTC()
	return service.repository.TransitionExecutionOrder(
		ctx, order, StateCreated, Transition{
			FromState: statePointer(StateCreated), ToState: StatePreviewed,
			Actor: "USER", Reason: "order preview completed",
			CreatedAt: order.UpdatedAt,
		}, nil,
	)
}

func (service *Service) Approve(ctx context.Context, id int64) (Approval, error) {
	order, err := service.repository.ExecutionOrder(ctx, id)
	if err != nil {
		return Approval{}, err
	}
	if err := ValidateTransition(order.State, StateApproved); err != nil {
		return Approval{}, fmt.Errorf("%w: %w", ErrConflict, err)
	}
	if err := service.ensureOrderMode(order); err != nil {
		return Approval{}, err
	}
	expected := StatePreviewed
	tokenBytes := make([]byte, 32)
	if err := service.random(tokenBytes); err != nil {
		return Approval{}, fmt.Errorf("generating approval token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	now := service.clock().UTC()
	expiresAt := now.Add(service.limits.ApprovalTTL)
	order.State = StateApproved
	order.ApprovalHash = hash[:]
	order.ApprovalExpiresAt = &expiresAt
	order.ApprovedAt = &now
	order.UpdatedAt = now
	order, err = service.repository.TransitionExecutionOrder(
		ctx, order, expected, Transition{
			FromState: statePointer(expected), ToState: StateApproved,
			Actor: "USER", Reason: "single-use human approval recorded",
			CreatedAt: now,
		}, nil,
	)
	if err != nil {
		return Approval{}, err
	}
	return Approval{
		Order: order, ConfirmationToken: token,
		ConfirmationText: confirmationText(order),
	}, nil
}

func (service *Service) Submit(
	ctx context.Context, id int64, token, confirmation string,
) (Order, error) {
	order, err := service.repository.ExecutionOrder(ctx, id)
	if err != nil {
		return Order{}, err
	}
	if err := ValidateTransition(order.State, StateSubmitted); err != nil {
		return Order{}, fmt.Errorf("%w: %w", ErrConflict, err)
	}
	if err := service.ensureOrderMode(order); err != nil {
		return Order{}, err
	}
	if !service.validApproval(order, token, confirmation) {
		return Order{}, ErrApprovalRequired
	}
	return service.submitApproved(
		ctx,
		order,
		"USER",
		"approved order submitted to broker adapter",
	)
}

func (service *Service) SubmitAutomatic(
	ctx context.Context,
	id int64,
) (Order, error) {
	if !service.automatic {
		return Order{}, ErrAutoTradingOff
	}
	// Serialize automatic submissions so each order reserves exposure before
	// the next order revalidates buying power and portfolio allocation.
	service.automaticMu.Lock()
	defer service.automaticMu.Unlock()

	order, err := service.repository.ExecutionOrder(ctx, id)
	if err != nil {
		return Order{}, err
	}
	if err := ValidateTransition(order.State, StateApproved); err != nil {
		return Order{}, fmt.Errorf("%w: %w", ErrConflict, err)
	}
	if err := service.ensureOrderMode(order); err != nil {
		return Order{}, err
	}
	order, allowed, err := service.revalidate(ctx, order, StatePreviewed)
	if err != nil || !allowed {
		return order, err
	}
	now := service.clock().UTC()
	order.State = StateApproved
	order.ApprovedAt = &now
	order.UpdatedAt = now
	order, err = service.repository.TransitionExecutionOrder(
		ctx,
		order,
		StatePreviewed,
		Transition{
			FromState: statePointer(StatePreviewed),
			ToState:   StateApproved,
			Actor:     "SYSTEM",
			Reason:    "deterministic strategy approved automatic execution",
			CreatedAt: now,
		},
		nil,
	)
	if err != nil {
		return Order{}, err
	}
	return service.submitApproved(
		ctx,
		order,
		"SYSTEM",
		"automatic strategy order submitted to broker adapter",
	)
}

func (service *Service) submitApproved(
	ctx context.Context,
	order Order,
	actor string,
	reason string,
) (Order, error) {
	order, allowed, err := service.revalidate(ctx, order, StateApproved)
	if err != nil || !allowed {
		return order, err
	}
	now := service.clock().UTC()
	order.State = StateSubmitted
	order.SubmittedAt = &now
	order.ApprovalHash = nil
	order.ApprovalExpiresAt = nil
	order.UpdatedAt = now
	order, err = service.repository.TransitionExecutionOrder(
		ctx, order, StateApproved, Transition{
			FromState: statePointer(StateApproved), ToState: StateSubmitted,
			Actor: actor, Reason: reason,
			CreatedAt: now,
		}, nil,
	)
	if err != nil {
		return Order{}, err
	}
	submission, submitErr := service.adapter(order.Mode).PlaceOrder(
		ctx, service.brokerRequest(order),
	)
	if submitErr != nil {
		var definitive interface {
			DefinitiveSubmissionFailure() bool
		}
		if errors.As(submitErr, &definitive) &&
			definitive.DefinitiveSubmissionFailure() {
			return service.fail(ctx, order, submitErr)
		}
		if order.Mode == ModeLive {
			order.UpdatedAt = service.clock().UTC()
			order, transitionErr := service.repository.TransitionExecutionOrder(
				ctx, order, StateSubmitted, Transition{
					FromState: statePointer(StateSubmitted),
					ToState:   StateSubmitted,
					Actor:     "SYSTEM",
					Reason: "broker response was ambiguous; reconciliation required: " +
						submitErr.Error(),
					CreatedAt: order.UpdatedAt,
				}, nil,
			)
			if transitionErr != nil {
				return Order{}, errors.Join(submitErr, transitionErr)
			}
			return order, fmt.Errorf(
				"broker submission outcome is unknown; order remains SUBMITTED: %w",
				submitErr,
			)
		}
		return service.fail(ctx, order, submitErr)
	}
	order.BrokerOrderID = submission.BrokerOrderID
	if submission.State == "" || submission.State == StateSubmitted {
		order.UpdatedAt = service.clock().UTC()
		return service.repository.TransitionExecutionOrder(
			ctx, order, StateSubmitted, Transition{
				FromState: statePointer(StateSubmitted),
				ToState:   StateSubmitted,
				Actor:     "BROKER",
				Reason:    "broker accepted order; awaiting fills",
				CreatedAt: order.UpdatedAt,
			}, nil,
		)
	}
	if submission.State != StateFilled && submission.State != StatePartiallyFilled {
		return service.fail(ctx, order, fmt.Errorf(
			"broker returned unsupported state %s", submission.State,
		))
	}
	if len(submission.Fills) == 0 {
		return service.fail(ctx, order, errors.New("broker returned fill state without a fill"))
	}
	for index, fill := range submission.Fills {
		expected := order.State
		target := submission.State
		if submission.State == StateFilled && index < len(submission.Fills)-1 {
			target = StatePartiallyFilled
		}
		order.State = target
		order.UpdatedAt = service.clock().UTC()
		order, err = service.repository.TransitionExecutionOrder(
			ctx, order, expected, Transition{
				FromState: statePointer(expected), ToState: target,
				Actor: "BROKER", Reason: "fill synchronized",
				CreatedAt: order.UpdatedAt,
			}, &fill,
		)
		if err != nil {
			return Order{}, err
		}
	}
	return order, nil
}

func (service *Service) Order(
	ctx context.Context,
	id int64,
) (Order, error) {
	return service.repository.ExecutionOrder(ctx, id)
}

func (service *Service) Cancel(ctx context.Context, id int64) (Order, error) {
	return service.cancel(ctx, id, "USER")
}

func (service *Service) CancelAutomatic(
	ctx context.Context,
	id int64,
) (Order, error) {
	if !service.automatic {
		return Order{}, ErrAutoTradingOff
	}
	return service.cancel(ctx, id, "SYSTEM")
}

func (service *Service) cancel(
	ctx context.Context,
	id int64,
	actor string,
) (Order, error) {
	order, err := service.repository.ExecutionOrder(ctx, id)
	if err != nil {
		return Order{}, err
	}
	if err := ValidateTransition(order.State, StateCancelled); err != nil {
		return Order{}, fmt.Errorf("%w: %w", ErrConflict, err)
	}
	if err := service.ensureOrderMode(order); err != nil {
		return Order{}, err
	}
	if (order.State == StateSubmitted || order.State == StatePartiallyFilled) &&
		order.Mode == ModeLive {
		if err := service.adapter(order.Mode).CancelOrder(
			ctx, order.AccountID, order.ClientOrderID,
		); err != nil {
			return Order{}, fmt.Errorf("cancelling broker order: %w", err)
		}
		now := service.clock().UTC()
		order.UpdatedAt = now
		return service.repository.TransitionExecutionOrder(
			ctx, order, order.State, Transition{
				FromState: statePointer(order.State), ToState: order.State,
				Actor:     actor,
				Reason:    "cancel requested; awaiting broker confirmation",
				CreatedAt: now,
			}, nil,
		)
	}
	expected := order.State
	now := service.clock().UTC()
	order.State = StateCancelled
	order.ApprovalHash = nil
	order.ApprovalExpiresAt = nil
	order.UpdatedAt = now
	return service.repository.TransitionExecutionOrder(
		ctx, order, expected, Transition{
			FromState: statePointer(expected), ToState: StateCancelled,
			Actor: actor, Reason: "order cancelled", CreatedAt: now,
		}, nil,
	)
}

func (service *Service) Orders(ctx context.Context) ([]Order, error) {
	return service.repository.ExecutionOrders(ctx)
}

func (service *Service) Transitions(
	ctx context.Context, id int64,
) ([]Transition, error) {
	return service.repository.ExecutionTransitions(ctx, id)
}

func (service *Service) Positions(ctx context.Context) ([]Position, error) {
	return service.repository.ExecutionPositions(ctx)
}

func (service *Service) Transactions(
	ctx context.Context,
) ([]Transaction, error) {
	return service.repository.ExecutionTransactions(ctx)
}

func (service *Service) DailyPnL(
	ctx context.Context,
) ([]DailyPnL, error) {
	return service.repository.ExecutionDailyPnL(ctx)
}

func (service *Service) AutomaticTradingEnabled() bool {
	return service.automatic
}

func (service *Service) Size(
	ctx context.Context, input SizingInput,
) (SizingResult, error) {
	input.Ticker = strings.ToUpper(strings.TrimSpace(input.Ticker))
	if !executionTicker.MatchString(input.Ticker) {
		return SizingResult{}, errors.New("invalid ticker")
	}
	accountID := ""
	var err error
	if service.mode == ModeLive {
		accountID, err = service.brokerAccount(ctx)
		if err != nil {
			return SizingResult{}, fmt.Errorf("loading broker account: %w", err)
		}
		if accountID == "" {
			return SizingResult{}, errors.New(
				"no recently synchronized broker account is available",
			)
		}
	}
	snapshot, err := service.repository.RiskSnapshot(
		ctx, input.Ticker, service.mode, accountID,
	)
	if err != nil {
		return SizingResult{}, fmt.Errorf("loading sizing risk snapshot: %w", err)
	}
	if service.mode != ModeLive && snapshot.BuyingPower <= 0 {
		snapshot.BuyingPower = service.limits.DefaultBuyingPower
	}
	if service.mode != ModeLive && snapshot.PortfolioEquity <= 0 {
		snapshot.PortfolioEquity = service.limits.DefaultBuyingPower
	}
	if service.mode == ModeLive &&
		(snapshot.BuyingPower <= 0 || snapshot.PortfolioEquity <= 0) {
		return SizingResult{}, errors.New(
			"live buying power and portfolio equity are unavailable",
		)
	}
	return service.risk.Size(input, snapshot)
}

func (service *Service) KillSwitchActive() bool {
	return service.limits.KillSwitch
}

func (service *Service) AllowedSessions() []string {
	return append([]string(nil), service.limits.AllowedSessions...)
}

func (service *Service) revalidate(
	ctx context.Context, order Order, expected State,
) (Order, bool, error) {
	snapshot, err := service.repository.RiskSnapshot(
		ctx, order.Ticker, order.Mode, order.AccountID,
	)
	if err != nil {
		return Order{}, false, fmt.Errorf("refreshing risk snapshot: %w", err)
	}
	if order.Mode != ModeLive {
		if snapshot.BuyingPower <= 0 {
			snapshot.BuyingPower = service.limits.DefaultBuyingPower
		}
		if snapshot.PortfolioEquity <= 0 {
			snapshot.PortfolioEquity = service.limits.DefaultBuyingPower
		}
	}
	risk := service.risk.ValidateAt(CreateOrderInput{
		Ticker: order.Ticker, Side: order.Side, Quantity: order.Quantity,
		OrderType: order.OrderType, LimitPrice: order.LimitPrice,
		StopPrice: order.StopPrice, TimeInForce: order.TimeInForce,
	}, snapshot, service.clock().UTC())
	order.Risk = risk
	order.EstimatedCost = risk.EstimatedCost
	if risk.Allowed {
		return order, true, nil
	}
	now := service.clock().UTC()
	order.State = StateRejected
	order.ApprovalHash = nil
	order.ApprovalExpiresAt = nil
	order.UpdatedAt = now
	order, err = service.repository.TransitionExecutionOrder(
		ctx, order, expected, Transition{
			FromState: statePointer(expected), ToState: StateRejected,
			Actor: "SYSTEM", Reason: "risk validation changed before execution",
			CreatedAt: now, Metadata: map[string]any{"risk": risk},
		}, nil,
	)
	if err != nil {
		return Order{}, false, err
	}
	return order, false, nil
}

func (service *Service) validApproval(
	order Order, token, confirmation string,
) bool {
	if len(order.ApprovalHash) != sha256.Size ||
		order.ApprovalExpiresAt == nil ||
		service.clock().UTC().After(*order.ApprovalExpiresAt) ||
		confirmation != confirmationText(order) {
		return false
	}
	provided := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(order.ApprovalHash, provided[:]) == 1
}

func (service *Service) fail(
	ctx context.Context, order Order, cause error,
) (Order, error) {
	expected := order.State
	order.State = StateFailed
	order.UpdatedAt = service.clock().UTC()
	failed, err := service.repository.TransitionExecutionOrder(
		ctx, order, expected, Transition{
			FromState: statePointer(expected), ToState: StateFailed,
			Actor: "SYSTEM", Reason: cause.Error(), CreatedAt: order.UpdatedAt,
		}, nil,
	)
	if err != nil {
		return Order{}, errors.Join(cause, err)
	}
	return failed, fmt.Errorf("order failed: %w", cause)
}

func (service *Service) identifier(prefix string) (string, error) {
	bytes := make([]byte, 12)
	if err := service.random(bytes); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(bytes), nil
}

func (service *Service) adapter(mode Mode) BrokerAdapter {
	return service.adapters[mode]
}

func (service *Service) ensureOrderMode(order Order) error {
	if service.mode != order.Mode {
		return fmt.Errorf("%w: order mode is %s", ErrConflict, order.Mode)
	}
	if service.adapter(order.Mode) == nil {
		return fmt.Errorf("%w: %s broker adapter is unavailable", ErrConflict, order.Mode)
	}
	return nil
}

func (service *Service) brokerAccount(ctx context.Context) (string, error) {
	if service.liveAccountID != "" {
		return service.liveAccountID, nil
	}
	return service.repository.DefaultBrokerAccount(ctx)
}

func (service *Service) brokerRequest(order Order) BrokerOrderRequest {
	return BrokerOrderRequest{
		AccountID: order.AccountID, ClientOrderID: order.ClientOrderID,
		Ticker: order.Ticker, Side: order.Side, OrderType: order.OrderType,
		TimeInForce: order.TimeInForce,
		TradingSession: market.SessionAt(
			service.clock().UTC(),
		).WebullOrderSession(),
		Quantity:   order.Quantity,
		LimitPrice: order.LimitPrice,
		StopPrice:  order.StopPrice,
	}
}

func confirmationText(order Order) string {
	return fmt.Sprintf(
		"SEND %s %s %.4f @ %.4f",
		order.Side, order.Ticker, order.Quantity, order.LimitPrice,
	)
}

func statePointer(state State) *State {
	return &state
}
