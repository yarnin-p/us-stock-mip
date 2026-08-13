package bracket

import (
	"context"
	"time"
)

// Record is a bracket as it is stored: the domain shape plus the broker handles
// and provenance the engine needs to recover after a restart.
type Record struct {
	ID             int64      `json:"id"`
	Mode           string     `json:"mode"`
	AccountID      string     `json:"account_id,omitempty"`
	Ticker         string     `json:"ticker"`
	State          State      `json:"state"`
	Quantity       float64    `json:"quantity"`
	RequestedEntry float64    `json:"requested_entry"`
	EntryPrice     float64    `json:"entry_price,omitempty"`
	StopPrice      float64    `json:"stop_price,omitempty"`
	TargetPrice    float64    `json:"target_price,omitempty"`
	HighWater      float64    `json:"high_water,omitempty"`
	Config         Config     `json:"config"`
	EntryOrderID   string     `json:"entry_order_id,omitempty"`
	StopOrderID    string     `json:"stop_order_id,omitempty"`
	TargetOrderID  string     `json:"target_order_id,omitempty"`
	RiskFlags      []string   `json:"risk_flags"`
	Note           string     `json:"note,omitempty"`
	OpenedAt       time.Time  `json:"opened_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// LastPrice and UnrealizedPnL are filled by the reader for display and are
	// not persisted; they come from the quote feed at read time.
	LastPrice     float64 `json:"last_price,omitempty"`
	UnrealizedPnL float64 `json:"unrealized_pnl,omitempty"`
}

// Bracket projects the stored record onto the domain shape Plan works with.
func (record Record) Bracket() Bracket {
	entry := record.EntryPrice
	if entry <= 0 {
		entry = record.RequestedEntry
	}
	return Bracket{
		ID: record.ID, Ticker: record.Ticker, State: record.State,
		Config: record.Config, Quantity: record.Quantity,
		EntryPrice: entry, StopPrice: record.StopPrice,
		TargetPrice: record.TargetPrice, HighWater: record.HighWater,
	}
}

// AdjustmentRecord is one entry in the audit trail: what moved, why, and whether
// the broker took it. A refused amendment is recorded as loudly as an applied
// one, because it means the position is protected at a stale level.
type AdjustmentRecord struct {
	ID             int64     `json:"id"`
	BracketID      int64     `json:"bracket_id"`
	Trigger        Trigger   `json:"trigger"`
	PreviousStop   float64   `json:"previous_stop,omitempty"`
	NewStop        float64   `json:"new_stop,omitempty"`
	PreviousTarget float64   `json:"previous_target,omitempty"`
	NewTarget      float64   `json:"new_target,omitempty"`
	LastPrice      float64   `json:"last_price"`
	HighWater      float64   `json:"high_water"`
	Applied        bool      `json:"applied"`
	BrokerError    string    `json:"broker_error,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Repository is the persistence this package needs. It is defined here, beside
// the code that consumes it, so the domain states its own requirement rather
// than importing a store's idea of one.
type Repository interface {
	CreateBracket(context.Context, Record) (Record, error)
	Bracket(context.Context, int64) (Record, error)
	// OpenBrackets is read once at start-up, to re-attach a feed to every position
	// that was already live when the process died. It is not a polling loop: after
	// boot the service reports each open and close as it happens.
	OpenBrackets(context.Context, string) ([]Record, error)
	// OpenBracketsForTicker is what a pushed price needs: the engine is handed one
	// symbol and must not read the whole book to find out whether it cares.
	OpenBracketsForTicker(context.Context, string, string) ([]Record, error)
	Brackets(context.Context, string, int) ([]Record, error)
	// SaveLevels persists new levels and the high-water mark together with the
	// adjustment that produced them, so the audit trail can never disagree with
	// the state it describes.
	SaveLevels(context.Context, Record, AdjustmentRecord) (Record, error)
	SaveBracketState(context.Context, int64, State, string) (Record, error)
	BracketAdjustments(context.Context, int64) ([]AdjustmentRecord, error)
}
