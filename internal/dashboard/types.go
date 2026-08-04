package dashboard

import (
	"context"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/spike"
)

type Quote struct {
	BidPrice   float64   `json:"bid_price"`
	BidSize    float64   `json:"bid_size"`
	AskPrice   float64   `json:"ask_price"`
	AskSize    float64   `json:"ask_size"`
	ObservedAt time.Time `json:"observed_at"`
	Source     string    `json:"source"`
}

type MarketSnapshot struct {
	Price             float64   `json:"price"`
	Volume            float64   `json:"volume"`
	ChangeRatio       float64   `json:"change_ratio"`
	RunnerProbability *float64  `json:"runner_probability,omitempty"`
	Score             float64   `json:"score"`
	ObservedAt        time.Time `json:"observed_at"`
}

type Candidate struct {
	Ticker      string          `json:"ticker"`
	TradingDate time.Time       `json:"trading_date"`
	Rank        int             `json:"rank"`
	Score       float64         `json:"score"`
	Coverage    float64         `json:"coverage"`
	Selected    bool            `json:"selected"`
	Snapshot    *MarketSnapshot `json:"snapshot,omitempty"`
	Quote       *Quote          `json:"quote,omitempty"`
}

type ScanSignal struct {
	Ticker string `json:"ticker"`
	MarketSnapshot
	Quote *Quote `json:"quote,omitempty"`
}

type WatchlistItem struct {
	Ticker   string          `json:"ticker"`
	Thesis   string          `json:"thesis,omitempty"`
	AddedAt  time.Time       `json:"added_at"`
	Snapshot *MarketSnapshot `json:"snapshot,omitempty"`
	Quote    *Quote          `json:"quote,omitempty"`
}

type WatchlistInput struct {
	Ticker string `json:"ticker"`
	Thesis string `json:"thesis"`
}

type Position struct {
	ID                int64      `json:"id"`
	Ticker            string     `json:"ticker"`
	Side              string     `json:"side"`
	Quantity          float64    `json:"quantity"`
	RemainingQuantity float64    `json:"remaining_quantity"`
	EntryPrice        float64    `json:"entry_price"`
	EnteredAt         time.Time  `json:"entered_at"`
	Strategy          string     `json:"strategy"`
	Notes             string     `json:"notes,omitempty"`
	CurrentPrice      *float64   `json:"current_price,omitempty"`
	PriceObservedAt   *time.Time `json:"price_observed_at,omitempty"`
	UnrealizedPnL     *float64   `json:"unrealized_pnl,omitempty"`
	MomentumScore     *float64   `json:"momentum_score,omitempty"`
	BuyerPressure     *float64   `json:"buyer_pressure,omitempty"`
	VWAP              *float64   `json:"vwap,omitempty"`
	Support           *float64   `json:"support,omitempty"`
	Resistance        *float64   `json:"resistance,omitempty"`
	Health            string     `json:"health"`
	Confidence        float64    `json:"confidence"`
	Fees              float64    `json:"fees"`
	RealizedPnL       float64    `json:"realized_pnl"`
	ThesisMatch       string     `json:"thesis_match"`
	ReentryGrade      string     `json:"reentry_grade"`
}

type TradeReview struct {
	EntryQuality       string  `json:"entry_quality"`
	ExitQuality        string  `json:"exit_quality"`
	HoldingQuality     string  `json:"holding_quality"`
	Summary            string  `json:"summary"`
	DecisionConfidence float64 `json:"decision_confidence"`
}

type Trade struct {
	ID                int64        `json:"id"`
	Ticker            string       `json:"ticker"`
	Side              string       `json:"side"`
	Quantity          float64      `json:"quantity"`
	RemainingQuantity float64      `json:"remaining_quantity"`
	EntryPrice        float64      `json:"entry_price"`
	ExitPrice         *float64     `json:"exit_price,omitempty"`
	EnteredAt         time.Time    `json:"entered_at"`
	ExitedAt          *time.Time   `json:"exited_at,omitempty"`
	Strategy          string       `json:"strategy"`
	PnL               *float64     `json:"pnl,omitempty"`
	Notes             string       `json:"notes,omitempty"`
	Fees              float64      `json:"fees"`
	RealizedPnL       float64      `json:"realized_pnl"`
	Review            *TradeReview `json:"review,omitempty"`
}

type TradeInput struct {
	Ticker     string    `json:"ticker"`
	Side       string    `json:"side"`
	Quantity   float64   `json:"quantity"`
	EntryPrice float64   `json:"entry_price"`
	EnteredAt  time.Time `json:"entered_at"`
	Strategy   string    `json:"strategy"`
	Notes      string    `json:"notes"`
	Fees       float64   `json:"fees"`
}

type TradeEventInput struct {
	Type       string    `json:"type"`
	Quantity   float64   `json:"quantity"`
	Price      float64   `json:"price"`
	Fees       float64   `json:"fees"`
	OccurredAt time.Time `json:"occurred_at"`
	Notes      string    `json:"notes"`
}

type ScorePoint struct {
	ObservedAt time.Time      `json:"observed_at"`
	Score      float64        `json:"score"`
	Coverage   *float64       `json:"coverage,omitempty"`
	Source     string         `json:"source"`
	Components map[string]any `json:"components"`
}

type Alert struct {
	ID             int64      `json:"id"`
	Type           string     `json:"type"`
	Severity       string     `json:"severity"`
	Ticker         *string    `json:"ticker,omitempty"`
	Title          string     `json:"title"`
	Message        string     `json:"message"`
	CreatedAt      time.Time  `json:"created_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

type ComponentHealth struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail,omitempty"`
	LastUpdate *time.Time `json:"last_update,omitempty"`
	LatencyMS  *int64     `json:"latency_ms,omitempty"`
}

type SchedulerHealth struct {
	Enabled     bool       `json:"enabled"`
	Timezone    string     `json:"timezone"`
	NextJob     string     `json:"next_job,omitempty"`
	NextRun     *time.Time `json:"next_run,omitempty"`
	LastJob     string     `json:"last_job,omitempty"`
	LastStatus  string     `json:"last_status,omitempty"`
	LastRun     *time.Time `json:"last_run,omitempty"`
	LastMessage string     `json:"last_message,omitempty"`
}

type SystemHealth struct {
	Components []ComponentHealth `json:"components"`
	Scheduler  SchedulerHealth   `json:"scheduler"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type Event struct {
	Scope      string    `json:"scope"`
	Operation  string    `json:"operation,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type NewsCatalyst struct {
	ID             int64                           `json:"id"`
	Ticker         string                          `json:"ticker"`
	ExternalID     string                          `json:"external_id,omitempty"`
	PublishedAt    time.Time                       `json:"published_at"`
	AvailableAt    time.Time                       `json:"available_at"`
	Title          string                          `json:"title"`
	Description    string                          `json:"description,omitempty"`
	SourceURL      string                          `json:"source_url,omitempty"`
	Sentiment      string                          `json:"sentiment,omitempty"`
	StoredScore    float64                         `json:"stored_score"`
	Classification intelligence.NewsClassification `json:"classification"`
	Snapshot       *MarketSnapshot                 `json:"snapshot,omitempty"`
	Quote          *Quote                          `json:"quote,omitempty"`
}

type NewsCatalystSource interface {
	NewsCatalysts(
		context.Context,
		time.Time,
		time.Duration,
		int,
	) ([]NewsCatalyst, error)
}

type BrokerPosition struct {
	AccountID     string    `json:"account_id"`
	PositionID    string    `json:"position_id"`
	Ticker        string    `json:"ticker"`
	Quantity      float64   `json:"quantity"`
	AveragePrice  *float64  `json:"average_price,omitempty"`
	UnrealizedPnL *float64  `json:"unrealized_pnl,omitempty"`
	SyncedAt      time.Time `json:"synced_at"`
}

type BrokerOrder struct {
	AccountID      string     `json:"account_id"`
	ClientOrderID  string     `json:"client_order_id"`
	OrderID        string     `json:"order_id,omitempty"`
	Ticker         string     `json:"ticker"`
	Side           string     `json:"side"`
	Status         string     `json:"status"`
	TotalQuantity  float64    `json:"total_quantity"`
	FilledQuantity float64    `json:"filled_quantity"`
	FilledPrice    *float64   `json:"filled_price,omitempty"`
	Commission     float64    `json:"commission"`
	Fees           float64    `json:"fees"`
	PlacedAt       *time.Time `json:"placed_at,omitempty"`
	FilledAt       *time.Time `json:"filled_at,omitempty"`
	SyncedAt       time.Time  `json:"synced_at"`
}

type ModelSummary struct {
	ID                int64          `json:"id"`
	Name              string         `json:"name"`
	Algorithm         string         `json:"algorithm"`
	Stage             string         `json:"stage"`
	TrainedFrom       time.Time      `json:"trained_from"`
	TrainedTo         time.Time      `json:"trained_to"`
	ValidationFrom    *time.Time     `json:"validation_from,omitempty"`
	ValidationTo      *time.Time     `json:"validation_to,omitempty"`
	TrainingMetrics   map[string]any `json:"training_metrics"`
	ValidationMetrics map[string]any `json:"validation_metrics"`
	CreatedAt         time.Time      `json:"created_at"`
}

type SpikePredictionEvidence = spike.PredictionEvidence
type SpikeModelGate = spike.ModelGate

type SpikeCandidate struct {
	Ticker                 string          `json:"ticker"`
	Rank                   int             `json:"rank"`
	Probability            float64         `json:"probability"`
	Phase                  string          `json:"phase"`
	Confirmation           string          `json:"confirmation"`
	PatternRank            int             `json:"pattern_rank,omitempty"`
	PatternScore           float64         `json:"pattern_score,omitempty"`
	PatternCoverage        float64         `json:"pattern_coverage,omitempty"`
	PatternState           string          `json:"pattern_state,omitempty"`
	PatternVersion         string          `json:"pattern_version,omitempty"`
	PatternObservedAt      *time.Time      `json:"pattern_observed_at,omitempty"`
	PatternReasons         []string        `json:"pattern_reasons,omitempty"`
	PatternMissingFeatures []string        `json:"pattern_missing_features,omitempty"`
	Snapshot               *MarketSnapshot `json:"snapshot,omitempty"`
	Quote                  *Quote          `json:"quote,omitempty"`
}

type SpikeWatch struct {
	ModelID           int64                   `json:"model_id"`
	ModelName         string                  `json:"model_name"`
	Algorithm         string                  `json:"algorithm"`
	TargetTradingDate time.Time               `json:"target_trading_date"`
	RankingAsOf       time.Time               `json:"ranking_as_of"`
	GeneratedAt       time.Time               `json:"generated_at"`
	RankingCount      int                     `json:"ranking_count"`
	Evidence          SpikePredictionEvidence `json:"evidence"`
	ModelGate         SpikeModelGate          `json:"model_gate"`
	ValidationSamples int                     `json:"validation_samples"`
	PositiveCount     int                     `json:"positive_count"`
	RecallAt20        float64                 `json:"recall_at_20"`
	RecallAt100       float64                 `json:"recall_at_100"`
	PatternVersion    string                  `json:"pattern_version"`
	PatternStatus     string                  `json:"pattern_status"`
	PatternNote       string                  `json:"pattern_note"`
	Candidates        []SpikeCandidate        `json:"candidates"`
	Note              string                  `json:"note"`
}

type SpikeWatcher interface {
	SpikeWatch(context.Context) (SpikeWatch, error)
}

type LearningDecision struct {
	Decision  string    `json:"decision"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

type LearningCoverage struct {
	TradingDate        time.Time  `json:"trading_date"`
	DailyBars          int64      `json:"daily_bars"`
	FeatureSnapshots   int64      `json:"feature_snapshots"`
	CandidateRankings  int64      `json:"candidate_rankings"`
	MarketQuotes       int64      `json:"market_quotes"`
	MarketQuoteTickers int64      `json:"market_quote_tickers"`
	MarketTicks        int64      `json:"market_ticks"`
	MarketTickTickers  int64      `json:"market_tick_tickers"`
	ScannerSignals     int64      `json:"scanner_signals"`
	NewsItems          int64      `json:"news_items"`
	SECFilings         int64      `json:"sec_filings"`
	ExecutionOrders    int64      `json:"execution_orders"`
	ExecutionFills     int64      `json:"execution_fills"`
	StrategyCycles     int64      `json:"strategy_cycles"`
	FirstMarketEvent   *time.Time `json:"first_market_event,omitempty"`
	LastMarketEvent    *time.Time `json:"last_market_event,omitempty"`
}

type StrategyEvidence struct {
	TradingDate              *time.Time `json:"trading_date,omitempty"`
	ShadowStrategyVersion    string     `json:"shadow_strategy_version,omitempty"`
	ShadowFirstTradingDate   *time.Time `json:"shadow_first_trading_date,omitempty"`
	ShadowLastTradingDate    *time.Time `json:"shadow_last_trading_date,omitempty"`
	ShadowTradingDays        int64      `json:"shadow_trading_days"`
	ValidShadowCycles        int64      `json:"valid_shadow_cycles"`
	ExcludedCycles           int64      `json:"excluded_cycles"`
	ShadowNetPnL             float64    `json:"shadow_net_pnl"`
	ShadowGrossProfit        float64    `json:"shadow_gross_profit"`
	ShadowGrossLoss          float64    `json:"shadow_gross_loss"`
	ShadowProfitFactor       float64    `json:"shadow_profit_factor"`
	ShadowFees               float64    `json:"shadow_fees"`
	ReplayCycles             int64      `json:"replay_cycles"`
	ReplayMatchedClosed      int64      `json:"replay_matched_closed"`
	ReplayInconclusive       int64      `json:"replay_inconclusive"`
	ActualNetPnL             float64    `json:"actual_net_pnl"`
	ChallengerNetPnLDelta    float64    `json:"challenger_net_pnl_delta"`
	PartialFillTransitions   int64      `json:"partial_fill_transitions"`
	StopCancelReplacements   int64      `json:"stop_cancel_replacements"`
	RestartRecoveryEvents    int64      `json:"restart_recovery_events"`
	DisconnectRecoveryEvents int64      `json:"disconnect_recovery_events"`
}

type CertificationCheck struct {
	Code     string `json:"code"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Passed   bool   `json:"passed"`
	Detail   string `json:"detail"`
}

type StrategyCertification struct {
	Eligible bool                 `json:"eligible"`
	Decision string               `json:"decision"`
	Checks   []CertificationCheck `json:"checks"`
}

type LearningReport struct {
	Champion        *ModelSummary         `json:"champion,omitempty"`
	Challenger      *ModelSummary         `json:"challenger,omitempty"`
	SpikeModel      *ModelSummary         `json:"spike_model,omitempty"`
	SpikeEvaluation *spike.Report         `json:"spike_evaluation,omitempty"`
	LastDecision    *LearningDecision     `json:"last_decision,omitempty"`
	Coverage        LearningCoverage      `json:"coverage"`
	Strategy        StrategyEvidence      `json:"strategy"`
	Certification   StrategyCertification `json:"certification"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

type EventSource interface {
	Events(context.Context) (<-chan Event, error)
}

type Repository interface {
	Candidates(context.Context) ([]Candidate, error)
	Scan(context.Context) ([]ScanSignal, error)
	Watchlist(context.Context) ([]WatchlistItem, error)
	UpsertWatchlist(context.Context, WatchlistInput) (WatchlistItem, error)
	DeleteWatchlist(context.Context, string) error
	Positions(context.Context) ([]Position, error)
	Trades(context.Context) ([]Trade, error)
	CreateTrade(context.Context, TradeInput) (Trade, error)
	ApplyTradeEvent(context.Context, int64, TradeEventInput) (Trade, error)
	ScoreHistory(context.Context, string) ([]ScorePoint, error)
	Alerts(context.Context) ([]Alert, error)
	AcknowledgeAlert(context.Context, int64) error
	SystemHealth(context.Context) (SystemHealth, error)
	BrokerPositions(context.Context) ([]BrokerPosition, error)
	BrokerOrders(context.Context) ([]BrokerOrder, error)
	LearningReport(context.Context) (LearningReport, error)
}
