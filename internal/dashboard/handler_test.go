package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

type fakeRepository struct {
	candidates []Candidate
	watchlist  []WatchlistItem
	positions  []Position
	trades     []Trade
	added      WatchlistInput
	removed    string
	created    TradeInput
	event      TradeEventInput
	score      []ScorePoint
	alerts     []Alert
	health     SystemHealth
	learning   LearningReport
	err        error
}

type fakeSpikeWatcher struct {
	watch SpikeWatch
	err   error
}

type fakeNewsCatalystSource struct {
	items    []NewsCatalyst
	err      error
	asOf     time.Time
	lookback time.Duration
	limit    int
}

func (source *fakeNewsCatalystSource) NewsCatalysts(
	_ context.Context,
	asOf time.Time,
	lookback time.Duration,
	limit int,
) ([]NewsCatalyst, error) {
	source.asOf = asOf
	source.lookback = lookback
	source.limit = limit
	return source.items, source.err
}

func (watcher *fakeSpikeWatcher) SpikeWatch(context.Context) (SpikeWatch, error) {
	return watcher.watch, watcher.err
}

func (repo *fakeRepository) Candidates(context.Context) ([]Candidate, error) {
	return repo.candidates, repo.err
}

func (repo *fakeRepository) Scan(context.Context) ([]ScanSignal, error) {
	return nil, repo.err
}

func (repo *fakeRepository) Watchlist(context.Context) ([]WatchlistItem, error) {
	return repo.watchlist, repo.err
}

func (repo *fakeRepository) UpsertWatchlist(
	_ context.Context, input WatchlistInput,
) (WatchlistItem, error) {
	repo.added = input
	return WatchlistItem{Ticker: input.Ticker, Thesis: input.Thesis}, repo.err
}

func (repo *fakeRepository) DeleteWatchlist(
	_ context.Context, ticker string,
) error {
	repo.removed = ticker
	return repo.err
}

func (repo *fakeRepository) Positions(context.Context) ([]Position, error) {
	return repo.positions, repo.err
}

func (repo *fakeRepository) Trades(context.Context) ([]Trade, error) {
	return repo.trades, repo.err
}

func (repo *fakeRepository) CreateTrade(
	_ context.Context, input TradeInput,
) (Trade, error) {
	repo.created = input
	return Trade{
		ID: 7, Ticker: input.Ticker, Side: input.Side,
		Quantity: input.Quantity, EntryPrice: input.EntryPrice,
		EnteredAt: input.EnteredAt, Strategy: input.Strategy,
	}, repo.err
}

func (repo *fakeRepository) ApplyTradeEvent(
	_ context.Context, _ int64, input TradeEventInput,
) (Trade, error) {
	repo.event = input
	return Trade{ID: 7}, repo.err
}

func (repo *fakeRepository) ScoreHistory(
	_ context.Context, _ string,
) ([]ScorePoint, error) {
	return repo.score, repo.err
}

func (repo *fakeRepository) Alerts(context.Context) ([]Alert, error) {
	return repo.alerts, repo.err
}

func (repo *fakeRepository) AcknowledgeAlert(context.Context, int64) error {
	return repo.err
}

func (repo *fakeRepository) SystemHealth(context.Context) (SystemHealth, error) {
	return repo.health, repo.err
}

func (repo *fakeRepository) BrokerPositions(
	context.Context,
) ([]BrokerPosition, error) {
	return nil, repo.err
}

func (repo *fakeRepository) BrokerOrders(
	context.Context,
) ([]BrokerOrder, error) {
	return nil, repo.err
}

func (repo *fakeRepository) LearningReport(
	context.Context,
) (LearningReport, error) {
	return repo.learning, repo.err
}

func TestHandlerReturnsCandidatesAndFreshness(t *testing.T) {
	observedAt := time.Date(2026, 7, 28, 19, 57, 3, 0, time.UTC)
	repo := &fakeRepository{candidates: []Candidate{{
		Ticker: "OPK", Rank: 1, Score: 87.5, Selected: true,
		Quote: &Quote{
			BidPrice: 1.67, BidSize: 15017, AskPrice: 1.68,
			AskSize: 34481, ObservedAt: observedAt,
		},
	}}}
	request := httptest.NewRequest(http.MethodGet, "/candidates", nil)
	recorder := httptest.NewRecorder()

	NewHandler(repo, Options{AllowedOrigin: "http://localhost:3001"}).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got !=
		"http://localhost:3001" {
		t.Fatalf("allow origin = %q", got)
	}
	var response struct {
		Data []Candidate `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].Quote == nil ||
		response.Data[0].Quote.AskPrice != 1.68 {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandlerSelectsConfiguredLoopbackCORSOrigin(t *testing.T) {
	handler := NewHandler(&fakeRepository{}, Options{
		AllowedOrigin: "http://localhost:3001,http://127.0.0.1:3001",
	})
	for _, origin := range []string{
		"http://localhost:3001",
		"http://127.0.0.1:3001",
	} {
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.Header.Set("Origin", origin)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("origin %q allowed as %q", origin, got)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", "https://example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("untrusted origin allowed as %q", got)
	}
}

func TestHandlerReturnsLearningReport(t *testing.T) {
	repo := &fakeRepository{learning: LearningReport{
		Certification: StrategyCertification{Decision: "BLOCKED"},
		Coverage: LearningCoverage{
			MarketQuotes:       123,
			MarketQuoteTickers: 7,
			MarketTicks:        456,
			MarketTickTickers:  6,
			StrategyCycles:     4,
		},
	}}
	request := httptest.NewRequest(http.MethodGet, "/learning-report", nil)
	response := httptest.NewRecorder()
	NewHandler(repo, Options{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data LearningReport `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Certification.Decision != "BLOCKED" {
		t.Fatalf("learning report = %#v", body.Data)
	}
	if body.Data.Coverage.MarketQuotes != 123 ||
		body.Data.Coverage.MarketQuoteTickers != 7 ||
		body.Data.Coverage.MarketTicks != 456 ||
		body.Data.Coverage.MarketTickTickers != 6 ||
		body.Data.Coverage.StrategyCycles != 4 {
		t.Fatalf("learning coverage = %#v", body.Data.Coverage)
	}
}

func TestHandlerReturnsPointInTimeSpikeWatch(t *testing.T) {
	watcher := &fakeSpikeWatcher{watch: SpikeWatch{
		ModelName: "spike-discovery-v1",
		Evidence: SpikePredictionEvidence{
			Kind: "FORWARD", Status: "FORWARD_READY",
			PointInTimeCausal: true,
		},
		Candidates: []SpikeCandidate{{
			Ticker: "SPRC", Rank: 1, Probability: .42,
			Confirmation: "LIVE_CONFIRMED",
		}},
	}}
	request := httptest.NewRequest(http.MethodGet, "/spike-watch", nil)
	response := httptest.NewRecorder()

	NewHandler(&fakeRepository{}, Options{SpikeWatcher: watcher}).
		ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data SpikeWatch `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Candidates) != 1 ||
		body.Data.Candidates[0].Ticker != "SPRC" ||
		!body.Data.Evidence.PointInTimeCausal {
		t.Fatalf("spike watch = %#v", body.Data)
	}
}

func TestHandlerReturnsNewsCatalystsWithBoundedWindow(t *testing.T) {
	publishedAt := time.Date(2026, 7, 31, 5, 16, 0, 0, time.UTC)
	source := &fakeNewsCatalystSource{items: []NewsCatalyst{{
		Ticker:      "VEON",
		PublishedAt: publishedAt,
		AvailableAt: publishedAt.Add(12 * time.Second),
		Title:       "VEON Raises FY2026 Sales Guidance Above Estimates",
		Classification: intelligence.NewsClassification{
			Kind: "GUIDANCE", Strength: .92, Tradeable: true,
		},
	}}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/news-catalysts?hours=8&limit=25",
		nil,
	)
	response := httptest.NewRecorder()

	NewHandler(&fakeRepository{}, Options{NewsCatalysts: source}).
		ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data []NewsCatalyst `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 ||
		body.Data[0].Ticker != "VEON" ||
		body.Data[0].Classification.Kind != "GUIDANCE" {
		t.Fatalf("news catalysts = %#v", body.Data)
	}
	if source.asOf.IsZero() || source.lookback != 8*time.Hour ||
		source.limit != 25 {
		t.Fatalf(
			"source args asOf=%s lookback=%s limit=%d",
			source.asOf, source.lookback, source.limit,
		)
	}
}

func TestHandlerNormalizesWatchlistTicker(t *testing.T) {
	repo := &fakeRepository{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/watchlist",
		bytes.NewBufferString(`{"ticker":" opk ","thesis":"after-hours continuation"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	NewHandler(repo, Options{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.added.Ticker != "OPK" {
		t.Fatalf("ticker = %q", repo.added.Ticker)
	}
}

func TestHandlerRejectsInvalidTrade(t *testing.T) {
	repo := &fakeRepository{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/trades",
		bytes.NewBufferString(`{"ticker":"OPK","quantity":0,"entry_price":1.67}`),
	)
	recorder := httptest.NewRecorder()

	NewHandler(repo, Options{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.created.Ticker != "" {
		t.Fatal("repository was called for invalid trade")
	}
}

func TestHandlerMapsRepositoryFailure(t *testing.T) {
	repo := &fakeRepository{err: errors.New("database unavailable")}
	request := httptest.NewRequest(http.MethodGet, "/positions", nil)
	recorder := httptest.NewRecorder()

	NewHandler(repo, Options{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("database unavailable")) {
		t.Fatal("internal repository error leaked to client")
	}
}

func TestHandlerDeletesWatchlistSymbol(t *testing.T) {
	repo := &fakeRepository{}
	request := httptest.NewRequest(http.MethodDelete, "/watchlist/opk", nil)
	recorder := httptest.NewRecorder()

	NewHandler(repo, Options{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	if repo.removed != "OPK" {
		t.Fatalf("removed = %q", repo.removed)
	}
}

func TestHandlerReturnsSystemHealthWithAPIAndRedis(t *testing.T) {
	repo := &fakeRepository{health: SystemHealth{
		Components: []ComponentHealth{{Name: "Database", Status: "CONNECTED"}},
	}}
	request := httptest.NewRequest(http.MethodGet, "/system-health", nil)
	recorder := httptest.NewRecorder()

	NewHandler(repo, Options{
		RedisPing: func(context.Context) error { return nil },
		RuntimeHealth: func() []ComponentHealth {
			return []ComponentHealth{{
				Name: "Alpaca News", Status: "CONNECTED",
			}}
		},
	}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data SystemHealth `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Components) != 4 {
		t.Fatalf("components = %#v", response.Data.Components)
	}
	if response.Data.Components[2].Name != "Redis" ||
		response.Data.Components[2].Status != "CONNECTED" {
		t.Fatalf("redis component = %#v", response.Data.Components[2])
	}
	if response.Data.Components[3].Name != "Alpaca News" ||
		response.Data.Components[3].Status != "CONNECTED" {
		t.Fatalf("runtime component = %#v", response.Data.Components[3])
	}
}

func TestHandlerValidatesScoreHistoryTicker(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/score-history/bad%20ticker", nil)
	recorder := httptest.NewRecorder()

	NewHandler(&fakeRepository{}, Options{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestHandlerNormalizesTradeLifecycleEvent(t *testing.T) {
	repo := &fakeRepository{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/trades/7/events",
		bytes.NewBufferString(
			`{"type":"partial_exit","quantity":10,"price":1.9,"fees":0.1}`,
		),
	)
	recorder := httptest.NewRecorder()

	NewHandler(repo, Options{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.event.Type != "PARTIAL_EXIT" {
		t.Fatalf("event type = %q", repo.event.Type)
	}
}
