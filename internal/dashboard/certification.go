package dashboard

import "fmt"

const (
	minimumShadowCycles       = 20
	minimumShadowTradingDays  = 3
	minimumShadowProfitFactor = 1.15
	minimumReplayCycles       = 10
)

func EvaluateStrategyCertification(
	evidence StrategyEvidence,
) StrategyCertification {
	inconclusiveRate := 1.0
	if evidence.ReplayCycles > 0 {
		inconclusiveRate = float64(evidence.ReplayInconclusive) /
			float64(evidence.ReplayCycles)
	}
	checks := []CertificationCheck{
		check(
			"shadow_sample",
			"Valid shadow sample",
			true,
			evidence.ValidShadowCycles >= minimumShadowCycles,
			fmt.Sprintf(
				"%d valid cycles; minimum %d",
				evidence.ValidShadowCycles,
				minimumShadowCycles,
			),
		),
		check(
			"shadow_days",
			"Forward shadow coverage",
			true,
			evidence.ShadowTradingDays >= minimumShadowTradingDays,
			fmt.Sprintf(
				"%d trading days; minimum %d",
				evidence.ShadowTradingDays,
				minimumShadowTradingDays,
			),
		),
		check(
			"shadow_pnl",
			"Positive forward shadow net PnL",
			true,
			evidence.ShadowNetPnL > 0,
			fmt.Sprintf("$%.2f after fees", evidence.ShadowNetPnL),
		),
		check(
			"shadow_profit_factor",
			"Forward shadow profit factor",
			true,
			evidence.ShadowProfitFactor >= minimumShadowProfitFactor,
			fmt.Sprintf(
				"%.2f; minimum %.2f",
				evidence.ShadowProfitFactor,
				minimumShadowProfitFactor,
			),
		),
		check(
			"replay_quality",
			"Actual-fill replay quality",
			false,
			evidence.ReplayMatchedClosed >= minimumReplayCycles &&
				inconclusiveRate <= 0.25,
			fmt.Sprintf(
				"%d matched; %.0f%% inconclusive",
				evidence.ReplayMatchedClosed,
				inconclusiveRate*100,
			),
		),
		check(
			"actual_pnl",
			"Observed live net PnL",
			false,
			evidence.ActualNetPnL > 0,
			fmt.Sprintf("$%.2f after fees", evidence.ActualNetPnL),
		),
		check(
			"challenger_delta",
			"Challenger improves baseline",
			false,
			evidence.ChallengerNetPnLDelta > 0,
			fmt.Sprintf("$%.2f replay delta", evidence.ChallengerNetPnLDelta),
		),
		check(
			"partial_fill",
			"Partial-fill lifecycle observed",
			false,
			evidence.PartialFillTransitions > 0,
			fmt.Sprintf("%d transitions", evidence.PartialFillTransitions),
		),
		check(
			"stop_replacement",
			"Protective-stop replacement observed",
			false,
			evidence.StopCancelReplacements > 0,
			fmt.Sprintf("%d replacements", evidence.StopCancelReplacements),
		),
		check(
			"restart_recovery",
			"Restart recovery observed",
			false,
			evidence.RestartRecoveryEvents > 0,
			fmt.Sprintf("%d recovery events", evidence.RestartRecoveryEvents),
		),
		check(
			"disconnect_recovery",
			"Broker disconnect recovery observed",
			false,
			evidence.DisconnectRecoveryEvents > 0,
			fmt.Sprintf("%d recoveries", evidence.DisconnectRecoveryEvents),
		),
	}
	eligible := true
	for _, item := range checks {
		if item.Required {
			eligible = eligible && item.Passed
		}
	}
	decision := "BLOCKED"
	if eligible {
		decision = "ELIGIBLE_FOR_MANUAL_PROMOTION"
	}
	return StrategyCertification{
		Eligible: eligible,
		Decision: decision,
		Checks:   checks,
	}
}

func check(
	code, label string,
	required, passed bool,
	detail string,
) CertificationCheck {
	return CertificationCheck{
		Code: code, Label: label, Required: required,
		Passed: passed, Detail: detail,
	}
}
