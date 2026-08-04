package massive_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/massive"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

func TestClient_AggregatesFollowsPagination(t *testing.T) {
	t.Parallel()

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("apiKey"); got != "test-key" {
			t.Errorf("apiKey = %q, want test-key", got)
		}

		switch requests {
		case 1:
			if got := r.URL.Path; got != "/v2/aggs/ticker/AAPL/range/1/day/2026-01-02/2026-01-03" {
				t.Errorf("path = %q", got)
			}
			if got := r.URL.Query().Get("adjusted"); got != "true" {
				t.Errorf("adjusted = %q, want true", got)
			}
			if got := r.URL.Query().Get("sort"); got != "asc" {
				t.Errorf("sort = %q, want asc", got)
			}
			writeJSON(t, w, map[string]any{
				"status": "OK",
				"results": []map[string]any{
					{"o": 10.0, "h": 12.0, "l": 9.5, "c": 11.0, "v": 1000, "vw": 10.75, "t": 1767330000000},
				},
				"next_url": serverURL(r) + "/page-2?cursor=abc",
			})
		case 2:
			if got := r.URL.Query().Get("cursor"); got != "abc" {
				t.Errorf("cursor = %q, want abc", got)
			}
			writeJSON(t, w, map[string]any{
				"status": "OK",
				"results": []map[string]any{
					{"o": 11.0, "h": 13.0, "l": 10.0, "c": 12.5, "v": 2000, "t": 1767416400000},
				},
			})
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client, err := massive.NewClient("test-key", massive.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	bars, err := client.Aggregates(context.Background(), model.AggregateQuery{
		Ticker:     "AAPL",
		Multiplier: 1,
		Timespan:   model.TimespanDay,
		From:       "2026-01-02",
		To:         "2026-01-03",
	})
	if err != nil {
		t.Fatalf("Aggregates() error = %v", err)
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if len(bars) != 2 {
		t.Fatalf("len(bars) = %d, want 2", len(bars))
	}
	if bars[0].Ticker != "AAPL" || bars[0].Close != 11 || bars[0].VWAP == nil || *bars[0].VWAP != 10.75 {
		t.Errorf("first bar = %#v", bars[0])
	}
	if bars[1].VWAP != nil {
		t.Errorf("second bar VWAP = %v, want nil", bars[1].VWAP)
	}
}

func TestClient_DailySummaryReturnsTheWholeMarket(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/v2/aggs/grouped/locale/us/market/stocks/2026-07-29" {
			t.Errorf("path = %q", got)
		}
		if got := r.URL.Query().Get("adjusted"); got != "false" {
			t.Errorf("adjusted = %q, want false", got)
		}
		if got := r.URL.Query().Get("include_otc"); got != "false" {
			t.Errorf("include_otc = %q, want false", got)
		}
		writeJSON(t, w, map[string]any{
			"status": "OK",
			"results": []map[string]any{
				{
					"T": "AAPL", "o": 210.0, "h": 215.0, "l": 208.0,
					"c": 214.0, "v": 1_000_000, "vw": 212.5,
					"t": 1785297600000, "n": 100,
				},
				{
					"T": "RUN", "o": 5.0, "h": 8.0, "l": 4.5,
					"c": 7.0, "v": 10_000_000, "t": 1785297600000,
				},
				{
					"T": "MSpI", "o": 25.0, "h": 25.0, "l": 25.0,
					"c": 25.0, "v": 100, "t": 1785297600000,
				},
			},
		})
	}))
	defer server.Close()

	client, err := massive.NewClient("test-key", massive.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	bars, err := client.DailySummary(
		context.Background(),
		time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 {
		t.Fatalf("len(bars) = %d, want 2", len(bars))
	}
	if bars[0].Ticker != "AAPL" || bars[0].Open != 210 || bars[0].Transactions == nil {
		t.Errorf("first bar = %#v", bars[0])
	}
	if bars[1].Ticker != "RUN" || bars[1].VWAP != nil {
		t.Errorf("second bar = %#v", bars[1])
	}
}

func TestClient_RejectsCrossOriginPagination(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"status":   "OK",
			"next_url": "https://attacker.example/steal",
		})
	}))
	defer server.Close()

	client, err := massive.NewClient("test-key", massive.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Aggregates(context.Background(), model.AggregateQuery{
		Ticker:     "AAPL",
		Multiplier: 1,
		Timespan:   model.TimespanDay,
		From:       "2026-01-02",
		To:         "2026-01-03",
	})
	if err == nil {
		t.Fatal("Aggregates() error = nil, want cross-origin error")
	}
}

func TestClient_RejectsOversizedAggregateResult(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"status":  "OK",
			"results": make([]struct{}, 100_001),
		})
	}))
	defer server.Close()

	client, err := massive.NewClient("test-key", massive.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Aggregates(context.Background(), model.AggregateQuery{
		Ticker:     "AAPL",
		Multiplier: 1,
		Timespan:   model.TimespanMinute,
		From:       "2026-01-01",
		To:         "2026-03-31",
	})
	if err == nil {
		t.Fatal("Aggregates() error = nil, want result-size error")
	}
}

func TestClient_ReportsAPIErrorWithoutLeakingKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(
			w,
			`{"status":"ERROR","error":"key super-secret-key is not authorized"}`,
			http.StatusUnauthorized,
		)
	}))
	defer server.Close()

	client, err := massive.NewClient("super-secret-key",
		massive.WithBaseURL(server.URL),
		massive.WithHTTPClient(&http.Client{Timeout: time.Second}),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Aggregates(context.Background(), model.AggregateQuery{
		Ticker:     "AAPL",
		Multiplier: 1,
		Timespan:   model.TimespanDay,
		From:       "2026-01-02",
		To:         "2026-01-03",
	})
	if err == nil {
		t.Fatal("Aggregates() error = nil, want API error")
	}
	if contains(err.Error(), "super-secret-key") {
		t.Fatalf("error leaked API key: %v", err)
	}
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client, err := massive.NewClient("test-key", massive.WithBaseURL(source.URL))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Ticker(context.Background(), "AAPL")
	if err == nil {
		t.Fatal("Ticker() error = nil, want redirect error")
	}
	if redirected {
		t.Fatal("client followed redirect")
	}
}

func TestClient_RedactsKeyFromTransportErrors(t *testing.T) {
	t.Parallel()

	const apiKey = "transport-secret-key"
	httpClient := &http.Client{
		Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial failed for %s", request.URL.String())
		}),
	}
	client, err := massive.NewClient(
		apiKey,
		massive.WithBaseURL("https://api.massive.test"),
		massive.WithHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Ticker(context.Background(), "AAPL")
	if err == nil {
		t.Fatal("Ticker() error = nil, want transport error")
	}
	if contains(err.Error(), apiKey) {
		t.Fatalf("error leaked API key: %v", err)
	}
}

func TestClient_TickerAndFloat(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/reference/tickers/AAPL":
			writeJSON(t, w, map[string]any{
				"status": "OK",
				"results": map[string]any{
					"ticker":           "AAPL",
					"name":             "Apple Inc.",
					"primary_exchange": "XNAS",
					"sic_description":  "Electronic Computers",
					"type":             "CS",
					"market_cap":       3_000_000_000_000.0,
				},
			})
		case "/stocks/vX/float":
			if got := r.URL.Query().Get("ticker"); got != "AAPL" {
				t.Errorf("ticker = %q, want AAPL", got)
			}
			writeJSON(t, w, map[string]any{
				"status": "OK",
				"results": []map[string]any{
					{"ticker": "AAPL", "free_float": 15_000_000_000},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := massive.NewClient("test-key", massive.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	stock, err := client.Ticker(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("Ticker() error = %v", err)
	}
	floatShares, err := client.Float(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("Float() error = %v", err)
	}

	if stock.CompanyName != "Apple Inc." || stock.Exchange != "XNAS" || stock.Sector != "Electronic Computers" {
		t.Errorf("stock = %#v", stock)
	}
	if stock.SecurityType != "CS" {
		t.Errorf("security type = %q, want CS", stock.SecurityType)
	}
	if floatShares == nil || *floatShares != 15_000_000_000 {
		t.Errorf("floatShares = %v", floatShares)
	}
}

func TestClient_CommonStocksFollowsPointInTimePagination(t *testing.T) {
	t.Parallel()

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if got := r.URL.Query().Get("date"); got != "2026-07-22" {
				t.Errorf("date = %q", got)
			}
			if got := r.URL.Query().Get("type"); got != "CS" {
				t.Errorf("type = %q", got)
			}
		}
		switch requests {
		case 1:
			writeJSON(t, w, map[string]any{
				"results": []map[string]any{{
					"ticker": "AAPL", "name": "Apple", "type": "CS",
					"primary_exchange": "XNAS",
				}},
				"next_url": serverURL(r) + "/v3/reference/tickers?cursor=next",
			})
		case 2:
			writeJSON(t, w, map[string]any{
				"results": []map[string]any{
					{"ticker": "RUN", "name": "Runner", "type": "CS"},
					{"ticker": "MSpI", "name": "Preferred", "type": "CS"},
				},
			})
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()
	client, err := massive.NewClient("test-key", massive.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	stocks, err := client.CommonStocks(
		context.Background(),
		time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(stocks) != 2 || stocks[0].Ticker != "AAPL" || stocks[1].Ticker != "RUN" {
		t.Errorf("stocks = %+v", stocks)
	}
}

func TestClient_NewsAndSplitsMapIntelligenceEvents(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/v2/reference/news":
			_, _ = writer.Write([]byte(`{"results":[{
				"id":"news-1","title":"AI FDA approval","description":"milestone",
				"published_utc":"2026-01-02T12:00:00Z",
				"article_url":"https://example.com/news",
				"insights":[{"ticker":"TEST","sentiment":"positive"}]}]}`))
		case "/stocks/v1/splits":
			_, _ = writer.Write([]byte(`{"results":[{
				"id":"split-1","execution_date":"2026-01-03",
				"split_from":10,"split_to":1}]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := massive.NewClient(
		"test-key",
		massive.WithBaseURL(server.URL),
		massive.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	news, err := client.News(context.Background(), "TEST", asOf)
	if err != nil {
		t.Fatal(err)
	}
	splits, err := client.Splits(context.Background(), "TEST", asOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(news) != 1 || news[0].Sentiment != "positive" {
		t.Fatalf("news = %+v", news)
	}
	if len(splits) != 1 || !splits[0].Reverse {
		t.Fatalf("splits = %+v", splits)
	}
}

func TestClient_LatestNewsUsesBoundedPointInTimeWindow(t *testing.T) {
	t.Parallel()

	var query string
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		query = request.URL.RawQuery
		_, _ = writer.Write([]byte(`{"results":[{
			"id":"news-2","title":"Preliminary results","description":"growth",
			"published_utc":"2026-07-30T11:00:00Z",
			"article_url":"https://example.com/latest",
			"insights":[{"ticker":"NUWE","sentiment":"positive"}]}],
			"next_url":"https://example.com/must-not-follow"}`))
	}))
	defer server.Close()
	client, err := massive.NewClient(
		"test-key",
		massive.WithBaseURL(server.URL),
		massive.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	items, err := client.LatestNews(
		context.Background(),
		"NUWE",
		from,
		to,
		25,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ExternalID != "news-2" {
		t.Fatalf("latest news = %+v", items)
	}
	for name, want := range map[string]string{
		"ticker":            "NUWE",
		"published_utc.gte": from.Format(time.RFC3339),
		"published_utc.lte": to.Format(time.RFC3339),
		"limit":             "25",
		"sort":              "published_utc",
		"order":             "desc",
	} {
		values, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		if got := values.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestClient_LatestMarketNewsDiscoversAllTickersWithoutTickerFilter(
	t *testing.T,
) {
	t.Parallel()

	var query string
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		query = request.URL.RawQuery
		_, _ = writer.Write([]byte(`{"results":[{
			"id":"news-market","title":"Material contract awarded",
			"description":"largest award","published_utc":"2026-07-31T19:50:00Z",
			"article_url":"https://example.com/news",
			"tickers":["AAA","BBB"],
			"insights":[{"ticker":"AAA","sentiment":"positive"}]}]}`))
	}))
	defer server.Close()
	client, err := massive.NewClient(
		"test-key",
		massive.WithBaseURL(server.URL),
		massive.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 31, 18, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)

	items, err := client.LatestMarketNews(
		context.Background(),
		from,
		to,
		1000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 ||
		items[0].Ticker != "AAA" ||
		items[0].News.Sentiment != "positive" ||
		items[1].Ticker != "BBB" {
		t.Fatalf("market news = %+v", items)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if values.Has("ticker") {
		t.Fatalf("all-market query unexpectedly contains ticker: %s", query)
	}
	if values.Get("published_utc.gte") != from.Format(time.RFC3339) ||
		values.Get("published_utc.lte") != to.Format(time.RFC3339) {
		t.Fatalf("query = %s", query)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encoding response: %v", err)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
