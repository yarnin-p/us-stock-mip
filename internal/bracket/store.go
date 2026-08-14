package bracket

import (
	"context"
	"strings"
	"time"
)

// Record is a bracket as it is stored: the domain shape plus the broker handles
// and provenance the engine needs to recover after a restart.
type Record struct {
	ID             int64    `json:"id"`
	Mode           string   `json:"mode"`
	AccountID      string   `json:"account_id,omitempty"`
	Ticker         string   `json:"ticker"`
	State          State    `json:"state"`
	Quantity       float64  `json:"quantity"`
	RequestedEntry float64  `json:"requested_entry"`
	EntryPrice     float64  `json:"entry_price,omitempty"`
	StopPrice      float64  `json:"stop_price,omitempty"`
	TargetPrice    float64  `json:"target_price,omitempty"`
	HighWater      float64  `json:"high_water,omitempty"`
	Config         Config   `json:"config"`
	EntryOrderID   string   `json:"entry_order_id,omitempty"`
	StopOrderID    string   `json:"stop_order_id,omitempty"`
	TargetOrderID  string   `json:"target_order_id,omitempty"`
	RiskFlags      []string `json:"risk_flags"`
	// ManualHold means the operator has taken the wheel: the engine records what it
	// would have done and sends nothing. It exists because a stop typed by hand and
	// then moved by the engine leaves nobody able to say which of them is driving.
	ManualHold bool `json:"manual_hold"`
	// PartialTakenQuantity is how much has already been sold into strength, and
	// PartialOrderID is the sale that did it. A quantity rather than a flag, because
	// the remainder is what the stop still protects and a boolean could not say how
	// much that is.
	PartialTakenQuantity float64 `json:"partial_taken_quantity,omitempty"`
	PartialOrderID       string  `json:"partial_order_id,omitempty"`
	// PartialFillPrice is what that sale got. Without it the money banked by the
	// rung cannot be worked out, which is the only number that says whether arming
	// it was worth anything.
	PartialFillPrice float64 `json:"partial_fill_price,omitempty"`
	// StopGeneration counts how many times a stop order has been rested at the broker
	// for this bracket. A stop that changes hands with the session is placed and
	// withdrawn once a day at least, and a broker that keys on client_order_id would
	// refuse a second order reusing the handle of one cancelled hours earlier.
	StopGeneration int `json:"stop_generation,omitempty"`
	// StopFired says the engine has already sent the protective sell for this bracket.
	//
	// An explicit fact rather than something inferred from the order handle, because
	// two different things wear that handle: a stop resting at the broker, and the
	// limit sell this engine fired. Confusing them let the session handover cancel the
	// exit that was in flight and then sell again -- which does not just cost money, it
	// can leave the position short.
	StopFired bool       `json:"stop_fired,omitempty"`
	Note      string     `json:"note,omitempty"`
	OpenedAt  time.Time  `json:"opened_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`

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
		PartialTakenQuantity: record.PartialTakenQuantity,
		// A slice that has been sent but not yet confirmed is still sent, so the
		// order ID -- not the quantity -- is what says the rung has fired.
		PartialSliceSent: strings.TrimSpace(record.PartialOrderID) != "",
		EntryPrice:       entry, StopPrice: record.StopPrice,
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

// Finisher records a bracket as over and releases whatever was following its
// symbol.
//
// The engine holds one instead of writing the terminal state itself, so a bracket
// closed by a stop filling and one closed by hand go through the same door. Two
// ways to end a bracket would be two places to forget to release the feed, and a
// symbol nobody unwatches keeps paying a venue subscription for a position that
// no longer exists.
type Finisher interface {
	Close(ctx context.Context, id int64, state State, note string) (Record, error)
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
	// SaveBracket persists levels, configuration and the manual-hold flag together
	// with the row explaining them. The engine uses SaveLevels for its hot path; a
	// human changing the rules goes through here, so one amendment is one write.
	SaveBracket(context.Context, Record, AdjustmentRecord) (Record, error)
	SaveBracketState(context.Context, int64, State, string) (Record, error)
	BracketAdjustments(context.Context, int64) ([]AdjustmentRecord, error)
}
