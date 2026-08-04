package dashboard

import "testing"

func TestEvaluateStrategyCertificationUsesForwardShadowProofForPromotion(t *testing.T) {
	evidence := StrategyEvidence{
		ValidShadowCycles:        24,
		ShadowTradingDays:        3,
		ShadowNetPnL:             8.40,
		ShadowGrossProfit:        28.40,
		ShadowGrossLoss:          20,
		ShadowProfitFactor:       1.42,
		ReplayCycles:             14,
		ReplayMatchedClosed:      12,
		ReplayInconclusive:       2,
		StopCancelReplacements:   1,
		RestartRecoveryEvents:    1,
		DisconnectRecoveryEvents: 1,
	}
	got := EvaluateStrategyCertification(evidence)
	if !got.Eligible || got.Decision != "ELIGIBLE_FOR_MANUAL_PROMOTION" {
		t.Fatalf("certification = %#v", got)
	}
	for _, item := range got.Checks {
		if item.Required && !item.Passed {
			t.Fatalf("unexpected failed promotion gate: %#v", item)
		}
	}
}

func TestEvaluateStrategyCertificationBlocksWeakOrMissingEvidence(t *testing.T) {
	got := EvaluateStrategyCertification(StrategyEvidence{
		ValidShadowCycles:      4,
		ShadowTradingDays:      1,
		ShadowNetPnL:           -5,
		ShadowGrossProfit:      3,
		ShadowGrossLoss:        8,
		ShadowProfitFactor:     0.375,
		ReplayCycles:           4,
		ReplayMatchedClosed:    2,
		ReplayInconclusive:     2,
		ChallengerNetPnLDelta:  1,
		RestartRecoveryEvents:  1,
		StopCancelReplacements: 1,
	})
	if got.Eligible || got.Decision != "BLOCKED" {
		t.Fatalf("certification = %#v", got)
	}
	if len(got.Checks) != 11 {
		t.Fatalf("check count = %d", len(got.Checks))
	}
	for _, item := range got.Checks {
		if item.Code == "actual_pnl" && item.Required {
			t.Fatalf("actual live pnl must be a post-live monitor: %#v", item)
		}
		if item.Code == "partial_fill" && item.Required {
			t.Fatalf("partial fill must be a post-live monitor: %#v", item)
		}
	}
}
