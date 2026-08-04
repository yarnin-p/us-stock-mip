package ranking_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/ranking"
)

func TestBuildLabelsUsesIntradayAndFiveTradingDayTargets(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	bars := []ranking.PriceBar{
		{Date: start, Open: 10, High: 15, Close: 12, Volume: 100},
		{Date: start.AddDate(0, 0, 1), Open: 12, High: 18, Close: 12, Volume: 250},
		{Date: start.AddDate(0, 0, 2), Open: 12, High: 14, Close: 12, Volume: 100},
		{Date: start.AddDate(0, 0, 3), Open: 12, High: 14, Close: 12, Volume: 100},
		{Date: start.AddDate(0, 0, 4), Open: 12, High: 14, Close: 12, Volume: 100},
		{Date: start.AddDate(0, 0, 5), Open: 24, High: 25, Close: 24, Volume: 100},
		{Date: start.AddDate(0, 0, 6), Open: 24, High: 25, Close: 24, Volume: 100},
	}

	labels, err := ranking.BuildLabels(bars)
	if err != nil {
		t.Fatal(err)
	}
	if !labels[0].IntradayRunner || !labels[0].SwingRunner || !labels[0].Runner {
		t.Fatalf("first label = %+v", labels[0])
	}
	if labels[1].IntradayRunner || !labels[1].SwingRunner {
		t.Fatalf("second label = %+v", labels[1])
	}
	if !labels[0].Exhaustion {
		t.Fatalf("first label should capture next-session volume climax: %+v", labels[0])
	}
}

func TestBuildSpikeLabelsUsesNextHighAgainstPointInTimeClose(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	bars := []ranking.PriceBar{
		{Date: start, Open: 10, High: 10.5, Close: 10, Volume: 100},
		{
			Date: start.AddDate(0, 0, 1),
			Open: 12, High: 15.1, Close: 13, Volume: 1_000,
		},
		{
			Date: start.AddDate(0, 0, 2),
			Open: 13, High: 14, Close: 13.5, Volume: 500,
		},
	}

	labels, err := ranking.BuildSpikeLabels(bars, .50)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 2 {
		t.Fatalf("labels = %d, want 2 point-in-time outcomes", len(labels))
	}
	if !labels[0].Runner {
		t.Fatalf("first label = %+v, want next-session 50%% spike", labels[0])
	}
	if labels[1].Runner {
		t.Fatalf("second label = %+v, want no next-session 50%% spike", labels[1])
	}
}

func TestBuildSpikeLabelsRejectsCorporateActionDiscontinuity(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	bars := []ranking.PriceBar{
		{Date: start, Open: .04, High: .05, Close: .042, Volume: 100},
		{
			Date: start.AddDate(0, 0, 3),
			Open: 5.10, High: 6.20, Close: 5.50, Volume: 100,
		},
	}

	labels, err := ranking.BuildSpikeLabels(bars, .50)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 0 {
		t.Fatalf("labels = %+v, want split discontinuity quarantined", labels)
	}
}
