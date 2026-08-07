package massive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

const (
	defaultBaseURL      = "https://api.massive.com"
	defaultTimeout      = 15 * time.Second
	maxResponseSize     = 16 << 20
	maxPages            = 100
	maxAggregateBars    = 100_000
	maxMarketBars       = 50_000
	maxReferenceTickers = 50_000
	maxNewsItems        = 100_000
	maxSplitItems       = 100_000
)

var tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

type Option func(*Client) error

type Client struct {
	apiKey             string
	baseURL            *url.URL
	httpClient         *http.Client
	rateMu             sync.Mutex
	minRequestInterval time.Duration
	lastRequest        time.Time
}

func NewClient(apiKey string, options ...Option) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("massive API key is required")
	}

	baseURL, err := url.Parse(defaultBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing default Massive base URL: %w", err)
	}

	client := &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:       defaultTimeout,
			CheckRedirect: rejectRedirect,
		},
	}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, fmt.Errorf("configuring Massive client: %w", err)
		}
	}

	return client, nil
}

func WithBaseURL(rawURL string) Option {
	return func(client *Client) error {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("parsing base URL: %w", err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("unsupported base URL scheme %q", parsed.Scheme)
		}
		if parsed.Host == "" {
			return errors.New("base URL host is required")
		}
		client.baseURL = parsed
		return nil
	}
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(client *Client) error {
		if httpClient == nil {
			return errors.New("HTTP client is required")
		}
		cloned := *httpClient
		cloned.CheckRedirect = rejectRedirect
		client.httpClient = &cloned
		return nil
	}
}

func WithMinRequestInterval(interval time.Duration) Option {
	return func(client *Client) error {
		if interval < 0 || interval > time.Minute {
			return errors.New("massive minimum request interval is invalid")
		}
		client.minRequestInterval = interval
		return nil
	}
}

func (client *Client) Aggregates(ctx context.Context, query model.AggregateQuery) ([]model.AggregateBar, error) {
	if err := validateAggregateQuery(query); err != nil {
		return nil, err
	}

	endpoint := client.baseURL.JoinPath(
		"v2",
		"aggs",
		"ticker",
		query.Ticker,
		"range",
		strconv.Itoa(query.Multiplier),
		string(query.Timespan),
		query.From,
		query.To,
	)
	values := endpoint.Query()
	values.Set("adjusted", "true")
	values.Set("sort", "asc")
	values.Set("limit", "50000")
	endpoint.RawQuery = values.Encode()

	var bars []model.AggregateBar
	for page := 0; endpoint != nil; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("fetching aggregates for %s: pagination exceeded %d pages", query.Ticker, maxPages)
		}

		var response aggregateResponse
		if err := client.getJSON(ctx, endpoint, &response); err != nil {
			return nil, fmt.Errorf("fetching aggregates for %s: %w", query.Ticker, err)
		}
		if len(bars)+len(response.Results) > maxAggregateBars {
			return nil, fmt.Errorf(
				"fetching aggregates for %s: result exceeds %d bars",
				query.Ticker,
				maxAggregateBars,
			)
		}
		for _, result := range response.Results {
			bars = append(bars, model.AggregateBar{
				Ticker:       query.Ticker,
				Timestamp:    time.UnixMilli(result.Timestamp).UTC(),
				Open:         result.Open,
				High:         result.High,
				Low:          result.Low,
				Close:        result.Close,
				Volume:       result.Volume,
				VWAP:         result.VWAP,
				Transactions: result.Transactions,
			})
		}

		var err error
		endpoint, err = client.nextPageURL(response.NextURL)
		if err != nil {
			return nil, fmt.Errorf("fetching aggregates for %s: %w", query.Ticker, err)
		}
	}

	return bars, nil
}

// DailySummary returns unadjusted OHLCV bars for all non-OTC U.S. stocks on a
// trading date. Consumers building an opening list must use only Open from the
// requested date; the remaining fields are persisted for later sessions.
func (client *Client) DailySummary(
	ctx context.Context,
	date time.Time,
) ([]model.AggregateBar, error) {
	if date.IsZero() {
		return nil, errors.New("daily summary date is required")
	}
	dateRaw := date.Format(time.DateOnly)
	endpoint := client.baseURL.JoinPath(
		"v2", "aggs", "grouped", "locale", "us", "market", "stocks", dateRaw,
	)
	values := endpoint.Query()
	values.Set("adjusted", "false")
	values.Set("include_otc", "false")
	endpoint.RawQuery = values.Encode()

	var response dailySummaryResponse
	if err := client.getJSON(ctx, endpoint, &response); err != nil {
		return nil, fmt.Errorf("fetching daily market summary for %s: %w", dateRaw, err)
	}
	if len(response.Results) > maxMarketBars {
		return nil, fmt.Errorf(
			"fetching daily market summary for %s: result exceeds %d bars",
			dateRaw,
			maxMarketBars,
		)
	}
	bars := make([]model.AggregateBar, 0, len(response.Results))
	for _, result := range response.Results {
		ticker, err := normalizeTicker(result.Ticker)
		if err != nil {
			continue
		}
		if ticker != result.Ticker {
			// Massive uses mixed-case symbols for some preferred instruments.
			// The MIP opening universe is uppercase U.S. common-stock symbols;
			// uppercasing these values could collide with a different ticker.
			continue
		}
		bars = append(bars, model.AggregateBar{
			Ticker:       ticker,
			Timestamp:    time.UnixMilli(result.Timestamp).UTC(),
			Open:         result.Open,
			High:         result.High,
			Low:          result.Low,
			Close:        result.Close,
			Volume:       result.Volume,
			VWAP:         result.VWAP,
			Transactions: result.Transactions,
		})
	}
	return bars, nil
}

func (client *Client) Ticker(ctx context.Context, ticker string) (model.Stock, error) {
	return client.TickerAt(ctx, ticker, time.Time{})
}

func (client *Client) TickerAt(
	ctx context.Context,
	ticker string,
	asOf time.Time,
) (model.Stock, error) {
	ticker, err := normalizeTicker(ticker)
	if err != nil {
		return model.Stock{}, err
	}

	endpoint := client.baseURL.JoinPath("v3", "reference", "tickers", ticker)
	if !asOf.IsZero() {
		values := endpoint.Query()
		values.Set("date", asOf.Format(time.DateOnly))
		endpoint.RawQuery = values.Encode()
	}
	var response tickerResponse
	if err := client.getJSON(ctx, endpoint, &response); err != nil {
		return model.Stock{}, fmt.Errorf("fetching ticker %s: %w", ticker, err)
	}
	if response.Results.Ticker == "" {
		return model.Stock{}, fmt.Errorf("fetching ticker %s: response contained no ticker", ticker)
	}

	return model.Stock{
		Ticker:       response.Results.Ticker,
		CompanyName:  response.Results.Name,
		Exchange:     response.Results.PrimaryExchange,
		Sector:       response.Results.SICDescription,
		SecurityType: response.Results.Type,
		MarketCap:    response.Results.MarketCap,
	}, nil
}

// CommonStocks returns the point-in-time U.S. common-stock universe. This
// excludes ETFs, preferred shares, warrants, units, and other grouped-summary
// instruments before they can enter the opening candidate pool.
func (client *Client) CommonStocks(
	ctx context.Context,
	asOf time.Time,
) ([]model.Stock, error) {
	if asOf.IsZero() {
		return nil, errors.New("common-stock universe date is required")
	}
	endpoint := client.baseURL.JoinPath("v3", "reference", "tickers")
	values := endpoint.Query()
	values.Set("market", "stocks")
	values.Set("type", "CS")
	values.Set("active", "true")
	values.Set("date", asOf.Format(time.DateOnly))
	values.Set("limit", "1000")
	values.Set("sort", "ticker")
	values.Set("order", "asc")
	endpoint.RawQuery = values.Encode()

	var stocks []model.Stock
	for page := 0; endpoint != nil; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf(
				"fetching common-stock universe: pagination exceeded %d pages",
				maxPages,
			)
		}
		var response tickerListResponse
		if err := client.getJSON(ctx, endpoint, &response); err != nil {
			return nil, fmt.Errorf("fetching common-stock universe: %w", err)
		}
		if len(stocks)+len(response.Results) > maxReferenceTickers {
			return nil, fmt.Errorf(
				"fetching common-stock universe: result exceeds %d tickers",
				maxReferenceTickers,
			)
		}
		for _, result := range response.Results {
			ticker, err := normalizeTicker(result.Ticker)
			if err != nil || ticker != result.Ticker || result.Type != "CS" {
				continue
			}
			stocks = append(stocks, model.Stock{
				Ticker:       ticker,
				CompanyName:  result.Name,
				Exchange:     result.PrimaryExchange,
				SecurityType: result.Type,
			})
		}
		next, err := client.nextPageURL(response.NextURL)
		if err != nil {
			return nil, fmt.Errorf("fetching common-stock universe: %w", err)
		}
		endpoint = next
	}
	return stocks, nil
}

func (client *Client) Float(ctx context.Context, ticker string) (*int64, error) {
	ticker, err := normalizeTicker(ticker)
	if err != nil {
		return nil, err
	}

	endpoint := client.baseURL.JoinPath("stocks", "vX", "float")
	values := endpoint.Query()
	values.Set("ticker", ticker)
	values.Set("limit", "1")
	endpoint.RawQuery = values.Encode()

	var response floatResponse
	if err := client.getJSON(ctx, endpoint, &response); err != nil {
		return nil, fmt.Errorf("fetching float for %s: %w", ticker, err)
	}
	if len(response.Results) == 0 {
		return nil, nil
	}
	return response.Results[0].FreeFloat, nil
}

func (client *Client) News(
	ctx context.Context,
	ticker string,
	publishedBefore time.Time,
) ([]intelligence.NewsItem, error) {
	ticker, err := normalizeTicker(ticker)
	if err != nil {
		return nil, err
	}
	endpoint := client.baseURL.JoinPath("v2", "reference", "news")
	values := endpoint.Query()
	values.Set("ticker", ticker)
	values.Set("published_utc.lte", publishedBefore.UTC().Format(time.RFC3339))
	values.Set("order", "desc")
	values.Set("limit", "1000")
	values.Set("sort", "published_utc")
	endpoint.RawQuery = values.Encode()

	var items []intelligence.NewsItem
	for page := 0; endpoint != nil; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("fetching news for %s: pagination exceeded %d pages", ticker, maxPages)
		}
		var response newsResponse
		if err := client.getJSON(ctx, endpoint, &response); err != nil {
			return nil, fmt.Errorf("fetching news for %s: %w", ticker, err)
		}
		if len(items)+len(response.Results) > maxNewsItems {
			return nil, fmt.Errorf("fetching news for %s: result exceeds %d items", ticker, maxNewsItems)
		}
		items, err = appendNewsResults(items, response, ticker)
		if err != nil {
			return nil, err
		}
		endpoint, err = client.nextPageURL(response.NextURL)
		if err != nil {
			return nil, fmt.Errorf("fetching news for %s: %w", ticker, err)
		}
	}
	return items, nil
}

// LatestNews returns one bounded page and deliberately does not follow
// pagination. It is intended for rate-limited live catalyst enrichment, not
// historical news backfills.
func (client *Client) LatestNews(
	ctx context.Context,
	ticker string,
	publishedAfter time.Time,
	publishedBefore time.Time,
	limit int,
) ([]intelligence.NewsItem, error) {
	ticker, err := normalizeTicker(ticker)
	if err != nil {
		return nil, err
	}
	if publishedAfter.IsZero() || publishedBefore.IsZero() ||
		!publishedAfter.Before(publishedBefore) {
		return nil, errors.New("latest news window is invalid")
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("latest news limit must be between 1 and 1000")
	}
	endpoint := client.baseURL.JoinPath("v2", "reference", "news")
	values := endpoint.Query()
	values.Set("ticker", ticker)
	values.Set(
		"published_utc.gte",
		publishedAfter.UTC().Format(time.RFC3339),
	)
	values.Set(
		"published_utc.lte",
		publishedBefore.UTC().Format(time.RFC3339),
	)
	values.Set("order", "desc")
	values.Set("limit", strconv.Itoa(limit))
	values.Set("sort", "published_utc")
	endpoint.RawQuery = values.Encode()

	var response newsResponse
	if err := client.getJSON(ctx, endpoint, &response); err != nil {
		return nil, fmt.Errorf("fetching latest news for %s: %w", ticker, err)
	}
	return appendNewsResults(nil, response, ticker)
}

// LatestMarketNews returns one bounded, all-market page. It intentionally does
// not follow pagination so a live monitor has a predictable request budget.
// Every article is expanded to one item per valid ticker.
func (client *Client) LatestMarketNews(
	ctx context.Context,
	publishedAfter time.Time,
	publishedBefore time.Time,
	limit int,
) ([]intelligence.TickerNewsItem, error) {
	if publishedAfter.IsZero() || publishedBefore.IsZero() ||
		!publishedAfter.Before(publishedBefore) {
		return nil, errors.New("latest market news window is invalid")
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New(
			"latest market news limit must be between 1 and 1000",
		)
	}
	endpoint := client.baseURL.JoinPath("v2", "reference", "news")
	values := endpoint.Query()
	values.Set(
		"published_utc.gte",
		publishedAfter.UTC().Format(time.RFC3339),
	)
	values.Set(
		"published_utc.lte",
		publishedBefore.UTC().Format(time.RFC3339),
	)
	values.Set("order", "desc")
	values.Set("limit", strconv.Itoa(limit))
	values.Set("sort", "published_utc")
	endpoint.RawQuery = values.Encode()

	var response newsResponse
	if err := client.getJSON(ctx, endpoint, &response); err != nil {
		return nil, fmt.Errorf("fetching latest market news: %w", err)
	}
	items := make([]intelligence.TickerNewsItem, 0)
	for _, result := range response.Results {
		publishedAt, err := time.Parse(time.RFC3339, result.PublishedUTC)
		if err != nil {
			return nil, fmt.Errorf(
				"parsing all-market news timestamp: %w",
				err,
			)
		}
		tickers := make([]string, 0, len(result.Tickers)+len(result.Insights))
		seen := make(map[string]struct{})
		addTicker := func(raw string) {
			ticker, normalizeErr := normalizeTicker(raw)
			if normalizeErr != nil {
				return
			}
			if _, exists := seen[ticker]; exists {
				return
			}
			seen[ticker] = struct{}{}
			tickers = append(tickers, ticker)
		}
		for _, ticker := range result.Tickers {
			addTicker(ticker)
		}
		for _, insight := range result.Insights {
			addTicker(insight.Ticker)
		}
		for _, ticker := range tickers {
			var sentiment string
			for _, insight := range result.Insights {
				if strings.EqualFold(insight.Ticker, ticker) {
					sentiment = insight.Sentiment
					break
				}
			}
			items = append(items, intelligence.TickerNewsItem{
				Ticker: ticker,
				News: intelligence.NewsItem{
					ExternalID: result.ID, PublishedAt: publishedAt,
					Title: result.Title, Description: result.Description,
					URL: result.ArticleURL, Sentiment: sentiment,
				},
			})
		}
	}
	return items, nil
}

func appendNewsResults(
	items []intelligence.NewsItem,
	response newsResponse,
	ticker string,
) ([]intelligence.NewsItem, error) {
	for _, result := range response.Results {
		publishedAt, err := time.Parse(time.RFC3339, result.PublishedUTC)
		if err != nil {
			return nil, fmt.Errorf(
				"parsing news timestamp for %s: %w",
				ticker,
				err,
			)
		}
		var sentiment string
		for _, insight := range result.Insights {
			if insight.Ticker == ticker {
				sentiment = insight.Sentiment
				break
			}
		}
		items = append(items, intelligence.NewsItem{
			ExternalID: result.ID, PublishedAt: publishedAt,
			Title: result.Title, Description: result.Description,
			URL: result.ArticleURL, Sentiment: sentiment,
		})
	}
	return items, nil
}

// SplitsInRange returns every split executing inside a date window, across the
// whole market rather than for one ticker.
//
// It exists because a session's gainer list cannot be built without it. The
// daily bars are stored unadjusted, so a one-for-eight reverse split reads as a
// 700% gain when today's close is compared with yesterday's; ranking on that
// puts a mechanical share consolidation at the top of a leaderboard meant for
// names people actually bought.
func (client *Client) SplitsInRange(
	ctx context.Context,
	from, to time.Time,
) ([]SplitEvent, error) {
	endpoint := client.baseURL.JoinPath("stocks", "v1", "splits")
	values := endpoint.Query()
	values.Set("execution_date.gte", from.Format(time.DateOnly))
	values.Set("execution_date.lte", to.Format(time.DateOnly))
	values.Set("limit", "1000")
	values.Set("sort", "execution_date")
	values.Set("order", "asc")
	endpoint.RawQuery = values.Encode()

	events := make([]SplitEvent, 0, 256)
	for page := 0; endpoint != nil; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("fetching splits: pagination exceeded %d pages", maxPages)
		}
		var response splitResponse
		if err := client.getJSON(ctx, endpoint, &response); err != nil {
			return nil, fmt.Errorf("fetching splits: %w", err)
		}
		if len(events)+len(response.Results) > maxSplitItems {
			return nil, fmt.Errorf("fetching splits: result exceeds %d items", maxSplitItems)
		}
		for _, result := range response.Results {
			executionDate, err := time.Parse(time.DateOnly, result.ExecutionDate)
			if err != nil {
				return nil, fmt.Errorf("parsing split date: %w", err)
			}
			ticker, err := normalizeTicker(result.Ticker)
			if err != nil {
				// A malformed symbol is not worth failing the whole window for.
				continue
			}
			events = append(events, SplitEvent{
				Ticker: ticker, ExternalID: result.ID,
				ExecutionDate: executionDate,
				From:          result.SplitFrom, To: result.SplitTo,
				Reverse: result.SplitTo < result.SplitFrom,
			})
		}
		next, err := client.nextPageURL(response.NextURL)
		if err != nil {
			return nil, fmt.Errorf("fetching splits: %w", err)
		}
		endpoint = next
	}
	return events, nil
}

// SplitEvent is one split, carrying the ticker so a market-wide sync can store
// it without a second lookup.
type SplitEvent struct {
	Ticker        string
	ExternalID    string
	ExecutionDate time.Time
	From          float64
	To            float64
	Reverse       bool
}

func (client *Client) Splits(
	ctx context.Context,
	ticker string,
	executionBefore time.Time,
) ([]intelligence.Split, error) {
	ticker, err := normalizeTicker(ticker)
	if err != nil {
		return nil, err
	}
	endpoint := client.baseURL.JoinPath("stocks", "v1", "splits")
	values := endpoint.Query()
	values.Set("ticker", ticker)
	values.Set("execution_date.lte", executionBefore.Format(time.DateOnly))
	values.Set("limit", "5000")
	values.Set("sort", "execution_date")
	values.Set("order", "desc")
	endpoint.RawQuery = values.Encode()

	var splits []intelligence.Split
	for page := 0; endpoint != nil; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("fetching splits for %s: pagination exceeded %d pages", ticker, maxPages)
		}
		var response splitResponse
		if err := client.getJSON(ctx, endpoint, &response); err != nil {
			return nil, fmt.Errorf("fetching splits for %s: %w", ticker, err)
		}
		if len(splits)+len(response.Results) > maxSplitItems {
			return nil, fmt.Errorf("fetching splits for %s: result exceeds %d items", ticker, maxSplitItems)
		}
		for _, result := range response.Results {
			executionDate, err := time.Parse(time.DateOnly, result.ExecutionDate)
			if err != nil {
				return nil, fmt.Errorf("parsing split date for %s: %w", ticker, err)
			}
			splits = append(splits, intelligence.Split{
				ExternalID: result.ID, ExecutionDate: executionDate,
				From: result.SplitFrom, To: result.SplitTo,
				Reverse: result.SplitTo < result.SplitFrom,
			})
		}
		endpoint, err = client.nextPageURL(response.NextURL)
		if err != nil {
			return nil, fmt.Errorf("fetching splits for %s: %w", ticker, err)
		}
	}
	return splits, nil
}

func (client *Client) getJSON(
	ctx context.Context,
	endpoint *url.URL,
	destination any,
) (returnErr error) {
	if err := client.waitRateLimit(ctx); err != nil {
		return err
	}
	requestURL := cloneURL(endpoint)
	values := requestURL.Query()
	values.Set("apiKey", client.apiKey)
	requestURL.RawQuery = values.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "mip/1.0")

	response, err := client.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("sending request: %w", context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("sending request: %w", context.DeadlineExceeded)
		}
		return fmt.Errorf("sending request: %s", redactMessage(err.Error(), client.apiKey))
	}
	defer func() {
		if err := response.Body.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("closing response body: %w", err)
		}
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var apiError struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&apiError); err != nil {
			return fmt.Errorf("massive returned HTTP %d", response.StatusCode)
		}
		if apiError.Error == "" {
			return fmt.Errorf("massive returned HTTP %d", response.StatusCode)
		}
		message := redactMessage(apiError.Error, client.apiKey)
		return fmt.Errorf("massive returned HTTP %d: %s", response.StatusCode, message)
	}

	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).Decode(destination); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

func (client *Client) waitRateLimit(ctx context.Context) error {
	if client.minRequestInterval == 0 {
		return nil
	}
	client.rateMu.Lock()
	defer client.rateMu.Unlock()
	wait := time.Until(client.lastRequest.Add(client.minRequestInterval))
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for Massive rate limit: %w", ctx.Err())
		case <-timer.C:
		}
	}
	client.lastRequest = time.Now()
	return nil
}

func (client *Client) nextPageURL(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, nil
	}

	nextURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parsing next page URL: %w", err)
	}
	if nextURL.Scheme != client.baseURL.Scheme || !strings.EqualFold(nextURL.Host, client.baseURL.Host) {
		return nil, errors.New("next page URL changed origin")
	}
	return nextURL, nil
}

func validateAggregateQuery(query model.AggregateQuery) error {
	ticker, err := normalizeTicker(query.Ticker)
	if err != nil {
		return err
	}
	if ticker != query.Ticker {
		return errors.New("aggregate ticker must be uppercase")
	}
	if query.Multiplier < 1 {
		return errors.New("aggregate multiplier must be positive")
	}
	if query.Timespan != model.TimespanDay && query.Timespan != model.TimespanMinute {
		return fmt.Errorf("unsupported aggregate timespan %q", query.Timespan)
	}
	if query.From == "" || query.To == "" {
		return errors.New("aggregate date range is required")
	}
	return nil
}

func normalizeTicker(ticker string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(ticker))
	if !tickerPattern.MatchString(normalized) {
		return "", fmt.Errorf("invalid ticker %q", ticker)
	}
	return normalized, nil
}

func cloneURL(source *url.URL) *url.URL {
	cloned := *source
	return &cloned
}

func rejectRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func redactMessage(message, apiKey string) string {
	message = strings.ReplaceAll(message, apiKey, "[REDACTED]")
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(message)
}

type aggregateResponse struct {
	Results []struct {
		Open         float64  `json:"o"`
		High         float64  `json:"h"`
		Low          float64  `json:"l"`
		Close        float64  `json:"c"`
		Volume       float64  `json:"v"`
		VWAP         *float64 `json:"vw"`
		Timestamp    int64    `json:"t"`
		Transactions *int64   `json:"n"`
	} `json:"results"`
	NextURL string `json:"next_url"`
}

type dailySummaryResponse struct {
	Results []struct {
		Ticker       string   `json:"T"`
		Open         float64  `json:"o"`
		High         float64  `json:"h"`
		Low          float64  `json:"l"`
		Close        float64  `json:"c"`
		Volume       float64  `json:"v"`
		VWAP         *float64 `json:"vw"`
		Timestamp    int64    `json:"t"`
		Transactions *int64   `json:"n"`
	} `json:"results"`
}

type tickerResponse struct {
	Results struct {
		Ticker          string   `json:"ticker"`
		Name            string   `json:"name"`
		PrimaryExchange string   `json:"primary_exchange"`
		SICDescription  string   `json:"sic_description"`
		Type            string   `json:"type"`
		MarketCap       *float64 `json:"market_cap"`
	} `json:"results"`
}

type tickerListResponse struct {
	Results []struct {
		Ticker          string `json:"ticker"`
		Name            string `json:"name"`
		PrimaryExchange string `json:"primary_exchange"`
		Type            string `json:"type"`
	} `json:"results"`
	NextURL string `json:"next_url"`
}

type floatResponse struct {
	Results []struct {
		FreeFloat *int64 `json:"free_float"`
	} `json:"results"`
}

type newsResponse struct {
	Results []struct {
		ID           string   `json:"id"`
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		PublishedUTC string   `json:"published_utc"`
		ArticleURL   string   `json:"article_url"`
		Tickers      []string `json:"tickers"`
		Insights     []struct {
			Ticker    string `json:"ticker"`
			Sentiment string `json:"sentiment"`
		} `json:"insights"`
	} `json:"results"`
	NextURL string `json:"next_url"`
}

type splitResponse struct {
	Results []struct {
		ID            string  `json:"id"`
		Ticker        string  `json:"ticker"`
		ExecutionDate string  `json:"execution_date"`
		SplitFrom     float64 `json:"split_from"`
		SplitTo       float64 `json:"split_to"`
	} `json:"results"`
	NextURL string `json:"next_url"`
}
