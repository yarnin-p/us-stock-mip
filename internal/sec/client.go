package sec

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"golang.org/x/net/html/charset"
)

const maxResponseSize = 16 << 20
const maxDocumentSize = 16 << 20

var cikPattern = regexp.MustCompile(`^\d{10}$`)
var titleCIKPattern = regexp.MustCompile(`\((\d{10})\)`)
var htmlTagPattern = regexp.MustCompile(`(?s)<[^>]*>`)
var whitespacePattern = regexp.MustCompile(`\s+`)
var accessionPattern = regexp.MustCompile(`accession-number=([0-9-]+)`)

type Client struct {
	baseURL    *url.URL
	webBaseURL *url.URL
	http       *http.Client
	userAgent  string

	tickerMu        sync.Mutex
	tickersByCIK    map[string]companyTicker
	tickersLoadedAt time.Time
}

type Option func(*Client) error

func NewClient(userAgent string, httpClient *http.Client, options ...Option) (*Client, error) {
	if strings.TrimSpace(userAgent) == "" {
		return nil, errors.New("SEC_USER_AGENT is required")
	}
	if httpClient == nil {
		return nil, errors.New("SEC HTTP client is required")
	}
	baseURL, _ := url.Parse("https://data.sec.gov")
	webBaseURL, _ := url.Parse("https://www.sec.gov")
	cloned := *httpClient
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client := &Client{
		baseURL: baseURL, webBaseURL: webBaseURL,
		http: &cloned, userAgent: userAgent,
	}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

func WithWebBaseURL(raw string) Option {
	return func(client *Client) error {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" {
			return errors.New("invalid SEC web base URL")
		}
		client.webBaseURL = parsed
		return nil
	}
}

func WithBaseURL(raw string) Option {
	return func(client *Client) error {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" {
			return errors.New("invalid SEC base URL")
		}
		client.baseURL = parsed
		return nil
	}
}

func (client *Client) Filings(
	ctx context.Context,
	cik string,
	asOf time.Time,
) ([]intelligence.Filing, error) {
	if !cikPattern.MatchString(cik) {
		return nil, fmt.Errorf("SEC CIK must be exactly ten digits")
	}
	endpoint := client.baseURL.JoinPath("submissions", "CIK"+cik+".json")
	var payload submissions
	if err := client.getJSON(ctx, endpoint, &payload); err != nil {
		return nil, err
	}
	filings, err := mapFilings(cik, asOf, payload.Filings.Recent)
	if err != nil {
		return nil, err
	}
	for _, file := range payload.Filings.Files {
		if file.Name == "" {
			continue
		}
		if file.From != "" {
			from, err := time.Parse(time.DateOnly, file.From)
			if err != nil {
				return nil, fmt.Errorf("parsing SEC submissions-file range: %w", err)
			}
			if from.After(asOf) {
				continue
			}
		}
		timer := time.NewTimer(120 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
		var older recentFilings
		if err := client.getJSON(
			ctx,
			client.baseURL.JoinPath("submissions", file.Name),
			&older,
		); err != nil {
			return nil, fmt.Errorf("fetching SEC submissions file %s: %w", file.Name, err)
		}
		mapped, err := mapFilings(cik, asOf, older)
		if err != nil {
			return nil, err
		}
		filings = append(filings, mapped...)
	}
	return filings, nil
}

func (client *Client) getJSON(
	ctx context.Context,
	endpoint *url.URL,
	destination any,
) (returnErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("creating SEC request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", client.userAgent)
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("sending SEC request: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("closing SEC response: %w", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("SEC returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).Decode(destination); err != nil {
		return fmt.Errorf("decoding SEC submissions: %w", err)
	}
	return nil
}

type CurrentFiling struct {
	Ticker      string
	CIK         string
	CompanyName string
	FormType    string
	AccessionNo string
	AcceptedAt  time.Time
	SourceURL   string
}

func (client *Client) CurrentFilings(
	ctx context.Context,
	count int,
) ([]CurrentFiling, error) {
	if count < 10 || count > 1000 {
		return nil, errors.New(
			"SEC current filing count must be between 10 and 1000",
		)
	}
	tickers, err := client.companyTickers(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := client.webBaseURL.JoinPath("cgi-bin", "browse-edgar")
	values := endpoint.Query()
	values.Set("action", "getcurrent")
	values.Set("output", "atom")
	values.Set("count", strconv.Itoa(count))
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		endpoint.String(),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("creating SEC current-filings request: %w", err)
	}
	request.Header.Set("Accept", "application/atom+xml,application/xml")
	request.Header.Set("User-Agent", client.userAgent)
	response, err := client.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("sending SEC current-filings request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"SEC current filings returned HTTP %d",
			response.StatusCode,
		)
	}
	var feed currentFeed
	decoder := xml.NewDecoder(io.LimitReader(response.Body, maxResponseSize))
	decoder.CharsetReader = charset.NewReaderLabel
	if err := decoder.Decode(&feed); err != nil {
		return nil, fmt.Errorf("decoding SEC current filings: %w", err)
	}
	result := make([]CurrentFiling, 0, len(feed.Entries))
	seen := make(map[string]struct{})
	for _, entry := range feed.Entries {
		match := titleCIKPattern.FindStringSubmatch(entry.Title)
		if len(match) != 2 {
			continue
		}
		company, exists := tickers[match[1]]
		if !exists {
			continue
		}
		acceptedAt, err := time.Parse(time.RFC3339, entry.Updated)
		if err != nil {
			continue
		}
		accessionMatch := accessionPattern.FindStringSubmatch(entry.ID)
		if len(accessionMatch) != 2 {
			continue
		}
		formType := strings.ToUpper(strings.TrimSpace(entry.Category.Term))
		key := company.Ticker + ":" + accessionMatch[1]
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, CurrentFiling{
			Ticker:      company.Ticker,
			CIK:         match[1],
			CompanyName: company.Title,
			FormType:    formType,
			AccessionNo: accessionMatch[1],
			AcceptedAt:  acceptedAt.UTC(),
			SourceURL:   entry.Link.Href,
		})
	}
	return result, nil
}

func (client *Client) companyTickers(
	ctx context.Context,
) (map[string]companyTicker, error) {
	client.tickerMu.Lock()
	if len(client.tickersByCIK) > 0 &&
		time.Since(client.tickersLoadedAt) < 24*time.Hour {
		result := client.tickersByCIK
		client.tickerMu.Unlock()
		return result, nil
	}
	client.tickerMu.Unlock()

	endpoint := client.webBaseURL.JoinPath("files", "company_tickers.json")
	var payload map[string]companyTicker
	if err := client.getWebJSON(ctx, endpoint, &payload); err != nil {
		return nil, fmt.Errorf("loading SEC company ticker map: %w", err)
	}
	byCIK := make(map[string]companyTicker, len(payload))
	for _, item := range payload {
		ticker := strings.ToUpper(strings.TrimSpace(item.Ticker))
		if ticker == "" || item.CIK <= 0 {
			continue
		}
		item.Ticker = ticker
		cik := fmt.Sprintf("%010d", item.CIK)
		byCIK[cik] = item
	}
	client.tickerMu.Lock()
	client.tickersByCIK = byCIK
	client.tickersLoadedAt = time.Now().UTC()
	client.tickerMu.Unlock()
	return byCIK, nil
}

func (client *Client) getWebJSON(
	ctx context.Context,
	endpoint *url.URL,
	destination any,
) (returnErr error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		endpoint.String(),
		nil,
	)
	if err != nil {
		return fmt.Errorf("creating SEC web request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", client.userAgent)
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("sending SEC web request: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("closing SEC web response: %w", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("SEC web endpoint returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(
		io.LimitReader(response.Body, maxResponseSize),
	).Decode(destination); err != nil {
		return fmt.Errorf("decoding SEC web response: %w", err)
	}
	return nil
}

// Document fetches a primary EDGAR filing document and converts HTML to
// bounded plain text suitable for downstream filing analysis.
func (client *Client) Document(
	ctx context.Context,
	rawURL string,
) (_ string, returnErr error) {
	endpoint, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || endpoint.Scheme != "https" ||
		(endpoint.Hostname() != "www.sec.gov" &&
			!strings.HasSuffix(endpoint.Hostname(), ".sec.gov")) {
		return "", errors.New("SEC document URL must use HTTPS on sec.gov")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", fmt.Errorf("creating SEC document request: %w", err)
	}
	request.Header.Set("Accept", "text/html,text/plain,application/xhtml+xml")
	request.Header.Set("User-Agent", client.userAgent)
	response, err := client.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("sending SEC document request: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("closing SEC document response: %w", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SEC document returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDocumentSize+1))
	if err != nil {
		return "", fmt.Errorf("reading SEC document: %w", err)
	}
	if len(body) > maxDocumentSize {
		return "", errors.New("SEC document exceeds 16 MiB safety limit")
	}
	text := html.UnescapeString(htmlTagPattern.ReplaceAllString(string(body), " "))
	text = whitespacePattern.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("SEC document contains no readable text")
	}
	return text, nil
}

// RelevantExcerpts preserves the beginning and end of a filing plus bounded
// windows around capital-structure terms, instead of silently prefix-truncating
// long filings.
func RelevantExcerpts(text string, maximum int) string {
	text = strings.ToValidUTF8(strings.TrimSpace(text), " ")
	if maximum < 1 || len(text) <= maximum {
		return text
	}
	headSize := min(16_000, maximum/5)
	windowRadius := min(5_000, maximum/10)
	tailReserve := min(12_000, maximum/5)
	builder := strings.Builder{}
	builder.Grow(maximum)
	builder.WriteString(text[:min(headSize, len(text))])
	lower := strings.ToLower(text)
	keywords := []string{
		"at-the-market", "shelf registration", "public offering", "private placement",
		"dilution", "warrant", "convertible", "reverse split",
		"authorized shares", "beneficial ownership",
	}
	for _, keyword := range keywords {
		searchFrom := 0
		for occurrence := 0; occurrence < 2; occurrence++ {
			found := strings.Index(lower[searchFrom:], keyword)
			if found < 0 {
				break
			}
			found += searchFrom
			start := max(0, found-windowRadius)
			end := min(len(text), found+len(keyword)+windowRadius)
			section := "\n\n[excerpt: " + keyword + "]\n" + text[start:end]
			if builder.Len()+len(section)+tailReserve > maximum {
				break
			}
			builder.WriteString(section)
			searchFrom = end
		}
	}
	tailBudget := maximum - builder.Len()
	const tailLabel = "\n\n[filing tail]\n"
	if tailBudget > len(tailLabel) {
		tailStart := max(0, len(text)-(tailBudget-len(tailLabel)))
		builder.WriteString(tailLabel)
		builder.WriteString(text[tailStart:])
	}
	result := builder.String()
	if len(result) > maximum {
		result = result[:maximum]
	}
	return strings.ToValidUTF8(result, " ")
}

func mapFilings(
	cik string,
	asOf time.Time,
	recent recentFilings,
) ([]intelligence.Filing, error) {
	count := min(
		len(recent.AccessionNumber),
		len(recent.FilingDate),
		len(recent.Form),
	)
	filings := make([]intelligence.Filing, 0, count)
	for index := range count {
		filedAt, err := time.Parse(time.DateOnly, recent.FilingDate[index])
		if err != nil {
			return nil, fmt.Errorf("parsing SEC filing date: %w", err)
		}
		if filedAt.After(asOf) {
			continue
		}
		accession := recent.AccessionNumber[index]
		sourceURL := "https://www.sec.gov/Archives/edgar/data/" +
			strings.TrimLeft(cik, "0") + "/" +
			strings.ReplaceAll(accession, "-", "")
		if index < len(recent.PrimaryDocument) &&
			strings.TrimSpace(recent.PrimaryDocument[index]) != "" {
			segments := strings.Split(recent.PrimaryDocument[index], "/")
			safeSegments := make([]string, 0, len(segments))
			for _, segment := range segments {
				segment = strings.TrimSpace(segment)
				if segment == "" || segment == "." || segment == ".." {
					continue
				}
				safeSegments = append(safeSegments, url.PathEscape(segment))
			}
			if len(safeSegments) > 0 {
				sourceURL += "/" + strings.Join(safeSegments, "/")
			}
		}
		filings = append(filings, intelligence.Filing{
			AccessionNo: accession, FiledAt: filedAt,
			FormType:  recent.Form[index],
			SourceURL: sourceURL,
		})
	}
	return filings, nil
}

type submissions struct {
	Filings struct {
		Recent recentFilings `json:"recent"`
		Files  []struct {
			Name string `json:"name"`
			From string `json:"filingFrom"`
		} `json:"files"`
	} `json:"filings"`
}

type recentFilings struct {
	AccessionNumber []string `json:"accessionNumber"`
	FilingDate      []string `json:"filingDate"`
	Form            []string `json:"form"`
	PrimaryDocument []string `json:"primaryDocument"`
}

type companyTicker struct {
	CIK    int64  `json:"cik_str"`
	Ticker string `json:"ticker"`
	Title  string `json:"title"`
}

type currentFeed struct {
	Entries []struct {
		Title   string `xml:"title"`
		Updated string `xml:"updated"`
		ID      string `xml:"id"`
		Link    struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Category struct {
			Term string `xml:"term,attr"`
		} `xml:"category"`
	} `xml:"entry"`
}
