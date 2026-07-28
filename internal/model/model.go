package model

import "time"

// Timespan identifies the aggregation window requested from the market-data source.
type Timespan string

const (
	TimespanUnknown Timespan = ""
	TimespanMinute  Timespan = "minute"
	TimespanDay     Timespan = "day"
)

// AggregateQuery describes a bounded historical aggregate request.
type AggregateQuery struct {
	Ticker     string
	Multiplier int
	Timespan   Timespan
	From       string
	To         string
}

// AggregateBar is the source-neutral representation returned by market-data adapters.
type AggregateBar struct {
	Ticker       string
	Timestamp    time.Time
	Open         float64
	High         float64
	Low          float64
	Close        float64
	Volume       float64
	VWAP         *float64
	Transactions *int64
}

type Stock struct {
	ID          int64
	Ticker      string
	CompanyName string
	Exchange    string
	Sector      string
	MarketCap   *float64
	FloatShares *int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DailyPrice struct {
	StockID      int64
	Date         time.Time
	Open         float64
	High         float64
	Low          float64
	Close        float64
	Volume       float64
	VWAP         *float64
	Transactions *int64
}

type IntradayPrice struct {
	StockID      int64
	Timestamp    time.Time
	Timespan     Timespan
	Multiplier   int
	Open         float64
	High         float64
	Low          float64
	Close        float64
	Volume       float64
	VWAP         *float64
	Transactions *int64
}

type News struct {
	ID            int64
	Ticker        string
	PublishedAt   time.Time
	Title         string
	Content       string
	SourceURL     string
	CatalystScore *float64
	CreatedAt     time.Time
}

type SECFiling struct {
	ID            int64
	Ticker        string
	FormType      string
	FiledAt       time.Time
	AccessionNo   string
	SourceURL     string
	DilutionScore *float64
	CreatedAt     time.Time
}

type Trade struct {
	ID         int64
	Ticker     string
	EnteredAt  time.Time
	ExitedAt   *time.Time
	EntryPrice float64
	ExitPrice  *float64
	Strategy   string
	PNL        *float64
	Notes      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
