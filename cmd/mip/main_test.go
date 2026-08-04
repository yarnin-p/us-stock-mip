package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func TestParseFeatureArgs_UsesDefaults(t *testing.T) {
	t.Parallel()

	got, err := parseFeatureArgs([]string{
		"--tickers", "AAPL,MSFT",
		"--date", "2026-01-05",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFeatureArgs() error = %v", err)
	}

	if got.tickers != "AAPL,MSFT" {
		t.Errorf("tickers = %q", got.tickers)
	}
	if !got.asOf.Equal(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("as-of = %s", got.asOf)
	}
	if got.relativeVolumePeriod != 20 || got.emaPeriod != 9 || got.breakoutPeriod != 20 {
		t.Errorf("periods = %d/%d/%d, want 20/9/20",
			got.relativeVolumePeriod,
			got.emaPeriod,
			got.breakoutPeriod,
		)
	}
}

func TestParseFeatureArgs_RejectsInvalidDate(t *testing.T) {
	t.Parallel()

	_, err := parseFeatureArgs([]string{
		"--tickers", "AAPL",
		"--date", "01/05/2026",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "parsing date") {
		t.Fatalf("parseFeatureArgs() error = %v, want date error", err)
	}
}

func TestRun_RejectsUnknownCommandWithUsage(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	err := run([]string{"unknown"}, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want command error")
	}
	if !strings.Contains(stderr.String(), "mip features") {
		t.Errorf("usage = %q, want features command", stderr.String())
	}
}

func TestParseOpeningListArgsUsesPRDV2TopFiveDefaults(t *testing.T) {
	t.Parallel()

	got, err := parseOpeningListArgs(
		[]string{"--date", "2026-07-22", "--sync=false"},
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.criteria.Limit != 5 || got.lookback != 20 {
		t.Errorf("limit/lookback = %d/%d, want 5/20", got.criteria.Limit, got.lookback)
	}
	if got.criteria.MinGap != 0.05 || got.criteria.MinAverageDollarVolume != 1_000_000 {
		t.Errorf("criteria = %+v", got.criteria)
	}
	if got.syncMarket {
		t.Error("syncMarket = true, want false")
	}
	if got.criteria.Limit != 5 || got.minMarketBars != 5_000 ||
		got.minUniverseMembers != 1_000 || got.researchLimit != 25 {
		t.Errorf("PRD v2 opening defaults = %+v", got)
	}
}

func TestWriteOpeningReportCSV(t *testing.T) {
	t.Parallel()

	date := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	var output bytes.Buffer
	err := writeOpeningReport(&output, "csv", opening.Report{Lists: []opening.DayList{{
		TradingDate: date,
		Entries: []opening.Result{{
			Ticker: "RUN", Rank: 1, Score: 88.25, ScoreCoverage: 0.8,
			OpenPrice: 5.5, Gap: 0.1, RelativeVolume: 2,
			AverageDollarVolume: 3_000_000,
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "2026-07-22,1,RUN,88.25,80%") {
		t.Errorf("CSV = %q", output.String())
	}
}

func TestNextTradingDayOpeningSkipsWeekendAndHoliday(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	friday := time.Date(2026, 7, 24, 16, 0, 0, 0, location)
	got := nextTradingDayOpening(friday, location)
	if got.Weekday() != time.Monday || got.Hour() != 9 || got.Minute() != 30 {
		t.Errorf("next opening = %s", got)
	}

	thursday := time.Date(2026, 7, 2, 16, 0, 0, 0, location)
	got = nextTradingDayOpening(thursday, location)
	if got.Format(time.DateOnly) != "2026-07-06" {
		t.Errorf("opening after observed Independence Day = %s", got)
	}
}

func TestWebullTokenMaintenanceRefreshesRunningClient(t *testing.T) {
	t.Parallel()
	refreshed := make(chan struct{}, 1)
	expires := time.Now().Add(time.Hour).UnixMilli()
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/openapi/auth/token/refresh":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(
				writer,
				`{"token":"new-token","expires":%d,"status":"NORMAL"}`,
				expires,
			)
			refreshed <- struct{}{}
		case "/openapi/market-data/stock/snapshot":
			if request.Header.Get("x-access-token") != "new-token" {
				t.Errorf("snapshot token = %q", request.Header.Get("x-access-token"))
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`[{"symbol":"AAPL","price":"10","volume":"1",` +
					`"change_ratio":"0","pre_close":"10","last_trade_time":1710849600000}]`,
			))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	client, err := webull.NewClient(
		"key", "secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithAccessToken("old-token"),
		webull.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	err = startWebullTokenMaintenance(
		ctx,
		config.Config{
			WebullAppKey: "key", WebullSecret: "secret",
			WebullBaseURL: server.URL, WebullAlgorithm: "HMAC-SHA256",
			WebullAccessToken: "old-token", WebullTokenFile: tokenFile,
			WebullTokenExpires: time.Now().Add(5*time.Minute + 50*time.Millisecond).UnixMilli(),
			HTTPTimeout:        time.Second,
		},
		client,
		stop,
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-refreshed:
	case <-time.After(2 * time.Second):
		t.Fatal("token was not refreshed")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		stored, readErr := webull.ReadTokenFile(tokenFile)
		if readErr == nil && stored.Token == "new-token" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refreshed token was not installed: %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := client.Snapshots(context.Background(), []string{"AAPL"}); err != nil {
		t.Fatal(err)
	}
}
