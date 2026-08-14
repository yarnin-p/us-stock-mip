package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

/* Risk ceilings that outlive a restart.
 *
 * Without this the settings screen would be a lie by tomorrow morning: widen a limit,
 * restart for any reason, and the environment silently puts the old one back. A
 * ceiling that reverts without saying so is worse than one that never moved.
 *
 * Null columns mean "the environment still decides this one", which is why every field
 * is a pointer here. Zero already means "no ceiling" to the risk engine, so zero and
 * unset cannot share a representation.
 */

// RuntimeLimits mirrors dashboard.LimitOverrides for reads at boot.
type RuntimeLimits struct {
	MaxPositionValue     *float64
	MaxGrossExposure     *float64
	MaxCapitalAllocation *float64
	MaxDailyLoss         *float64
	MaxRiskPerTrade      *float64
	AllowedSessions      []string
	KillSwitch           *bool
	Note                 string
}

func (store *Store) RuntimeLimits(ctx context.Context) (RuntimeLimits, error) {
	var limits RuntimeLimits
	err := store.pool.QueryRow(ctx, `
		SELECT max_position_value, max_gross_exposure, max_capital_allocation,
		       max_daily_loss, max_risk_per_trade, allowed_sessions, kill_switch,
		       coalesce(note,'')
		  FROM runtime_limits WHERE id`,
	).Scan(
		&limits.MaxPositionValue, &limits.MaxGrossExposure, &limits.MaxCapitalAllocation,
		&limits.MaxDailyLoss, &limits.MaxRiskPerTrade, &limits.AllowedSessions,
		&limits.KillSwitch, &limits.Note,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeLimits{}, nil
	}
	if err != nil {
		return RuntimeLimits{}, fmt.Errorf("reading runtime limits: %w", err)
	}
	return limits, nil
}

// SaveRuntimeLimits writes the overrides and records what moved. The change log is
// written in the same transaction: a ceiling that changed without a trace is the kind
// of thing nobody can account for after a loss.
func (store *Store) SaveRuntimeLimits(
	ctx context.Context, previous execution.Limits, next dashboard.LimitOverrides,
) error {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("saving runtime limits: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, err := transaction.Exec(ctx, `
		INSERT INTO runtime_limits (
			id, max_position_value, max_gross_exposure, max_capital_allocation,
			max_daily_loss, max_risk_per_trade, allowed_sessions, kill_switch, note,
			updated_at
		) VALUES (TRUE,$1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),now())
		ON CONFLICT (id) DO UPDATE SET
			max_position_value=EXCLUDED.max_position_value,
			max_gross_exposure=EXCLUDED.max_gross_exposure,
			max_capital_allocation=EXCLUDED.max_capital_allocation,
			max_daily_loss=EXCLUDED.max_daily_loss,
			max_risk_per_trade=EXCLUDED.max_risk_per_trade,
			allowed_sessions=EXCLUDED.allowed_sessions,
			kill_switch=EXCLUDED.kill_switch,
			note=EXCLUDED.note,
			updated_at=now()`,
		next.MaxPositionValue, next.MaxGrossExposure, next.MaxCapitalAllocation,
		next.MaxDailyLoss, next.MaxRiskPerTrade, next.AllowedSessions,
		next.KillSwitch, next.Note,
	); err != nil {
		return fmt.Errorf("saving runtime limits: %w", err)
	}

	changes := []struct {
		field string
		was   any
		now   any
	}{
		{"max_position_value", previous.MaxPositionValue, deref(next.MaxPositionValue)},
		{"max_gross_exposure", previous.MaxGrossExposure, deref(next.MaxGrossExposure)},
		{"max_capital_allocation", previous.MaxCapitalAllocation, deref(next.MaxCapitalAllocation)},
		{"max_daily_loss", previous.MaxDailyLoss, deref(next.MaxDailyLoss)},
		{"max_risk_per_trade", previous.MaxRiskPerTrade, deref(next.MaxRiskPerTrade)},
		{"kill_switch", previous.KillSwitch, derefBool(next.KillSwitch)},
	}
	for _, change := range changes {
		was := fmt.Sprint(change.was)
		now := fmt.Sprint(change.now)
		if was == now {
			continue
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO runtime_limit_changes (field, old_value, new_value, note)
			VALUES ($1,$2,$3,NULLIF($4,''))`,
			change.field, was, now, next.Note,
		); err != nil {
			return fmt.Errorf("recording the change to %s: %w", change.field, err)
		}
	}
	return transaction.Commit(ctx)
}

func deref(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func derefBool(value *bool) bool {
	return value != nil && *value
}
