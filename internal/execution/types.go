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

// OrderModifier is implemented by brokers that can amend a working order in
// place. It is deliberately separate from BrokerAdapter, because not every
// broker can do this and a caller must be able to tell which one it holds.
//
// The distinction matters for money. Cancelling a stop and placing a new one
// leaves the position unprotected in between, and that gap is exactly when a
// halted, fast-moving name gaps through the level. Code that finds only a
// BrokerAdapter should report the weaker guarantee rather than treat the two as
// equivalent.
type OrderModifier interface {
	ModifyOrder(context.Context, ModifyOrderRequest) error
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
