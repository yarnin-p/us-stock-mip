package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/feature"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

const maxFeaturePeriod = 10_000

func (store *Store) LoadFeatureObservation(
	ctx context.Context,
	ticker string,
	asOf time.Time,
	lookback int,
) (feature.Observation, error) {
	if ticker == "" {
		return feature.Observation{}, errors.New("ticker is required")
	}
	if asOf.IsZero() {
		return feature.Observation{}, errors.New("as-of date is required")
	}
	if lookback < 1 {
		return feature.Observation{}, errors.New("lookback must be positive")
	}

	var (
		stockID      int64
		storedTicker string
	)
	if err := store.pool.QueryRow(
		ctx,
		`SELECT id, ticker FROM stocks WHERE ticker = $1`,
		ticker,
	).Scan(&stockID, &storedTicker); err != nil {
		return feature.Observation{}, fmt.Errorf("loading stock %s: %w", ticker, err)
	}

	prices, err := store.loadDailyPrices(ctx, stockID, asOf, lookback+1)
	if err != nil {
		return feature.Observation{}, fmt.Errorf("loading %s daily prices: %w", ticker, err)
	}
	if len(prices) == 0 || !sameDate(prices[0].Date, asOf) {
		return feature.Observation{}, fmt.Errorf(
			"loading %s current daily price for %s: %w",
			ticker,
			asOf.Format(time.DateOnly),
			pgx.ErrNoRows,
		)
	}

	current := prices[0]
	prior := prices[1:]
	reverseDailyPrices(prior)
	floatShares, err := store.loadFloatShares(ctx, stockID, asOf)
	if err != nil {
		return feature.Observation{}, fmt.Errorf("loading %s float history: %w", ticker, err)
	}

	premarketClose, err := store.loadSessionClose(
		ctx,
		stockID,
		asOf,
		"04:00:00",
		"09:30:00",
	)
	if err != nil {
		return feature.Observation{}, fmt.Errorf(
			"loading %s premarket close: %w",
			ticker,
			err,
		)
	}
	afterHoursClose, err := store.loadSessionClose(
		ctx,
		stockID,
		asOf,
		"16:00:00",
		"20:00:00",
	)
	if err != nil {
		return feature.Observation{}, fmt.Errorf(
			"loading %s after-hours close: %w",
			ticker,
			err,
		)
	}
	intelligence, err := store.loadIntelligence(ctx, stockID, asOf)
	if err != nil {
		return feature.Observation{}, fmt.Errorf(
			"loading %s intelligence: %w",
			ticker,
			err,
		)
	}

	return feature.Observation{
		StockID:         stockID,
		Ticker:          storedTicker,
		AsOf:            dateOnly(asOf),
		Prior:           prior,
		Current:         current,
		PremarketClose:  premarketClose,
		AfterHoursClose: afterHoursClose,
		FloatShares:     floatShares,
		Intelligence:    intelligence,
	}, nil
}

func (store *Store) loadFloatShares(
	ctx context.Context,
	stockID int64,
	asOf time.Time,
) (*int64, error) {
	const query = `
		SELECT float_shares
		FROM stock_float_history
		WHERE stock_id=$1 AND available_at < ($2::date + INTERVAL '1 day')
		ORDER BY available_at DESC
		LIMIT 1`
	var floatShares int64
	err := store.pool.QueryRow(ctx, query, stockID, dateOnly(asOf)).Scan(&floatShares)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &floatShares, nil
}

func (store *Store) loadIntelligence(
	ctx context.Context,
	stockID int64,
	asOf time.Time,
) (feature.Intelligence, error) {
	const query = `
		SELECT news_score,fda_score,ma_score,theme_score,atm_risk,
			offering_risk,reverse_split_count
		FROM intelligence_snapshots
		WHERE stock_id=$1 AND as_of < ($2::date + INTERVAL '1 day')
		ORDER BY as_of DESC, scorer_version DESC
		LIMIT 1`
	var (
		value             feature.Intelligence
		reverseSplitCount int32
	)
	err := store.pool.QueryRow(ctx, query, stockID, dateOnly(asOf)).Scan(
		&value.NewsScore, &value.FDAScore, &value.MAScore, &value.ThemeScore,
		&value.ATMRisk, &value.OfferingRisk, &reverseSplitCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return feature.Intelligence{}, nil
	}
	if err != nil {
		return feature.Intelligence{}, err
	}
	value.ReverseSplitCount = &reverseSplitCount
	return value, nil
}

func (store *Store) loadDailyPrices(
	ctx context.Context,
	stockID int64,
	asOf time.Time,
	limit int,
) ([]model.DailyPrice, error) {
	const query = `
		SELECT
			stock_id, trade_date, open, high, low, close,
			volume, vwap, transactions
		FROM daily_prices
		WHERE stock_id = $1 AND trade_date <= $2
		ORDER BY trade_date DESC
		LIMIT $3`

	rows, err := store.pool.Query(ctx, query, stockID, dateOnly(asOf), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	prices := make([]model.DailyPrice, 0, limit)
	for rows.Next() {
		var price model.DailyPrice
		if err := rows.Scan(
			&price.StockID,
			&price.Date,
			&price.Open,
			&price.High,
			&price.Low,
			&price.Close,
			&price.Volume,
			&price.VWAP,
			&price.Transactions,
		); err != nil {
			return nil, err
		}
		prices = append(prices, price)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return prices, nil
}

func (store *Store) loadSessionClose(
	ctx context.Context,
	stockID int64,
	asOf time.Time,
	startTime string,
	endTime string,
) (*float64, error) {
	const query = `
		SELECT close
		FROM intraday_prices
		WHERE stock_id = $1
			AND timespan = 'minute'
			AND multiplier = 1
			AND timestamp >= (
				($2::date + $3::time) AT TIME ZONE 'America/New_York'
			)
			AND timestamp < (
				($2::date + $4::time) AT TIME ZONE 'America/New_York'
			)
		ORDER BY timestamp DESC
		LIMIT 1`

	var closePrice float64
	err := store.pool.QueryRow(
		ctx,
		query,
		stockID,
		dateOnly(asOf),
		startTime,
		endTime,
	).Scan(&closePrice)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &closePrice, nil
}

func (store *Store) UpsertFeatureSnapshot(
	ctx context.Context,
	snapshot model.FeatureSnapshot,
) error {
	if err := validateFeatureSnapshot(snapshot); err != nil {
		return err
	}

	const query = `
		INSERT INTO feature_snapshots (
			stock_id, as_of,
			gap_percent, premarket_change, after_hour_change, return_1d,
			relative_volume, volume_spike, float_rotation,
			ema, vwap_distance, breakout_strength,
			news_score, fda_score, ma_score, theme_score,
			atm_risk, offering_risk, reverse_split_count,
			calculator_version,
			relative_volume_period, ema_period, breakout_period
		)
		VALUES (
			$1, $2,
			$3, $4, $5, $6,
			$7, $8, $9,
			$10, $11, $12,
			$13, $14, $15, $16,
			$17, $18, $19,
			$20,
			$21, $22, $23
		)
		ON CONFLICT (
			stock_id,
			as_of,
			calculator_version,
			relative_volume_period,
			ema_period,
			breakout_period
		) DO UPDATE SET
			gap_percent = EXCLUDED.gap_percent,
			premarket_change = EXCLUDED.premarket_change,
			after_hour_change = EXCLUDED.after_hour_change,
			return_1d = EXCLUDED.return_1d,
			relative_volume = EXCLUDED.relative_volume,
			volume_spike = EXCLUDED.volume_spike,
			float_rotation = EXCLUDED.float_rotation,
			ema = EXCLUDED.ema,
			vwap_distance = EXCLUDED.vwap_distance,
			breakout_strength = EXCLUDED.breakout_strength,
			news_score = COALESCE(EXCLUDED.news_score, feature_snapshots.news_score),
			fda_score = COALESCE(EXCLUDED.fda_score, feature_snapshots.fda_score),
			ma_score = COALESCE(EXCLUDED.ma_score, feature_snapshots.ma_score),
			theme_score = COALESCE(EXCLUDED.theme_score, feature_snapshots.theme_score),
			atm_risk = COALESCE(EXCLUDED.atm_risk, feature_snapshots.atm_risk),
			offering_risk = COALESCE(EXCLUDED.offering_risk, feature_snapshots.offering_risk),
			reverse_split_count = COALESCE(
				EXCLUDED.reverse_split_count,
				feature_snapshots.reverse_split_count
			),
			updated_at = NOW()`

	_, err := store.pool.Exec(
		ctx,
		query,
		snapshot.StockID,
		dateOnly(snapshot.AsOf),
		snapshot.GapPercent,
		snapshot.PremarketChange,
		snapshot.AfterHourChange,
		snapshot.Return1D,
		snapshot.RelativeVolume,
		snapshot.VolumeSpike,
		snapshot.FloatRotation,
		snapshot.EMA,
		snapshot.VWAPDistance,
		snapshot.BreakoutStrength,
		snapshot.NewsScore,
		snapshot.FDAScore,
		snapshot.MAScore,
		snapshot.ThemeScore,
		snapshot.ATMRisk,
		snapshot.OfferingRisk,
		snapshot.ReverseSplitCount,
		snapshot.CalculatorVersion,
		snapshot.RelativeVolumePeriod,
		snapshot.EMAPeriod,
		snapshot.BreakoutPeriod,
	)
	if err != nil {
		return fmt.Errorf(
			"upserting feature snapshot for stock %d on %s: %w",
			snapshot.StockID,
			snapshot.AsOf.Format(time.DateOnly),
			err,
		)
	}
	return nil
}

func validateFeatureSnapshot(snapshot model.FeatureSnapshot) error {
	if snapshot.StockID < 1 {
		return errors.New("feature snapshot stock ID must be positive")
	}
	if snapshot.AsOf.IsZero() {
		return errors.New("feature snapshot as-of date is required")
	}
	if snapshot.CalculatorVersion < 1 {
		return errors.New("feature snapshot calculator version must be positive")
	}
	if snapshot.RelativeVolumePeriod < 1 ||
		snapshot.EMAPeriod < 1 ||
		snapshot.BreakoutPeriod < 1 {
		return errors.New("feature snapshot periods must be positive")
	}
	if snapshot.RelativeVolumePeriod > maxFeaturePeriod ||
		snapshot.EMAPeriod > maxFeaturePeriod ||
		snapshot.BreakoutPeriod > maxFeaturePeriod {
		return fmt.Errorf(
			"feature snapshot periods must not exceed %d",
			maxFeaturePeriod,
		)
	}

	values := []*float64{
		snapshot.GapPercent,
		snapshot.PremarketChange,
		snapshot.AfterHourChange,
		snapshot.Return1D,
		snapshot.RelativeVolume,
		snapshot.VolumeSpike,
		snapshot.FloatRotation,
		snapshot.EMA,
		snapshot.VWAPDistance,
		snapshot.BreakoutStrength,
		snapshot.NewsScore,
		snapshot.FDAScore,
		snapshot.MAScore,
		snapshot.ThemeScore,
		snapshot.ATMRisk,
		snapshot.OfferingRisk,
	}
	for _, value := range values {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return errors.New("feature snapshot values must be finite")
		}
	}
	for _, value := range []*float64{
		snapshot.NewsScore,
		snapshot.FDAScore,
		snapshot.MAScore,
		snapshot.ThemeScore,
		snapshot.ATMRisk,
		snapshot.OfferingRisk,
	} {
		if value != nil && (*value < 0 || *value > 1) {
			return errors.New("feature snapshot scores and risks must be between zero and one")
		}
	}
	if snapshot.ReverseSplitCount != nil && *snapshot.ReverseSplitCount < 0 {
		return errors.New("feature snapshot reverse-split count must not be negative")
	}
	return nil
}

func reverseDailyPrices(prices []model.DailyPrice) {
	for left, right := 0, len(prices)-1; left < right; left, right = left+1, right-1 {
		prices[left], prices[right] = prices[right], prices[left]
	}
}

func sameDate(left, right time.Time) bool {
	return dateOnly(left).Equal(dateOnly(right))
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
