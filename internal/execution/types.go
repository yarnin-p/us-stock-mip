package execution

import (
	"context"
	"time"
)

type Mode string

const (
	ModePaper  Mode = "paper"
	ModeShadow Mode = "shadow"
	ModeLive   Mode = "live"
)

type State string

const (
	StateCreated         State = "CREATED"
	StatePreviewed       State = "PREVIEWED"
	StateApproved        State = "APPROVED"
	StateSubmitted       State = "SUBMITTED"
	StatePartiallyFilled State = "PARTIALLY_FILLED"
	StateFilled          State = "FILLED"
	StateCancelled       State = "CANCELLED"
	StateRejected        State = "REJECTED"
	StateFailed          State = "FAILED"
)

type Order struct {
	ID                int64      `json:"id"`
	ClientOrderID     string     `json:"client_order_id"`
	Mode              Mode       `json:"mode"`
	AccountID         string     `json:"account_id,omitempty"`
	BrokerOrderID     string     `json:"broker_order_id,omitempty"`
	Ticker            string     `json:"ticker"`
	Side              string     `json:"side"`
	OrderType         string     `json:"order_type"`
	TimeInForce       string     `json:"time_in_force"`
	Quantity          float64    `json:"quantity"`
	LimitPrice        float64    `json:"limit_price"`
	StopPrice         float64    `json:"stop_price,omitempty"`
	State             State      `json:"state"`
	EstimatedCost     float64    `json:"estimated_cost"`
	EstimatedFee      float64    `json:"estimated_fee"`
	FilledQuantity    float64    `json:"filled_quantity"`
	AverageFillPrice  float64    `json:"average_fill_price"`
	AnalysisExcluded  bool       `json:"analysis_excluded"`
	ExclusionReason   string     `json:"analysis_exclusion_reason,omitempty"`
	Risk              RiskResult `json:"risk"`
	Reason            string     `json:"reason,omitempty"`
	AIScore           *float64   `json:"ai_score,omitempty"`
	CatalystScore     *float64   `json:"catalyst_score,omitempty"`
	ApprovalHash      []byte     `json:"-"`
	ApprovalExpiresAt *time.Time `json:"approval_expires_at,omitempty"`
	ApprovedAt        *time.Time `json:"approved_at,omitempty"`
	SubmittedAt       *time.Time `json:"submitted_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type CreateOrderInput struct {
	Ticker        string   `json:"ticker"`
	Side          string   `json:"side"`
	OrderType     string   `json:"order_type,omitempty"`
	Quantity      float64  `json:"quantity"`
	LimitPrice    float64  `json:"limit_price"`
	StopPrice     float64  `json:"stop_price,omitempty"`
	TimeInForce   string   `json:"time_in_force"`
	Reason        string   `json:"reason"`
	AIScore       *float64 `json:"ai_score,omitempty"`
	CatalystScore *float64 `json:"catalyst_score,omitempty"`
	AllowScaleIn  bool     `json:"allow_scale_in"`
}

type RiskViolation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RiskResult struct {
	Allowed       bool            `json:"allowed"`
	EstimatedCost float64         `json:"estimated_cost"`
	RiskLevel     string          `json:"risk_level"`
	Violations    []RiskViolation `json:"violations"`
}

type RiskSnapshot struct {
	SymbolExists     bool
	Session          string
	BuyingPower      float64
	PortfolioEquity  float64
	GrossExposure    float64
	ExistingQuantity float64
	AverageCost      float64
	DailyRealizedPnL float64
	LastLossAt       *time.Time
}

type Limits struct {
	MaxPositionValue     float64
	MaxGrossExposure     float64
	MaxCapitalAllocation float64
	MaxDailyLoss         float64
	MaxRiskPerTrade      float64
	AllowedSessions      []string
	DefaultBuyingPower   float64
	ApprovalTTL          time.Duration
	LossCooldown         time.Duration
	KillSwitch           bool
}

type SizingInput struct {
	Ticker     string  `json:"ticker"`
	RiskAmount float64 `json:"risk_amount"`
	EntryPrice float64 `json:"entry_price"`
	StopPrice  float64 `json:"stop_price"`
}

type SizingResult struct {
	Ticker          string  `json:"ticker"`
	Shares          int64   `json:"shares"`
	RiskAmount      float64 `json:"risk_amount"`
	RiskPerShare    float64 `json:"risk_per_share"`
	EstimatedCost   float64 `json:"estimated_cost"`
	CappedBy        string  `json:"capped_by,omitempty"`
	CalculationRule string  `json:"calculation_rule"`
}

type Transition struct {
	ID        int64          `json:"id"`
	OrderID   int64          `json:"order_id"`
	FromState *State         `json:"from_state,omitempty"`
	ToState   State          `json:"to_state"`
	Actor     string         `json:"actor"`
	Reason    string         `json:"reason,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Position struct {
	Mode          Mode      `json:"mode"`
	Ticker        string    `json:"ticker"`
	Quantity      float64   `json:"quantity"`
	AverageCost   float64   `json:"average_cost"`
	CurrentPrice  float64   `json:"current_price"`
	UnrealizedPnL float64   `json:"unrealized_pnl"`
	RealizedPnL   float64   `json:"realized_pnl"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Transaction struct {
	ID            int64     `json:"id"`
	OrderID       int64     `json:"order_id"`
	Mode          Mode      `json:"mode"`
	Ticker        string    `json:"ticker"`
	Side          string    `json:"side"`
	Quantity      float64   `json:"quantity"`
	Price         float64   `json:"price"`
	Fee           float64   `json:"fee"`
	RealizedPnL   *float64  `json:"realized_pnl,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	AIScore       *float64  `json:"ai_score,omitempty"`
	CatalystScore *float64  `json:"catalyst_score,omitempty"`
	Strategy      string    `json:"strategy"`
	Source        string    `json:"source"`
	ExecutedAt    time.Time `json:"executed_at"`
}

type DailyPnL struct {
	TradingDate  string  `json:"trading_date"`
	Mode         Mode    `json:"mode"`
	GrossPnL     float64 `json:"gross_pnl"`
	Fees         float64 `json:"fees"`
	NetPnL       float64 `json:"net_pnl"`
	Entries      int     `json:"entries"`
	Exits        int     `json:"exits"`
	Transactions int     `json:"transactions"`
}

type Fill struct {
	BrokerFillID string    `json:"broker_fill_id,omitempty"`
	Quantity     float64   `json:"quantity"`
	Price        float64   `json:"price"`
	Fee          float64   `json:"fee"`
	FilledAt     time.Time `json:"filled_at"`
}

type Preview struct {
	EstimatedCost float64
	EstimatedFee  float64
}

type Submission struct {
	BrokerOrderID string
	State         State
	Fills         []Fill
}

type BrokerOrderRequest struct {
	AccountID      string
	ClientOrderID  string
	Ticker         string
	Side           string
	OrderType      string
	TimeInForce    string
	TradingSession string
	Quantity       float64
	LimitPrice     float64
	StopPrice      float64
}

type BrokerOrder struct {
	BrokerOrderID string
	State         string
}

type BrokerPosition struct {
	Ticker       string
	Quantity     float64
	AveragePrice float64
}

type BrokerAdapter interface {
	PreviewOrder(context.Context, BrokerOrderRequest) (Preview, error)
	PlaceOrder(context.Context, BrokerOrderRequest) (Submission, error)
	CancelOrder(context.Context, string, string) error
	GetOrders(context.Context, string) ([]BrokerOrder, error)
	GetPositions(context.Context, string) ([]BrokerPosition, error)
	GetFills(context.Context, string) ([]Fill, error)
}

// ModifyOrderRequest amends an order already working at the broker.
//
// Side cannot change: that would be a different order. Quantity can, but only to
// match a position that has genuinely shrunk -- selling a slice into strength
// leaves a stop covering more shares than are held, and the resize is the same
// edit as the price move rather than a new promise.
type ModifyOrderRequest struct {
	AccountID     string
	ClientOrderID string
	Ticker        string
	OrderType     string
	TimeInForce   string
	Quantity      float64
	LimitPrice    float64
	StopPrice     float64
}

// OrderModifier moves a working order's prices. Whether the broker behind it can
// amend in place or has to withdraw and re-place is the adapter's business and
// nobody else's: the caller wants the level moved, and how a particular venue
// spells that is exactly the detail an adapter exists to absorb.
//
// It returns the client order ID that is live afterwards. Usually that is the one
// passed in. An adapter serving a venue with no working amend has to withdraw and
// re-place, and then the handle changes -- so the caller stores what comes back
// rather than assuming it still holds the right one. Assuming is how the next
// amendment goes to an order that no longer exists.
//
// Amending in place is worth preferring where a venue offers it: cancelling a stop
// and placing a new one leaves the position unprotected in between, and that gap is
// exactly when a halted, fast-moving name gaps through the level. That preference
// belongs in the adapter too.
type OrderModifier interface {
	ModifyOrder(context.Context, ModifyOrderRequest) (string, error)
}

// OrderOutcome is what became of one order that was placed.
//
// Filled and Working are stated separately rather than derived from State,
// because State is the broker's own vocabulary and every broker spells these
// differently. Neither being true is a real answer, not a missing one: an order
// can be cancelled, expired or rejected, and the caller has to be able to tell
// "gone, and nothing happened" from "still out there".
type OrderOutcome struct {
	// State is the broker's own word for it, kept for the audit trail.
	State          string
	Filled         bool
	Working        bool
	FilledQuantity float64
	FilledPrice    float64
	FilledAt       time.Time
}

// OrderInspector reports what became of a single order, found by the client order
// ID that placed it.
//
// This is what closes the loop on a protective order. Without it a stop can fill,
// the position can be gone, and the system goes on trailing a level for stock
// nobody holds: amending an order the broker has already finished with, and
// showing an operator a bracket that is protecting nothing.
//
// It reads one order rather than a list. The caller knows which orders it placed
// and asks about those, so the cost scales with open positions instead of with
// account history.
type OrderInspector interface {
	OrderOutcome(
		ctx context.Context, accountID, clientOrderID string,
	) (OrderOutcome, error)
}

type Repository interface {
	RiskSnapshot(context.Context, string, Mode, string) (RiskSnapshot, error)
	DefaultBrokerAccount(context.Context) (string, error)
	CreateExecutionOrder(context.Context, Order, Transition) (Order, error)
	ExecutionOrder(context.Context, int64) (Order, error)
	ExecutionOrders(context.Context) ([]Order, error)
	ExecutionTransitions(context.Context, int64) ([]Transition, error)
	TransitionExecutionOrder(
		context.Context, Order, State, Transition, *Fill,
	) (Order, error)
	ExecutionPositions(context.Context) ([]Position, error)
	ExecutionTransactions(context.Context) ([]Transaction, error)
	ExecutionDailyPnL(context.Context) ([]DailyPnL, error)
}
