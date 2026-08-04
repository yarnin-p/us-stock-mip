package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

const (
	defaultRESTBaseURL = "https://data.alpaca.markets"
	defaultStreamURL   = "wss://stream.data.alpaca.markets/v1beta1/news"
	maxPageSize        = 50
	maxResponseBytes   = 8 << 20
	maxErrorBytes      = 4 << 10
	maxDescriptionSize = 64 << 10
)

var stockTickerPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,4}$`)

type NewsHandler func(
	context.Context,
	[]intelligence.TickerNewsItem,
) error

type NewsClient struct {
	apiKeyID   string
	secretKey  string
	restURL    *url.URL
	streamURL  *url.URL
	httpClient *http.Client
	dialer     *websocket.Dialer
}

type NewsOption func(*NewsClient) error

func WithRESTBaseURL(rawURL string) NewsOption {
	return func(client *NewsClient) error {
		parsed, err := parseURL(rawURL, "http", "https")
		if err != nil {
			return fmt.Errorf("invalid Alpaca REST base URL: %w", err)
		}
		client.restURL = parsed
		return nil
	}
}

func WithStreamURL(rawURL string) NewsOption {
	return func(client *NewsClient) error {
		parsed, err := parseURL(rawURL, "ws", "wss")
		if err != nil {
			return fmt.Errorf("invalid Alpaca news stream URL: %w", err)
		}
		client.streamURL = parsed
		return nil
	}
}

func WithHTTPClient(httpClient *http.Client) NewsOption {
	return func(client *NewsClient) error {
		if httpClient == nil {
			return errors.New("Alpaca HTTP client is required")
		}
		client.httpClient = httpClient
		return nil
	}
}

func WithWebSocketDialer(dialer *websocket.Dialer) NewsOption {
	return func(client *NewsClient) error {
		if dialer == nil {
			return errors.New("Alpaca websocket dialer is required")
		}
		client.dialer = dialer
		return nil
	}
}

func NewNewsClient(
	apiKeyID string,
	secretKey string,
	options ...NewsOption,
) (*NewsClient, error) {
	apiKeyID = strings.TrimSpace(apiKeyID)
	secretKey = strings.TrimSpace(secretKey)
	if apiKeyID == "" || secretKey == "" {
		return nil, errors.New(
			"Alpaca API key id and secret key are required",
		)
	}
	restURL, err := parseURL(defaultRESTBaseURL, "https")
	if err != nil {
		return nil, err
	}
	streamURL, err := parseURL(defaultStreamURL, "wss")
	if err != nil {
		return nil, err
	}
	defaultDialer := *websocket.DefaultDialer
	client := &NewsClient{
		apiKeyID: apiKeyID, secretKey: secretKey,
		restURL: restURL, streamURL: streamURL,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		dialer:     &defaultDialer,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func (client *NewsClient) LatestMarketNews(
	ctx context.Context,
	from time.Time,
	to time.Time,
	limit int,
) ([]intelligence.TickerNewsItem, error) {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return nil, errors.New("Alpaca news window is invalid")
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New(
			"Alpaca news limit must be between 1 and 1000",
		)
	}
	endpoint := client.restURL.JoinPath("v1beta1", "news")
	result := make([]intelligence.TickerNewsItem, 0, limit)
	articlesSeen := 0
	pageToken := ""
	for articlesSeen < limit {
		remainingArticles := limit - articlesSeen
		pageLimit := min(remainingArticles, maxPageSize)
		query := endpoint.Query()
		query.Set("start", from.UTC().Format(time.RFC3339Nano))
		query.Set("end", to.UTC().Format(time.RFC3339Nano))
		query.Set("sort", "desc")
		query.Set("limit", strconv.Itoa(pageLimit))
		query.Set("include_content", "true")
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		pageURL := *endpoint
		pageURL.RawQuery = query.Encode()

		var response newsResponse
		if err := client.getJSON(ctx, pageURL.String(), &response); err != nil {
			return nil, fmt.Errorf("fetching Alpaca news: %w", err)
		}
		for _, article := range response.News {
			if articlesSeen >= limit {
				break
			}
			items, err := articleItems(article)
			if err != nil {
				return nil, fmt.Errorf("mapping Alpaca news: %w", err)
			}
			result = append(result, items...)
			articlesSeen++
		}
		pageToken = strings.TrimSpace(response.NextPageToken)
		if pageToken == "" || len(response.News) == 0 {
			break
		}
	}
	return result, nil
}

func (client *NewsClient) Stream(
	ctx context.Context,
	handler NewsHandler,
) error {
	return client.StreamWithReady(ctx, handler, nil)
}

func (client *NewsClient) StreamWithReady(
	ctx context.Context,
	handler NewsHandler,
	onReady func(),
) error {
	if handler == nil {
		return errors.New("Alpaca news handler is required")
	}
	headers := http.Header{
		"APCA-API-KEY-ID":     []string{client.apiKeyID},
		"APCA-API-SECRET-KEY": []string{client.secretKey},
	}
	connection, response, err := client.dialer.DialContext(
		ctx,
		client.streamURL.String(),
		headers,
	)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connecting to Alpaca news stream: %w", err)
	}
	defer connection.Close()
	connection.SetReadLimit(maxResponseBytes)

	stopCloser := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-stopCloser:
		}
	}()
	defer close(stopCloser)

	isSubscribed := false
	isReady := false
	for {
		_, payload, err := connection.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("reading Alpaca news stream: %w", err)
		}
		var messages []streamMessage
		if err := json.Unmarshal(payload, &messages); err != nil {
			return fmt.Errorf("decoding Alpaca news stream: %w", err)
		}
		for _, message := range messages {
			switch message.Type {
			case "success":
				if message.Message != "authenticated" || isSubscribed {
					continue
				}
				if err := connection.SetWriteDeadline(
					time.Now().Add(5 * time.Second),
				); err != nil {
					return fmt.Errorf(
						"setting Alpaca subscription deadline: %w",
						err,
					)
				}
				if err := connection.WriteJSON(map[string]any{
					"action": "subscribe",
					"news":   []string{"*"},
				}); err != nil {
					return fmt.Errorf(
						"subscribing to Alpaca news: %w",
						err,
					)
				}
				isSubscribed = true
			case "error":
				return fmt.Errorf(
					"Alpaca news stream error %d: %s",
					message.Code,
					strings.TrimSpace(message.Message),
				)
			case "subscription":
				if !isReady {
					isReady = true
					if onReady != nil {
						onReady()
					}
				}
			case "n":
				items, err := articleItems(message.newsArticle)
				if err != nil {
					return fmt.Errorf("mapping streamed Alpaca news: %w", err)
				}
				if len(items) == 0 {
					continue
				}
				if err := handler(ctx, items); err != nil {
					return fmt.Errorf("handling Alpaca news: %w", err)
				}
			}
		}
	}
}

func (client *NewsClient) getJSON(
	ctx context.Context,
	requestURL string,
	destination any,
) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		requestURL,
		nil,
	)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	request.Header.Set("APCA-API-KEY-ID", client.apiKeyID)
	request.Header.Set("APCA-API-SECRET-KEY", client.secretKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(
			response.Body,
			maxErrorBytes,
		))
		if readErr != nil {
			return fmt.Errorf(
				"status %d and reading error body: %w",
				response.StatusCode,
				readErr,
			)
		}
		return fmt.Errorf(
			"status %d: %s",
			response.StatusCode,
			strings.TrimSpace(string(body)),
		)
	}
	decoder := json.NewDecoder(io.LimitReader(
		response.Body,
		maxResponseBytes,
	))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

func articleItems(
	article newsArticle,
) ([]intelligence.TickerNewsItem, error) {
	if article.ID < 1 {
		return nil, errors.New("article id must be positive")
	}
	publishedAt, err := time.Parse(time.RFC3339Nano, article.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at: %w", err)
	}
	description := strings.TrimSpace(
		strings.TrimSpace(article.Summary) + "\n" +
			strings.TrimSpace(article.Content),
	)
	if len(description) > maxDescriptionSize {
		description = description[:maxDescriptionSize]
	}
	tickers := normalizedStockTickers(article.Symbols)
	result := make([]intelligence.TickerNewsItem, 0, len(tickers))
	for _, ticker := range tickers {
		result = append(result, intelligence.TickerNewsItem{
			Ticker: ticker,
			News: intelligence.NewsItem{
				ExternalID:  "alpaca:" + strconv.FormatInt(article.ID, 10),
				PublishedAt: publishedAt.UTC(),
				Title:       strings.TrimSpace(article.Headline),
				Description: description,
				URL:         strings.TrimSpace(article.URL),
			},
		})
	}
	return result, nil
}

func normalizedStockTickers(symbols []string) []string {
	result := make([]string, 0, len(symbols))
	seen := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		ticker := strings.ToUpper(strings.TrimSpace(symbol))
		if !stockTickerPattern.MatchString(ticker) {
			continue
		}
		if _, ok := seen[ticker]; ok {
			continue
		}
		seen[ticker] = struct{}{}
		result = append(result, ticker)
	}
	slices.Sort(result)
	return result
}

func parseURL(rawURL string, allowedSchemes ...string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return nil, errors.New("URL must be absolute")
	}
	if !slices.Contains(allowedSchemes, strings.ToLower(parsed.Scheme)) {
		return nil, fmt.Errorf(
			"URL scheme must be one of %s",
			strings.Join(allowedSchemes, ", "),
		)
	}
	return parsed, nil
}

type newsResponse struct {
	News          []newsArticle `json:"news"`
	NextPageToken string        `json:"next_page_token"`
}

type newsArticle struct {
	ID        int64    `json:"id"`
	Headline  string   `json:"headline"`
	Summary   string   `json:"summary"`
	Author    string   `json:"author"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Content   string   `json:"content"`
	URL       string   `json:"url"`
	Symbols   []string `json:"symbols"`
	Source    string   `json:"source"`
}

type streamMessage struct {
	Type    string `json:"T"`
	Message string `json:"msg"`
	Code    int    `json:"code"`
	newsArticle
}
