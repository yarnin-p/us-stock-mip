package webull

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxResponseSize = 4 << 20

var symbolPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

type Snapshot struct {
	Symbol        string
	Price         float64
	Volume        float64
	ChangeRatio   float64
	PreviousClose float64
	ObservedAt    time.Time
}

type Gainer struct {
	Symbol      string
	Price       float64
	Volume      float64
	ChangeRatio float64
}

type BookLevel struct {
	Price float64
	Size  float64
}

type BookQuote struct {
	Symbol     string
	Asks       []BookLevel
	Bids       []BookLevel
	ObservedAt time.Time
}

type Tick struct {
	Symbol     string
	Price      float64
	Volume     float64
	Side       string
	EventID    string
	ObservedAt time.Time
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *APIError) Error() string {
	if strings.TrimSpace(err.Message) == "" {
		return fmt.Sprintf("webull returned HTTP %d", err.StatusCode)
	}
	return fmt.Sprintf(
		"webull returned HTTP %d: %s",
		err.StatusCode,
		strings.TrimSpace(err.Message),
	)
}

// DefinitiveSubmissionFailure tells the execution service that Webull
// explicitly rejected the request before accepting an order. Rate limits and
// request timeouts remain ambiguous and must be reconciled by client_order_id.
func (err *APIError) DefinitiveSubmissionFailure() bool {
	return err.StatusCode >= http.StatusBadRequest &&
		err.StatusCode < http.StatusInternalServerError &&
		err.StatusCode != http.StatusRequestTimeout &&
		err.StatusCode != http.StatusTooEarly &&
		err.StatusCode != http.StatusTooManyRequests
}

func IsInvalidSymbolError(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) &&
		apiError.StatusCode == http.StatusExpectationFailed &&
		apiError.Code == "INVALID_SYMBOL"
}

func IsRateLimitError(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) &&
		apiError.StatusCode == http.StatusTooManyRequests
}

type AccessToken struct {
	Token   string `json:"token"`
	Expires int64  `json:"expires"`
	Status  string `json:"status"`
}

type Signer struct {
	appKey, appSecret, algorithm string
}

func NewSigner(appKey, appSecret, algorithm string) (*Signer, error) {
	if strings.TrimSpace(appKey) == "" || strings.TrimSpace(appSecret) == "" {
		return nil, errors.New("webull app key and secret are required")
	}
	if algorithm != "HMAC-SHA1" && algorithm != "HMAC-SHA256" {
		return nil, fmt.Errorf("unsupported Webull signature algorithm %q", algorithm)
	}
	return &Signer{appKey: appKey, appSecret: appSecret, algorithm: algorithm}, nil
}

func (signer *Signer) Signature(
	path string, query url.Values, body []byte, host, timestamp, nonce string,
) (string, error) {
	params := map[string]string{
		"host": host, "x-app-key": signer.appKey,
		"x-signature-algorithm": signer.algorithm, "x-signature-nonce": nonce,
		"x-signature-version": "1.0", "x-timestamp": timestamp,
	}
	for key, values := range query {
		if len(values) != 1 {
			return "", fmt.Errorf("signature parameter %q must have one value", key)
		}
		params[key] = values[0]
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	canonical := path + "&" + strings.Join(parts, "&")
	if len(body) > 0 {
		if signer.algorithm == "HMAC-SHA256" {
			sum := sha256.Sum256(body)
			canonical += "&" + strings.ToUpper(hex.EncodeToString(sum[:]))
		} else {
			sum := md5.Sum(body) // #nosec G501 -- protocol-mandated body checksum, not security.
			canonical += "&" + strings.ToUpper(hex.EncodeToString(sum[:]))
		}
	}
	encoded := strings.ReplaceAll(url.QueryEscape(canonical), "+", "%20")
	var constructor func() hash.Hash
	if signer.algorithm == "HMAC-SHA256" {
		constructor = sha256.New
	} else {
		constructor = sha1.New // #nosec G505 -- Webull protocol default; SHA256 is configurable.
	}
	mac := hmac.New(constructor, []byte(signer.appSecret+"&"))
	if _, err := mac.Write([]byte(encoded)); err != nil {
		return "", fmt.Errorf("hashing Webull signature: %w", err)
	}
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

type Option func(*Client) error

type Client struct {
	baseURL     *url.URL
	http        *http.Client
	signer      *Signer
	accessToken string
	tokenMutex  sync.RWMutex
	clock       func() time.Time
	nonce       func() (string, error)
}

func NewClient(appKey, appSecret string, options ...Option) (*Client, error) {
	baseURL, _ := url.Parse("https://api.webull.com")
	signer, err := NewSigner(appKey, appSecret, "HMAC-SHA1")
	if err != nil {
		return nil, err
	}
	client := &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
		signer: signer, clock: time.Now, nonce: randomNonce,
	}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func WithBaseURL(raw string) Option {
	return func(client *Client) error {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
			parsed.Host == "" || parsed.User != nil {
			return errors.New("invalid Webull base URL")
		}
		if parsed.Scheme == "http" && parsed.Hostname() != "localhost" &&
			parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
			return errors.New("webull base URL must use HTTPS except on localhost")
		}
		client.baseURL = parsed
		return nil
	}
}

func WithAlgorithm(algorithm string) Option {
	return func(client *Client) error {
		signer, err := NewSigner(client.signer.appKey, client.signer.appSecret, algorithm)
		if err != nil {
			return err
		}
		client.signer = signer
		return nil
	}
}

func WithAccessToken(accessToken string) Option {
	return func(client *Client) error {
		client.SetAccessToken(accessToken)
		return nil
	}
}

func (client *Client) SetAccessToken(accessToken string) {
	client.tokenMutex.Lock()
	defer client.tokenMutex.Unlock()
	client.accessToken = strings.TrimSpace(accessToken)
}

func (client *Client) currentAccessToken() string {
	client.tokenMutex.RLock()
	defer client.tokenMutex.RUnlock()
	return client.accessToken
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(client *Client) error {
		if httpClient == nil {
			return errors.New("HTTP client is required")
		}
		cloned := *httpClient
		cloned.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client.http = &cloned
		return nil
	}
}

func WithClock(clock func() time.Time) Option {
	return func(client *Client) error {
		if clock == nil {
			return errors.New("clock is required")
		}
		client.clock = clock
		return nil
	}
}

func WithNonce(nonce func() (string, error)) Option {
	return func(client *Client) error {
		if nonce == nil {
			return errors.New("nonce generator is required")
		}
		client.nonce = nonce
		return nil
	}
}

func (client *Client) Snapshots(
	ctx context.Context,
	symbols []string,
) (_ []Snapshot, returnErr error) {
	return client.SnapshotsWithOvernight(ctx, symbols, false)
}

// SnapshotsBestEffort preserves valid snapshots when Webull rejects a mixed
// batch because one or more symbols are unsupported. Non-symbol failures still
// fail the entire operation.
func (client *Client) SnapshotsBestEffort(
	ctx context.Context,
	symbols []string,
	overnight bool,
) ([]Snapshot, error) {
	if len(symbols) == 0 || len(symbols) > 100 {
		return nil, errors.New("webull snapshot request requires 1 to 100 symbols")
	}
	result, err := client.SnapshotsWithOvernight(ctx, symbols, overnight)
	if err == nil {
		return result, nil
	}
	if !IsInvalidSymbolError(err) {
		return nil, err
	}
	if len(symbols) == 1 {
		return nil, nil
	}
	middle := len(symbols) / 2
	left, err := client.SnapshotsBestEffort(ctx, symbols[:middle], overnight)
	if err != nil {
		return nil, err
	}
	right, err := client.SnapshotsBestEffort(ctx, symbols[middle:], overnight)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func (client *Client) SnapshotsWithOvernight(
	ctx context.Context,
	symbols []string,
	overnight bool,
) (_ []Snapshot, returnErr error) {
	if len(symbols) == 0 || len(symbols) > 100 {
		return nil, errors.New("webull snapshot request requires 1 to 100 symbols")
	}
	normalized := make([]string, len(symbols))
	for index, symbol := range symbols {
		normalized[index] = strings.ToUpper(strings.TrimSpace(symbol))
		if !symbolPattern.MatchString(normalized[index]) {
			return nil, fmt.Errorf("invalid Webull symbol %q", symbol)
		}
	}
	endpoint := client.baseURL.JoinPath("openapi", "market-data", "stock", "snapshot")
	query := endpoint.Query()
	query.Set("symbols", strings.Join(normalized, ","))
	query.Set("category", "US_STOCK")
	if client.signer.algorithm == "HMAC-SHA256" {
		// The Thailand OpenAPI SDK serializes Python true as "True" and omits
		// optional false values. Preserve the SDK's query insertion order on
		// the wire even though signing canonicalizes parameters by key.
		query.Set("extend_hour_required", "True")
		endpoint.RawQuery = "symbols=" + url.QueryEscape(query.Get("symbols")) +
			"&category=US_STOCK&extend_hour_required=True"
		if overnight {
			query.Set("overnight_required", "True")
			endpoint.RawQuery += "&overnight_required=True"
		}
	} else {
		query.Set("extend_hour_required", "true")
		query.Set("overnight_required", strconv.FormatBool(overnight))
		endpoint.RawQuery = query.Encode()
	}
	timestamp := client.clock().UTC().Format(time.RFC3339)
	nonce, err := client.nonce()
	if err != nil {
		return nil, fmt.Errorf("generating Webull nonce: %w", err)
	}
	signaturePath := endpoint.EscapedPath()
	if !strings.HasPrefix(signaturePath, "/") {
		signaturePath = "/" + signaturePath
	}
	signature, err := client.signer.Signature(
		signaturePath, query, nil, endpoint.Host, timestamp, nonce,
	)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating Webull request: %w", err)
	}
	request.Header.Set("x-app-key", client.signer.appKey)
	request.Header.Set("x-timestamp", timestamp)
	request.Header.Set("x-signature-algorithm", client.signer.algorithm)
	request.Header.Set("x-signature-version", "1.0")
	request.Header.Set("x-signature-nonce", nonce)
	request.Header.Set("x-version", "v2")
	request.Header.Set("x-signature", signature)
	request.Header.Set("x-webull-client-source", "sdk")
	if accessToken := client.currentAccessToken(); accessToken != "" {
		request.Header.Set("x-access-token", accessToken)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("sending Webull request: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("closing Webull response: %w", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		message, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		if readErr != nil || strings.TrimSpace(string(message)) == "" {
			return nil, &APIError{StatusCode: response.StatusCode}
		}
		var payload struct {
			Code    string `json:"error_code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(message, &payload)
		return nil, &APIError{
			StatusCode: response.StatusCode,
			Code:       payload.Code,
			Message:    strings.TrimSpace(string(message)),
		}
	}
	var raw []struct {
		Symbol                  string `json:"symbol"`
		Price                   string `json:"price"`
		Volume                  string `json:"volume"`
		ChangeRatio             string `json:"change_ratio"`
		PreClose                string `json:"pre_close"`
		LastTradeTime           int64  `json:"last_trade_time"`
		ExtendedPrice           string `json:"extend_hour_last_price"`
		ExtendedVolume          string `json:"extend_hour_volume"`
		ExtendedChangeRatio     string `json:"extend_hour_change_ratio"`
		ExtendedLastTradeTimeMS int64  `json:"extend_hour_last_trade_time"`
		OvernightPrice          string `json:"ovn_price"`
		OvernightVolume         string `json:"ovn_volume"`
		OvernightChangeRatio    string `json:"ovn_change_ratio"`
		OvernightTradeTimeMS    int64  `json:"ovn_last_trade_time"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding Webull snapshots: %w", err)
	}
	snapshots := make([]Snapshot, 0, len(raw))
	for _, item := range raw {
		price, err := parseNumber("price", item.Price)
		if err != nil {
			return nil, err
		}
		previous, err := parseNumber("previous close", item.PreClose)
		if err != nil {
			return nil, err
		}
		volume := 0.0
		if strings.TrimSpace(item.Volume) != "" {
			volume, err = parseNumber("volume", item.Volume)
			if err != nil {
				return nil, err
			}
		}
		change := price/previous - 1
		if strings.TrimSpace(item.ChangeRatio) != "" {
			change, err = parseNumber("change ratio", item.ChangeRatio)
			if err != nil {
				return nil, err
			}
		}
		observedAt := time.UnixMilli(item.LastTradeTime).UTC()
		if item.ExtendedPrice != "" && item.ExtendedLastTradeTimeMS > item.LastTradeTime {
			price, err = parseNumber("extended-hours price", item.ExtendedPrice)
			if err != nil {
				return nil, err
			}
			if item.ExtendedVolume != "" {
				volume, err = parseNumber("extended-hours volume", item.ExtendedVolume)
				if err != nil {
					return nil, err
				}
			}
			if item.ExtendedChangeRatio != "" {
				change, err = parseNumber(
					"extended-hours change ratio",
					item.ExtendedChangeRatio,
				)
				if err != nil {
					return nil, err
				}
			}
			observedAt = time.UnixMilli(item.ExtendedLastTradeTimeMS).UTC()
		}
		if overnight && item.OvernightPrice != "" &&
			item.OvernightTradeTimeMS > observedAt.UnixMilli() {
			price, err = parseNumber("overnight price", item.OvernightPrice)
			if err != nil {
				return nil, err
			}
			if item.OvernightVolume != "" {
				volume, err = parseNumber("overnight volume", item.OvernightVolume)
				if err != nil {
					return nil, err
				}
			}
			if item.OvernightChangeRatio != "" {
				change, err = parseNumber(
					"overnight change ratio",
					item.OvernightChangeRatio,
				)
				if err != nil {
					return nil, err
				}
			}
			observedAt = time.UnixMilli(item.OvernightTradeTimeMS).UTC()
		}
		snapshots = append(snapshots, Snapshot{
			Symbol: item.Symbol, Price: price, Volume: volume, ChangeRatio: change,
			PreviousClose: previous, ObservedAt: observedAt,
		})
	}
	return snapshots, nil
}

func (client *Client) TopGainers(
	ctx context.Context,
	rankType string,
	pageSize int,
) ([]Gainer, error) {
	rankType = strings.ToUpper(strings.TrimSpace(rankType))
	switch rankType {
	case "PRE_MARKET", "AFTER_MARKET", "MIN_3", "MIN_5", "DAY_1":
	default:
		return nil, fmt.Errorf("unsupported Webull gainer rank type %q", rankType)
	}
	if pageSize < 1 || pageSize > 100 {
		return nil, errors.New("webull gainer page size must be between 1 and 100")
	}
	query := make(url.Values)
	query.Set("rank_type", rankType)
	query.Set("category", "US_STOCK")
	query.Set("sort_by", "CHANGE_RATIO")
	query.Set("direction", "DESC")
	query.Set("page_index", "1")
	query.Set("page_size", strconv.Itoa(pageSize))
	var payload struct {
		Data []struct {
			Symbol      string `json:"symbol"`
			Price       string `json:"price"`
			Volume      string `json:"volume"`
			ChangeRatio string `json:"change_ratio"`
		} `json:"data"`
	}
	if err := client.signedGET(
		ctx,
		"/openapi/market-data/screener/gainers-losers",
		query,
		&payload,
	); err != nil {
		return nil, fmt.Errorf("loading Webull gainers: %w", err)
	}
	gainers := make([]Gainer, 0, len(payload.Data))
	for _, item := range payload.Data {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if !symbolPattern.MatchString(symbol) {
			continue
		}
		price, err := parseNumber("gainer price", item.Price)
		if err != nil {
			return nil, err
		}
		volume, err := parseNumber("gainer volume", item.Volume)
		if err != nil {
			return nil, err
		}
		changeRatio, err := parseNumber("gainer change ratio", item.ChangeRatio)
		if err != nil {
			return nil, err
		}
		gainers = append(gainers, Gainer{
			Symbol: symbol, Price: price, Volume: volume,
			ChangeRatio: changeRatio,
		})
	}
	return gainers, nil
}

func (client *Client) CreateToken(
	ctx context.Context,
	current string,
) (AccessToken, error) {
	body := map[string]any{}
	if current = strings.TrimSpace(current); current != "" {
		body["token"] = current
	}
	return client.tokenRequest(ctx, "create", body)
}

func (client *Client) CheckToken(
	ctx context.Context,
	token string,
) (AccessToken, error) {
	return client.tokenRequest(ctx, "check", map[string]any{"token": token})
}

func (client *Client) RefreshToken(
	ctx context.Context,
	token string,
) (AccessToken, error) {
	return client.tokenRequest(ctx, "refresh", map[string]any{"token": token})
}

func (client *Client) tokenRequest(
	ctx context.Context,
	action string,
	payload map[string]any,
) (AccessToken, error) {
	var result AccessToken
	if err := client.postJSON(
		ctx, []string{"openapi", "auth", "token", action}, payload, &result,
	); err != nil {
		return AccessToken{}, err
	}
	if strings.TrimSpace(result.Token) == "" || result.Expires <= 0 ||
		strings.TrimSpace(result.Status) == "" {
		return AccessToken{}, errors.New("webull returned an invalid token response")
	}
	return result, nil
}

func (client *Client) SubscribeSnapshots(
	ctx context.Context,
	sessionID string,
	symbols []string,
) error {
	return client.subscribeMarketData(
		ctx, sessionID, symbols, []string{"SNAPSHOT"}, false,
	)
}

func (client *Client) SubscribeQuotes(
	ctx context.Context,
	sessionID string,
	symbols []string,
) error {
	return client.subscribeMarketData(
		ctx, sessionID, symbols, []string{"QUOTE"}, false,
	)
}

func (client *Client) SubscribeOrderFlow(
	ctx context.Context,
	sessionID string,
	symbols []string,
) error {
	return client.subscribeMarketData(
		ctx, sessionID, symbols, []string{"QUOTE", "TICK"}, false,
	)
}

func (client *Client) SubscribeOvernightQuotes(
	ctx context.Context,
	sessionID string,
	symbols []string,
) error {
	return client.subscribeMarketData(
		ctx, sessionID, symbols, []string{"QUOTE"}, true,
	)
}

func (client *Client) subscribeMarketData(
	ctx context.Context,
	sessionID string,
	symbols []string,
	subTypes []string,
	overnight bool,
) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("webull streaming session ID is required")
	}
	if len(symbols) == 0 || len(symbols) > 100 {
		return errors.New("webull stream subscription requires 1 to 100 symbols")
	}
	normalized := make([]string, len(symbols))
	for index, symbol := range symbols {
		normalized[index] = strings.ToUpper(strings.TrimSpace(symbol))
		if !symbolPattern.MatchString(normalized[index]) {
			return fmt.Errorf("invalid Webull symbol %q", symbol)
		}
	}
	if len(subTypes) == 0 {
		return errors.New("webull stream subscription requires a data type")
	}
	for _, subType := range subTypes {
		switch subType {
		case "QUOTE", "SNAPSHOT", "TICK":
		default:
			return fmt.Errorf("unsupported Webull stream data type %q", subType)
		}
	}
	return client.postJSON(ctx,
		[]string{"openapi", "market-data", "streaming", "subscribe"},
		map[string]any{
			"session_id": sessionID, "symbols": normalized,
			"category": "US_STOCK", "sub_types": subTypes,
			"overnight_required": overnight,
		},
		nil,
	)
}

func (client *Client) UnsubscribeAll(
	ctx context.Context,
	sessionID string,
) error {
	return client.postJSON(ctx,
		[]string{"openapi", "market-data", "streaming", "unsubscribe"},
		map[string]any{"session_id": sessionID, "unsubscribe_all": true},
		nil,
	)
}

func (client *Client) postJSON(
	ctx context.Context,
	path []string,
	payload any,
	destination any,
) (_ error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding Webull request: %w", err)
	}
	endpoint := client.baseURL.JoinPath(path...)
	timestamp := client.clock().UTC().Format(time.RFC3339)
	nonce, err := client.nonce()
	if err != nil {
		return fmt.Errorf("generating Webull nonce: %w", err)
	}
	signaturePath := endpoint.EscapedPath()
	if !strings.HasPrefix(signaturePath, "/") {
		signaturePath = "/" + signaturePath
	}
	signature, err := client.signer.Signature(
		signaturePath, endpoint.Query(), body, endpoint.Host, timestamp, nonce,
	)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint.String(), strings.NewReader(string(body)),
	)
	if err != nil {
		return fmt.Errorf("creating Webull request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-app-key", client.signer.appKey)
	request.Header.Set("x-timestamp", timestamp)
	request.Header.Set("x-signature-algorithm", client.signer.algorithm)
	request.Header.Set("x-signature-version", "1.0")
	request.Header.Set("x-signature-nonce", nonce)
	request.Header.Set("x-version", "v2")
	request.Header.Set("x-signature", signature)
	request.Header.Set("x-webull-client-source", "sdk")
	if accessToken := client.currentAccessToken(); accessToken != "" {
		request.Header.Set("x-access-token", accessToken)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("sending Webull request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		if len(path) >= 4 && path[0] == "openapi" && path[1] == "auth" &&
			path[2] == "token" {
			return fmt.Errorf("webull token endpoint returned HTTP %d", response.StatusCode)
		}
		var payload struct {
			Code string `json:"error_code"`
		}
		_ = json.Unmarshal(message, &payload)
		return &APIError{
			StatusCode: response.StatusCode,
			Code:       payload.Code,
			Message:    strings.TrimSpace(string(message)),
		}
	}
	if destination == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseSize))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).
		Decode(destination); err != nil {
		return fmt.Errorf("decoding Webull response: %w", err)
	}
	return nil
}

func parseNumber(name, raw string) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing Webull %s: %w", name, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("parsing Webull %s: value must be finite", name)
	}
	return value, nil
}

func randomNonce() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32], nil
}
