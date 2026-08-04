package opening

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/model"
)

type MarketSource interface {
	DailySummary(context.Context, time.Time) ([]model.AggregateBar, error)
	CommonStocks(context.Context, time.Time) ([]model.Stock, error)
	TickerAt(context.Context, string, time.Time) (model.Stock, error)
	Float(context.Context, string) (*int64, error)
}

type Store interface {
	MarketDailyImport(context.Context, time.Time) (bool, int, bool, error)
	UpsertMarketDailyBars(context.Context, time.Time, []model.AggregateBar, bool) error
	MarketUniverseImport(context.Context, time.Time) (bool, int, bool, error)
	UpsertMarketUniverse(context.Context, time.Time, []model.Stock, bool) error
	UpsertOpeningResearch(context.Context, model.Stock, time.Time) error
	LoadOpeningInputs(context.Context, time.Time, time.Time, int) ([]Input, error)
	SaveOpeningList(
		context.Context,
		time.Time,
		time.Time,
		Criteria,
		int,
		int,
		[]Result,
	) (int64, error)
}

type Job struct {
	From, To           time.Time
	Lookback           int
	Criteria           Criteria
	SyncMarket         bool
	RequestDelay       time.Duration
	MinMarketBars      int
	MinUniverseMembers int
	ResearchLimit      int
}

type DayList struct {
	RunID        int64     `json:"run_id"`
	TradingDate  time.Time `json:"trading_date"`
	MarketOpenAt time.Time `json:"market_open_at"`
	Candidates   int       `json:"candidates_considered"`
	Ranked       int       `json:"candidates_ranked"`
	Entries      []Result  `json:"entries"`
}

type Report struct {
	DaysScanned int       `json:"days_scanned"`
	TradingDays int       `json:"trading_days"`
	Lists       []DayList `json:"lists"`
}

type Service struct {
	source MarketSource
	store  Store
	now    func() time.Time
}

func NewService(source MarketSource, store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("opening-list store is required")
	}
	return &Service{source: source, store: store, now: time.Now}, nil
}

func (service *Service) Run(ctx context.Context, job Job) (Report, error) {
	if err := validateJob(job); err != nil {
		return Report{}, err
	}
	if job.SyncMarket && service.source == nil {
		return Report{}, errors.New("opening-list market source is required when sync is enabled")
	}
	selector, err := NewSelector(job.Criteria)
	if err != nil {
		return Report{}, err
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return Report{}, fmt.Errorf("loading U.S. market timezone: %w", err)
	}
	now := service.now().In(location)
	today := dateOnly(now)
	if dateOnly(job.To).After(today) {
		return Report{}, errors.New("opening-list date range must not include a future date")
	}
	if includesDate(job, today) && IsTradingDay(today) &&
		now.Before(MarketOpenAt(today, location)) {
		return Report{}, errors.New("today's opening list is not available before 09:30 America/New_York")
	}

	start := dateOnly(job.From)
	if job.SyncMarket {
		start = start.AddDate(0, 0, -(job.Lookback*2 + 10))
	}
	end := dateOnly(job.To)
	var report Report
	for date := start; !date.After(end); date = date.AddDate(0, 0, 1) {
		if err := ctx.Err(); err != nil {
			return report, fmt.Errorf("building opening lists: %w", err)
		}
		if !IsTradingDay(date) {
			continue
		}
		inRange := !date.Before(dateOnly(job.From))
		exists, barCount, complete, err := service.store.MarketDailyImport(ctx, date)
		if err != nil {
			return report, err
		}
		universeExists, memberCount, universeComplete, err :=
			service.store.MarketUniverseImport(ctx, date)
		if err != nil {
			return report, err
		}
		if job.SyncMarket && (!exists || !complete || date.Equal(today)) {
			bars, err := service.source.DailySummary(ctx, date)
			if err != nil {
				return report, err
			}
			if len(bars) == 0 {
				if date.Equal(today) {
					return report, errors.New("official opening prints are not available yet")
				}
				if inRange {
					report.DaysScanned++
				}
				continue
			}
			if len(bars) < job.MinMarketBars {
				return report, fmt.Errorf(
					"market summary for %s is incomplete: got %d bars, require at least %d",
					date.Format(time.DateOnly),
					len(bars),
					job.MinMarketBars,
				)
			}
			sessionComplete := date.Before(today) ||
				!now.Before(marketHistoryCompleteAt(date, location))
			if err := service.store.UpsertMarketDailyBars(
				ctx,
				date,
				bars,
				sessionComplete,
			); err != nil {
				return report, err
			}
			exists = true
			barCount = len(bars)
			if err := waitForNextRequest(ctx, job.RequestDelay); err != nil {
				return report, err
			}
		}
		if job.SyncMarket && barCount > 0 &&
			(!universeExists || !universeComplete) {
			stocks, err := service.source.CommonStocks(ctx, date)
			if err != nil {
				return report, err
			}
			if len(stocks) < job.MinUniverseMembers {
				return report, fmt.Errorf(
					"common-stock universe for %s is incomplete: got %d members, require at least %d",
					date.Format(time.DateOnly),
					len(stocks),
					job.MinUniverseMembers,
				)
			}
			if err := service.store.UpsertMarketUniverse(
				ctx,
				date,
				stocks,
				true,
			); err != nil {
				return report, err
			}
			universeExists = true
			memberCount = len(stocks)
			universeComplete = true
			if err := waitForNextRequest(ctx, job.RequestDelay); err != nil {
				return report, err
			}
		}
		if !inRange {
			continue
		}
		report.DaysScanned++
		if !exists || barCount == 0 || !universeExists ||
			memberCount == 0 || !universeComplete {
			continue
		}
		report.TradingDays++
		marketOpenAt := MarketOpenAt(date, location)
		cutoffAt := marketOpenAt
		if date.Equal(today) {
			cutoffAt = now
		}
		inputs, err := service.store.LoadOpeningInputs(
			ctx,
			date,
			cutoffAt,
			job.Lookback,
		)
		if err != nil {
			return report, err
		}
		// Massive's dated ticker overview is keyed to the underlying report
		// period, not necessarily the date the market learned the data. Only
		// observe it live; historical runs must not backdate later knowledge.
		if job.SyncMarket && job.ResearchLimit > 0 && date.Equal(today) {
			preliminary, err := selector.RankAll(date, inputs)
			if err != nil {
				return report, err
			}
			researchCount := min(job.ResearchLimit, len(preliminary))
			for _, candidate := range preliminary[:researchCount] {
				stock, err := service.source.TickerAt(ctx, candidate.Ticker, date)
				if err != nil {
					return report, err
				}
				if stock.SecurityType != "CS" {
					continue
				}
				if err := waitForNextRequest(ctx, job.RequestDelay); err != nil {
					return report, err
				}
				if date.Equal(today) {
					floatShares, err := service.source.Float(ctx, candidate.Ticker)
					if err != nil {
						return report, err
					}
					stock.FloatShares = floatShares
					if err := waitForNextRequest(ctx, job.RequestDelay); err != nil {
						return report, err
					}
				}
				if err := service.store.UpsertOpeningResearch(
					ctx,
					stock,
					service.now(),
				); err != nil {
					return report, err
				}
			}
			cutoffAt = service.now()
			inputs, err = service.store.LoadOpeningInputs(
				ctx,
				date,
				cutoffAt,
				job.Lookback,
			)
			if err != nil {
				return report, err
			}
		}
		ranked, err := selector.RankAll(date, inputs)
		if err != nil {
			return report, err
		}
		selected := ranked
		if len(selected) > job.Criteria.Limit {
			selected = append([]Result(nil), selected[:job.Criteria.Limit]...)
		}
		runID, err := service.store.SaveOpeningList(
			ctx,
			date,
			marketOpenAt,
			job.Criteria,
			len(inputs),
			len(ranked),
			ranked,
		)
		if err != nil {
			return report, err
		}
		report.Lists = append(report.Lists, DayList{
			RunID:        runID,
			TradingDate:  date,
			MarketOpenAt: marketOpenAt,
			Candidates:   len(inputs),
			Ranked:       len(ranked),
			Entries:      selected,
		})
	}
	return report, nil
}

// MarketOpenAt returns the regular U.S. equity market open for a date. The
// America/New_York location accounts for daylight-saving time.
func MarketOpenAt(date time.Time, location *time.Location) time.Time {
	year, month, day := date.Date()
	return time.Date(year, month, day, 9, 30, 0, 0, location)
}

func marketHistoryCompleteAt(date time.Time, location *time.Location) time.Time {
	year, month, day := date.Date()
	// A short buffer after the 16:00 regular close avoids freezing an
	// in-flight grouped response as completed history.
	return time.Date(year, month, day, 16, 15, 0, 0, location)
}

func validateJob(job Job) error {
	if job.From.IsZero() || job.To.IsZero() {
		return errors.New("opening-list date range is required")
	}
	if dateOnly(job.To).Before(dateOnly(job.From)) {
		return errors.New("opening-list end date must not precede start date")
	}
	if job.Lookback < 2 || job.Lookback > 10_000 {
		return errors.New("opening-list lookback must be between 2 and 10000")
	}
	if job.RequestDelay < 0 || job.RequestDelay > time.Minute {
		return errors.New("opening-list request delay is invalid")
	}
	if job.MinMarketBars < 1 || job.MinMarketBars > 50_000 {
		return errors.New("opening-list minimum market bars is invalid")
	}
	if job.MinUniverseMembers < 1 || job.MinUniverseMembers > 50_000 {
		return errors.New("opening-list minimum universe members is invalid")
	}
	if job.ResearchLimit < 0 || job.ResearchLimit > 1_000 {
		return errors.New("opening-list research limit is invalid")
	}
	return nil
}

func includesDate(job Job, date time.Time) bool {
	date = dateOnly(date)
	return !date.Before(dateOnly(job.From)) && !date.After(dateOnly(job.To))
}

func waitForNextRequest(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting for Massive rate limit: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
