package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
)

var openingTickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

// UpsertMarketDailyBars imports a market-wide Massive daily summary. Although
// the complete bar is retained for future sessions, opening-list queries use
// only the current day's open and strictly earlier rows for every other field.
func (store *Store) UpsertMarketDailyBars(
	ctx context.Context,
	tradingDate time.Time,
	bars []model.AggregateBar,
	complete bool,
) error {
	if tradingDate.IsZero() {
		return errors.New("market daily trading date is required")
	}
	if len(bars) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(bars))
	seen := make(map[string]struct{}, len(bars))
	for _, bar := range bars {
		if err := validateMarketDailyBar(bar); err != nil {
			return err
		}
		if _, ok := seen[bar.Ticker]; ok {
			return fmt.Errorf("duplicate market daily bar for %s", bar.Ticker)
		}
		seen[bar.Ticker] = struct{}{}
		rows = append(rows, []any{
			bar.Ticker,
			bar.Open,
			bar.High,
			bar.Low,
			bar.Close,
			bar.Volume,
			nullableFloat(bar.VWAP),
			nullableInt64(bar.Transactions),
		})
	}
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			CREATE TEMP TABLE mip_market_daily_import (
				ticker TEXT NOT NULL,
				open DOUBLE PRECISION NOT NULL,
				high DOUBLE PRECISION NOT NULL,
				low DOUBLE PRECISION NOT NULL,
				close DOUBLE PRECISION NOT NULL,
				volume DOUBLE PRECISION NOT NULL,
				vwap DOUBLE PRECISION,
				transactions BIGINT
			) ON COMMIT DROP`); err != nil {
			return err
		}
		if _, err := tx.CopyFrom(
			ctx,
			pgx.Identifier{"mip_market_daily_import"},
			[]string{
				"ticker", "open", "high", "low", "close", "volume", "vwap", "transactions",
			},
			pgx.CopyFromRows(rows),
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO stocks (ticker)
			SELECT ticker FROM mip_market_daily_import
			ON CONFLICT (ticker) DO NOTHING`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO daily_prices (
				stock_id,trade_date,open,high,low,close,volume,vwap,transactions
			)
			SELECT
				s.id,$1,i.open,i.high,i.low,i.close,i.volume,i.vwap,i.transactions
			FROM mip_market_daily_import i
			JOIN stocks s ON s.ticker=i.ticker
			ON CONFLICT (stock_id,trade_date) DO UPDATE SET
				open=EXCLUDED.open,
				high=EXCLUDED.high,
				low=EXCLUDED.low,
				close=EXCLUDED.close,
				volume=EXCLUDED.volume,
				vwap=EXCLUDED.vwap,
				transactions=EXCLUDED.transactions,
				updated_at=NOW()`,
			dateOnly(tradingDate),
		); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO market_daily_imports (trading_date,bar_count,complete)
			VALUES ($1,$2,$3)
			ON CONFLICT (trading_date) DO UPDATE SET
				bar_count=EXCLUDED.bar_count,
				complete=EXCLUDED.complete,
				imported_at=NOW()`,
			dateOnly(tradingDate),
			len(bars),
			complete,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf(
			"upserting %d market daily bars for %s: %w",
			len(bars),
			tradingDate.Format(time.DateOnly),
			err,
		)
	}
	return nil
}

func (store *Store) MarketDailyImport(
	ctx context.Context,
	tradingDate time.Time,
) (bool, int, bool, error) {
	if tradingDate.IsZero() {
		return false, 0, false, errors.New("market daily trading date is required")
	}
	var barCount int
	var complete bool
	if err := store.pool.QueryRow(
		ctx,
		`SELECT bar_count,complete
		 FROM market_daily_imports WHERE trading_date=$1`,
		dateOnly(tradingDate),
	).Scan(&barCount, &complete); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, false, nil
		}
		return false, 0, false, fmt.Errorf("checking market daily import: %w", err)
	}
	return true, barCount, complete, nil
}

func (store *Store) UpsertMarketUniverse(
	ctx context.Context,
	tradingDate time.Time,
	stocks []model.Stock,
	complete bool,
) error {
	if tradingDate.IsZero() {
		return errors.New("market-universe trading date is required")
	}
	if len(stocks) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(stocks))
	seen := make(map[string]struct{}, len(stocks))
	for _, stock := range stocks {
		if !openingTickerPattern.MatchString(stock.Ticker) || stock.SecurityType != "CS" {
			return fmt.Errorf("invalid common-stock universe member %q", stock.Ticker)
		}
		if _, ok := seen[stock.Ticker]; ok {
			return fmt.Errorf("duplicate common-stock universe member %s", stock.Ticker)
		}
		seen[stock.Ticker] = struct{}{}
		rows = append(rows, []any{
			stock.Ticker,
			stock.CompanyName,
			stock.Exchange,
			stock.SecurityType,
		})
	}
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			CREATE TEMP TABLE mip_market_universe_import (
				ticker TEXT NOT NULL,
				company_name TEXT,
				exchange TEXT,
				security_type TEXT NOT NULL
			) ON COMMIT DROP`); err != nil {
			return err
		}
		if _, err := tx.CopyFrom(
			ctx,
			pgx.Identifier{"mip_market_universe_import"},
			[]string{"ticker", "company_name", "exchange", "security_type"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO stocks (ticker,company_name,exchange,security_type)
			SELECT
				ticker,NULLIF(company_name,''),NULLIF(exchange,''),security_type
			FROM mip_market_universe_import
			ON CONFLICT (ticker) DO UPDATE SET
				company_name=COALESCE(EXCLUDED.company_name,stocks.company_name),
				exchange=COALESCE(EXCLUDED.exchange,stocks.exchange),
				security_type=EXCLUDED.security_type,
				updated_at=NOW()`); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`DELETE FROM market_universe_memberships WHERE trading_date=$1`,
			dateOnly(tradingDate),
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO market_universe_memberships (
				trading_date,stock_id,security_type
			)
			SELECT $1,s.id,i.security_type
			FROM mip_market_universe_import i
			JOIN stocks s ON s.ticker=i.ticker`,
			dateOnly(tradingDate),
		); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO market_universe_imports (
				trading_date,member_count,complete
			) VALUES ($1,$2,$3)
			ON CONFLICT (trading_date) DO UPDATE SET
				member_count=EXCLUDED.member_count,
				complete=EXCLUDED.complete,
				imported_at=NOW()`,
			dateOnly(tradingDate),
			len(stocks),
			complete,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf(
			"upserting %d common-stock universe members for %s: %w",
			len(stocks),
			tradingDate.Format(time.DateOnly),
			err,
		)
	}
	return nil
}

func (store *Store) MarketUniverseImport(
	ctx context.Context,
	tradingDate time.Time,
) (bool, int, bool, error) {
	if tradingDate.IsZero() {
		return false, 0, false, errors.New("market-universe trading date is required")
	}
	var memberCount int
	var complete bool
	if err := store.pool.QueryRow(
		ctx,
		`SELECT member_count,complete
		 FROM market_universe_imports WHERE trading_date=$1`,
		dateOnly(tradingDate),
	).Scan(&memberCount, &complete); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, false, nil
		}
		return false, 0, false, fmt.Errorf("checking market-universe import: %w", err)
	}
	return true, memberCount, complete, nil
}

func (store *Store) UpsertOpeningResearch(
	ctx context.Context,
	stock model.Stock,
	effectiveAt time.Time,
) error {
	if !openingTickerPattern.MatchString(stock.Ticker) || effectiveAt.IsZero() {
		return errors.New("opening research ticker and effective timestamp are required")
	}
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		var stockID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO stocks (
				ticker,company_name,exchange,sector,security_type,
				market_cap,float_shares
			) VALUES (
				$1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,''),
				NULLIF($5,''),$6,$7
			)
			ON CONFLICT (ticker) DO UPDATE SET
				company_name=COALESCE(EXCLUDED.company_name,stocks.company_name),
				exchange=COALESCE(EXCLUDED.exchange,stocks.exchange),
				sector=COALESCE(EXCLUDED.sector,stocks.sector),
				security_type=COALESCE(EXCLUDED.security_type,stocks.security_type),
				market_cap=COALESCE(EXCLUDED.market_cap,stocks.market_cap),
				float_shares=COALESCE(EXCLUDED.float_shares,stocks.float_shares),
				updated_at=NOW()
			RETURNING id`,
			stock.Ticker,
			stock.CompanyName,
			stock.Exchange,
			stock.Sector,
			stock.SecurityType,
			stock.MarketCap,
			stock.FloatShares,
		).Scan(&stockID); err != nil {
			return err
		}
		if stock.MarketCap != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO stock_market_cap_history (
					stock_id,available_at,market_cap,source
				) VALUES ($1,$2,$3,'massive_ticker_overview')
				ON CONFLICT (stock_id,available_at,source) DO UPDATE SET
					market_cap=EXCLUDED.market_cap`,
				stockID, effectiveAt, *stock.MarketCap,
			); err != nil {
				return err
			}
		}
		if stock.FloatShares != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO stock_float_history (
					stock_id,available_at,float_shares,source
				) VALUES ($1,$2,$3,'massive_float')
				ON CONFLICT (stock_id,available_at,source) DO UPDATE SET
					float_shares=EXCLUDED.float_shares`,
				stockID, effectiveAt, *stock.FloatShares,
			); err != nil {
				return err
			}
		}
		if stock.Sector != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO stock_sector_history (
					stock_id,effective_at,sector
				) VALUES ($1,$2,$3)
				ON CONFLICT (stock_id,effective_at,source) DO UPDATE SET
					sector=EXCLUDED.sector`,
				stockID, effectiveAt, stock.Sector,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *Store) LoadOpeningInputs(
	ctx context.Context,
	tradingDate, marketOpenAt time.Time,
	lookback int,
) ([]opening.Input, error) {
	if tradingDate.IsZero() || marketOpenAt.IsZero() {
		return nil, errors.New("opening trading date and market-open timestamp are required")
	}
	if lookback < 2 || lookback > 10_000 {
		return nil, errors.New("opening lookback must be between 2 and 10000")
	}
	const query = `
		SELECT
			s.ticker,current_day.open,prior_day.close,prior_day.volume,
			stats.average_volume,stats.average_dollar_volume,
			COALESCE(prior_day.close/NULLIF(second_prior.close,0)-1,0),
			COALESCE(prior_day.close/NULLIF(breakout.highest_high,0)-1,0),
			stats.history_count,
			float_point.float_shares,market_cap_point.market_cap,
			intelligence.catalyst_score,COALESCE(sector_point.sector,''),
			NULL::double precision,
			intelligence.dilution_risk
		FROM daily_prices current_day
		JOIN stocks s ON s.id=current_day.stock_id
		JOIN market_universe_memberships universe
			ON universe.stock_id=current_day.stock_id
			AND universe.trading_date=current_day.trade_date
			AND universe.security_type='CS'
		JOIN LATERAL (
			SELECT price.trade_date,price.close,price.volume
			FROM daily_prices price
			JOIN market_daily_imports imported
				ON imported.trading_date=price.trade_date
				AND imported.complete
			WHERE price.stock_id=current_day.stock_id AND price.trade_date<$1
			ORDER BY price.trade_date DESC
			LIMIT 1
		) prior_day ON TRUE
		LEFT JOIN LATERAL (
			SELECT price.close
			FROM daily_prices price
			JOIN market_daily_imports imported
				ON imported.trading_date=price.trade_date
				AND imported.complete
			WHERE price.stock_id=current_day.stock_id
				AND price.trade_date<prior_day.trade_date
			ORDER BY price.trade_date DESC
			LIMIT 1
		) second_prior ON TRUE
		JOIN LATERAL (
			SELECT
				AVG(history.volume)::double precision AS average_volume,
				AVG(history.close*history.volume)::double precision
					AS average_dollar_volume,
				COUNT(*)::integer AS history_count
			FROM (
				SELECT price.close,price.volume
				FROM daily_prices price
				JOIN market_daily_imports imported
					ON imported.trading_date=price.trade_date
					AND imported.complete
				WHERE price.stock_id=current_day.stock_id AND price.trade_date<$1
				ORDER BY price.trade_date DESC
				LIMIT $3
			) history
		) stats ON stats.average_volume>0
		LEFT JOIN LATERAL (
			SELECT MAX(history.high)::double precision AS highest_high
			FROM (
				SELECT price.high
				FROM daily_prices price
				JOIN market_daily_imports imported
					ON imported.trading_date=price.trade_date
					AND imported.complete
				WHERE price.stock_id=current_day.stock_id
					AND price.trade_date<prior_day.trade_date
				ORDER BY price.trade_date DESC
				LIMIT $3
			) history
		) breakout ON TRUE
			LEFT JOIN LATERAL (
				SELECT float_shares
				FROM stock_float_history
				WHERE stock_id=current_day.stock_id
					AND available_at<=$2
					AND created_at<=$2
				ORDER BY available_at DESC
				LIMIT 1
			) float_point ON TRUE
			LEFT JOIN LATERAL (
				SELECT market_cap::double precision AS market_cap
				FROM stock_market_cap_history
				WHERE stock_id=current_day.stock_id
					AND available_at<=$2
					AND created_at<=$2
				ORDER BY available_at DESC
				LIMIT 1
			) market_cap_point ON TRUE
			LEFT JOIN LATERAL (
				SELECT sector
				FROM stock_sector_history
				WHERE stock_id=current_day.stock_id
					AND effective_at<=$2
					AND observed_at<=$2
				ORDER BY effective_at DESC
				LIMIT 1
		) sector_point ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				GREATEST(news_score,fda_score,ma_score,theme_score)::double precision
					AS catalyst_score,
				GREATEST(
					atm_risk,
					offering_risk,
					CASE WHEN reverse_split_count>0 THEN 0.5 ELSE 0 END
				)::double precision AS dilution_risk
			FROM intelligence_snapshots
			WHERE stock_id=current_day.stock_id AND as_of<=$2
			ORDER BY as_of DESC,scorer_version DESC
			LIMIT 1
		) intelligence ON TRUE
		WHERE current_day.trade_date=$1
		ORDER BY s.ticker`
	rows, err := store.pool.Query(
		ctx,
		query,
		dateOnly(tradingDate),
		marketOpenAt,
		lookback,
	)
	if err != nil {
		return nil, fmt.Errorf("loading opening inputs: %w", err)
	}
	defer rows.Close()
	var inputs []opening.Input
	for rows.Next() {
		var input opening.Input
		if err := rows.Scan(
			&input.Ticker,
			&input.CurrentOpen,
			&input.PriorClose,
			&input.PriorVolume,
			&input.AverageVolume,
			&input.AverageDollarVolume,
			&input.PriorReturn,
			&input.Breakout,
			&input.HistoryCount,
			&input.FloatShares,
			&input.MarketCap,
			&input.CatalystScore,
			&input.Sector,
			&input.SectorScore,
			&input.DilutionRisk,
		); err != nil {
			return nil, fmt.Errorf("scanning opening input: %w", err)
		}
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating opening inputs: %w", err)
	}
	return inputs, nil
}

func (store *Store) SaveOpeningList(
	ctx context.Context,
	tradingDate, marketOpenAt time.Time,
	criteria opening.Criteria,
	candidatesConsidered int,
	candidatesRanked int,
	results []opening.Result,
) (int64, error) {
	if candidatesRanked != len(results) ||
		candidatesConsidered < candidatesRanked ||
		candidatesConsidered < 0 {
		return 0, errors.New("opening candidates considered is invalid")
	}
	criteriaJSON, err := json.Marshal(criteria)
	if err != nil {
		return 0, fmt.Errorf("encoding opening criteria: %w", err)
	}
	digest := sha256.Sum256(criteriaJSON)
	configHash := hex.EncodeToString(digest[:])
	var runID int64
	err = pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO opening_list_runs (
				trading_date,market_open_at,selector_version,config_hash,
				criteria,candidates_considered,candidates_ranked
			) VALUES ($1,$2,$3,$4,$5,$6,$7)
			RETURNING id`,
			dateOnly(tradingDate),
			marketOpenAt,
			opening.SelectorVersion,
			configHash,
			criteriaJSON,
			candidatesConsidered,
			candidatesRanked,
		).Scan(&runID); err != nil {
			return err
		}
		for _, result := range results {
			tag, err := tx.Exec(ctx, `
				INSERT INTO opening_list_entries (
					run_id,stock_id,rank,selected,score,score_coverage,
					open_price,prior_close,gap,prior_volume,average_volume,
					relative_volume,average_dollar_volume,prior_return,breakout,
					history_count,momentum_score,volume_score,float_score,
					catalyst_score,market_cap_score,sector_score,dilution_score
				)
				SELECT
					$1,id,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
					$16,$17,$18,$19,$20,$21,$22
				FROM stocks WHERE ticker=$23`,
				runID,
				result.Rank,
				result.Rank <= criteria.Limit,
				result.Score,
				result.ScoreCoverage,
				result.OpenPrice,
				result.PriorClose,
				result.Gap,
				result.PriorVolume,
				result.AverageVolume,
				result.RelativeVolume,
				result.AverageDollarVolume,
				result.PriorReturn,
				result.Breakout,
				result.HistoryCount,
				result.MomentumScore,
				result.VolumeScore,
				result.FloatScore,
				result.CatalystScore,
				result.MarketCapScore,
				result.SectorScore,
				result.DilutionScore,
				result.Ticker,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("saving opening entry: ticker %s does not exist", result.Ticker)
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("saving opening list: %w", err)
	}
	return runID, nil
}

func validateMarketDailyBar(bar model.AggregateBar) error {
	if !openingTickerPattern.MatchString(bar.Ticker) {
		return fmt.Errorf("invalid market daily ticker %q", bar.Ticker)
	}
	values := []float64{bar.Open, bar.High, bar.Low, bar.Close, bar.Volume}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("market daily bar for %s contains a non-finite value", bar.Ticker)
		}
	}
	if bar.Open <= 0 || bar.High <= 0 || bar.Low <= 0 || bar.Close <= 0 ||
		bar.Volume < 0 || bar.High < bar.Low {
		return fmt.Errorf("market daily bar for %s contains an invalid value", bar.Ticker)
	}
	if bar.VWAP != nil &&
		(math.IsNaN(*bar.VWAP) || math.IsInf(*bar.VWAP, 0) || *bar.VWAP <= 0) {
		return fmt.Errorf("market daily VWAP for %s is invalid", bar.Ticker)
	}
	if bar.Transactions != nil && *bar.Transactions < 0 {
		return fmt.Errorf("market daily transactions for %s is invalid", bar.Ticker)
	}
	return nil
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
