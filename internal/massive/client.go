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
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/model"
)

const (
	defaultBaseURL   = "https://api.massive.com"
	defaultTimeout   = 15 * time.Second
	maxResponseSize  = 16 << 20
	maxPages         = 100
	maxAggregateBars = 100_000
)

var tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

type Option func(*Client) error

type Client struct {
	apiKey     string
	baseURL    *url.URL
	httpClient *http.Client
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

func (client *Client) Ticker(ctx context.Context, ticker string) (model.Stock, error) {
	ticker, err := normalizeTicker(ticker)
	if err != nil {
		return model.Stock{}, err
	}

	endpoint := client.baseURL.JoinPath("v3", "reference", "tickers", ticker)
	var response tickerResponse
	if err := client.getJSON(ctx, endpoint, &response); err != nil {
		return model.Stock{}, fmt.Errorf("fetching ticker %s: %w", ticker, err)
	}
	if response.Results.Ticker == "" {
		return model.Stock{}, fmt.Errorf("fetching ticker %s: response contained no ticker", ticker)
	}

	return model.Stock{
		Ticker:      response.Results.Ticker,
		CompanyName: response.Results.Name,
		Exchange:    response.Results.PrimaryExchange,
		Sector:      response.Results.SICDescription,
		MarketCap:   response.Results.MarketCap,
	}, nil
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

func (client *Client) getJSON(
	ctx context.Context,
	endpoint *url.URL,
	destination any,
) (returnErr error) {
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

type tickerResponse struct {
	Results struct {
		Ticker          string   `json:"ticker"`
		Name            string   `json:"name"`
		PrimaryExchange string   `json:"primary_exchange"`
		SICDescription  string   `json:"sic_description"`
		MarketCap       *float64 `json:"market_cap"`
	} `json:"results"`
}

type floatResponse struct {
	Results []struct {
		FreeFloat *int64 `json:"free_float"`
	} `json:"results"`
}
