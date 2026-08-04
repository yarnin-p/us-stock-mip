package opening_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/opening"
)

func TestSelectorSelectsAndRanksOpeningCandidates(t *testing.T) {
	t.Parallel()

	selector, err := opening.NewSelector(opening.Criteria{
		MinPrice:               1,
		MaxPrice:               20,
		MinGap:                 0.05,
		MinAverageDollarVolume: 1_000_000,
		MinimumHistory:         5,
		Limit:                  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	got, err := selector.Select(tradingDate, []opening.Input{
		{
			Ticker: "RUN", CurrentOpen: 8, PriorClose: 5, PriorVolume: 4_000_000,
			AverageVolume: 1_000_000, AverageDollarVolume: 5_000_000,
			PriorReturn: 0.20, Breakout: 0.10, HistoryCount: 20,
		},
		{
			Ticker: "MOVE", CurrentOpen: 6, PriorClose: 5, PriorVolume: 2_000_000,
			AverageVolume: 1_000_000, AverageDollarVolume: 3_000_000,
			PriorReturn: 0.08, Breakout: 0.02, HistoryCount: 20,
		},
		{
			Ticker: "THIRD", CurrentOpen: 5.5, PriorClose: 5, PriorVolume: 1_500_000,
			AverageVolume: 1_000_000, AverageDollarVolume: 2_000_000,
			PriorReturn: 0.03, Breakout: 0, HistoryCount: 20,
		},
		{
			Ticker: "LOWGAP", CurrentOpen: 5.1, PriorClose: 5, PriorVolume: 5_000_000,
			AverageVolume: 1_000_000, AverageDollarVolume: 5_000_000,
			PriorReturn: 0.40, Breakout: 0.20, HistoryCount: 20,
		},
		{
			Ticker: "ILLIQUID", CurrentOpen: 10, PriorClose: 8, PriorVolume: 10_000,
			AverageVolume: 10_000, AverageDollarVolume: 100_000,
			PriorReturn: 0.20, Breakout: 0.10, HistoryCount: 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(got))
	}
	if got[0].Ticker != "RUN" || got[0].Rank != 1 {
		t.Errorf("first result = %#v, want RUN rank 1", got[0])
	}
	if got[1].Ticker != "MOVE" || got[1].Rank != 2 {
		t.Errorf("second result = %#v, want MOVE rank 2", got[1])
	}
	for _, result := range got {
		if !result.TradingDate.Equal(tradingDate) {
			t.Errorf("%s trading date = %s", result.Ticker, result.TradingDate)
		}
		if result.Gap < 0.05 {
			t.Errorf("%s gap = %f", result.Ticker, result.Gap)
		}
	}
}

func TestSelectorRejectsInvalidOrInsufficientPointInTimeInputs(t *testing.T) {
	t.Parallel()

	selector, err := opening.NewSelector(opening.Criteria{
		MinPrice: 1, MaxPrice: 20, MinGap: 0,
		MinAverageDollarVolume: 0, MinimumHistory: 5, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = selector.Select(time.Now(), []opening.Input{{
		Ticker: "AAPL", CurrentOpen: 10, PriorClose: 9,
		PriorVolume: 1, AverageVolume: 1, AverageDollarVolume: 9,
		HistoryCount: 4,
	}})
	if err != nil {
		t.Fatalf("Select() insufficient history error = %v, want filtered result", err)
	}

	_, err = selector.Select(time.Now(), []opening.Input{{
		Ticker: "BAD SYMBOL", CurrentOpen: 10, PriorClose: 9,
		PriorVolume: 1, AverageVolume: 1, AverageDollarVolume: 9,
		HistoryCount: 5,
	}})
	if err == nil {
		t.Fatal("Select() invalid ticker error = nil")
	}
}

func TestNewSelectorRejectsInvalidCriteria(t *testing.T) {
	t.Parallel()

	_, err := opening.NewSelector(opening.Criteria{
		MinPrice: 20, MaxPrice: 1, Limit: 20, MinimumHistory: 5,
	})
	if err == nil {
		t.Fatal("NewSelector() error = nil")
	}
}

func TestSelectorUsesPRDV2WeightsAndReportsCoverage(t *testing.T) {
	t.Parallel()

	selector, err := opening.NewSelector(opening.Criteria{
		MinPrice: 1, MaxPrice: 20, MinGap: 0,
		MinAverageDollarVolume: 0, MinimumHistory: 2, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	floatShares := int64(4_000_000)
	marketCap := 40_000_000.0
	catalyst := 1.0
	sector := 1.0
	dilutionRisk := 0.0
	got, err := selector.Select(time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC), []opening.Input{{
		Ticker: "FULL", CurrentOpen: 10, PriorClose: 5,
		PriorVolume: 5_000_000, AverageVolume: 1_000_000,
		AverageDollarVolume: 5_000_000, PriorReturn: 0.5,
		Breakout: 0.1, HistoryCount: 20, FloatShares: &floatShares,
		MarketCap: &marketCap, CatalystScore: &catalyst,
		SectorScore: &sector, DilutionRisk: &dilutionRisk,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(got))
	}
	if got[0].Score != 100 || got[0].ScoreCoverage != 1 {
		t.Errorf("score/coverage = %.2f/%.2f, want 100/1", got[0].Score, got[0].ScoreCoverage)
	}
}

func TestSelectorDoesNotInferSectorStrengthFromCandidateSubset(t *testing.T) {
	t.Parallel()

	selector, err := opening.NewSelector(opening.Criteria{
		MinPrice: 1, MaxPrice: 20, MinGap: 0,
		MinAverageDollarVolume: 0, MinimumHistory: 2, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := selector.Select(time.Now(), []opening.Input{{
		Ticker: "ONLY", CurrentOpen: 10, PriorClose: 9,
		PriorVolume: 2_000_000, AverageVolume: 1_000_000,
		AverageDollarVolume: 9_000_000, PriorReturn: 0.2,
		Breakout: 0.1, HistoryCount: 20, Sector: "Technology",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(got))
	}
	if got[0].SectorScore != nil {
		t.Errorf("sector score = %v, want unavailable", *got[0].SectorScore)
	}
}
