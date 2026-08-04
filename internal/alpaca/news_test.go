package alpaca_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/momentum-intelligence-platform/mip/internal/alpaca"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

const (
	testAPIKeyID = "paper-key"
	testSecret   = "paper-secret"
)

func TestNewsClient_LatestMarketNewsMapsEveryEquitySymbol(t *testing.T) {
	t.Parallel()

	publishedAt := time.Date(2026, 7, 31, 19, 55, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if got := request.Header.Get("APCA-API-KEY-ID"); got != testAPIKeyID {
			t.Errorf("API key header = %q", got)
		}
		if got := request.Header.Get("APCA-API-SECRET-KEY"); got != testSecret {
			t.Errorf("API secret header = %q", got)
		}
		if request.URL.Path != "/v1beta1/news" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("include_content") != "true" {
			t.Errorf("include_content = %q", request.URL.Query().Get("include_content"))
		}
		if request.URL.Query().Get("limit") != "1" {
			t.Errorf("limit = %q, want one article", request.URL.Query().Get("limit"))
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"news": []map[string]any{{
				"id":         42,
				"headline":   "Company wins material contract",
				"summary":    "Expected to increase annual revenue.",
				"content":    "<p>Contract value is $50 million.</p>",
				"author":     "Benzinga Newsdesk",
				"created_at": publishedAt.Format(time.RFC3339),
				"updated_at": publishedAt.Add(time.Second).Format(time.RFC3339),
				"url":        "https://example.com/news/42",
				"symbols":    []string{"aapl", "TSLA", "BTCUSD", "bad symbol"},
				"source":     "benzinga",
				"images":     []any{},
			}},
			"next_page_token": nil,
		})
	}))
	defer server.Close()

	client, err := alpaca.NewNewsClient(
		testAPIKeyID,
		testSecret,
		alpaca.WithRESTBaseURL(server.URL),
		alpaca.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}

	items, err := client.LatestMarketNews(
		context.Background(),
		publishedAt.Add(-time.Hour),
		publishedAt.Add(time.Hour),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v, want two stock symbols", items)
	}
	for index, ticker := range []string{"AAPL", "TSLA"} {
		item := items[index]
		if item.Ticker != ticker {
			t.Errorf("item[%d].Ticker = %q, want %q", index, item.Ticker, ticker)
		}
		if item.News.ExternalID != "alpaca:42" {
			t.Errorf("ExternalID = %q", item.News.ExternalID)
		}
		if !item.News.PublishedAt.Equal(publishedAt) {
			t.Errorf("PublishedAt = %s", item.News.PublishedAt)
		}
		if !strings.Contains(item.News.Description, "Contract value") {
			t.Errorf("Description = %q", item.News.Description)
		}
	}
}

func TestNewsClient_StreamSubscribesToAllNewsAndStopsWithContext(
	t *testing.T,
) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("APCA-API-KEY-ID") != testAPIKeyID ||
			request.Header.Get("APCA-API-SECRET-KEY") != testSecret {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		connection, err := upgrader.Upgrade(response, request, nil)
		if err != nil {
			t.Errorf("upgrading websocket: %v", err)
			return
		}
		defer connection.Close()
		for _, message := range []string{
			`[{"T":"success","msg":"connected"}]`,
			`[{"T":"success","msg":"authenticated"}]`,
		} {
			if err := connection.WriteMessage(
				websocket.TextMessage,
				[]byte(message),
			); err != nil {
				return
			}
		}
		var subscription struct {
			Action string   `json:"action"`
			News   []string `json:"news"`
		}
		if err := connection.ReadJSON(&subscription); err != nil {
			t.Errorf("reading subscription: %v", err)
			return
		}
		if subscription.Action != "subscribe" ||
			len(subscription.News) != 1 ||
			subscription.News[0] != "*" {
			t.Errorf("subscription = %#v", subscription)
		}
		if err := connection.WriteMessage(
			websocket.TextMessage,
			[]byte(`[{"T":"subscription","news":["*"]}]`),
		); err != nil {
			return
		}
		_ = connection.WriteMessage(
			websocket.TextMessage,
			[]byte(`[{"T":"n","id":99,"headline":"FDA grants approval","summary":"Approved today.","author":"Benzinga Newsdesk","created_at":"2026-07-31T19:59:00Z","updated_at":"2026-07-31T19:59:01Z","content":"FDA approved the treatment.","url":"https://example.com/news/99","symbols":["KUST"],"source":"benzinga"}]`),
		)
		_, _, _ = connection.ReadMessage()
	}))
	defer server.Close()

	streamURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client, err := alpaca.NewNewsClient(
		testAPIKeyID,
		testSecret,
		alpaca.WithStreamURL(streamURL),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan []intelligence.TickerNewsItem, 1)
	ready := make(chan struct{}, 1)
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- client.StreamWithReady(
			ctx,
			func(
				_ context.Context,
				items []intelligence.TickerNewsItem,
			) error {
				received <- items
				cancel()
				return nil
			},
			func() {
				ready <- struct{}{}
			},
		)
	}()

	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("stream did not report subscription readiness")
	}
	select {
	case items := <-received:
		if len(items) != 1 || items[0].Ticker != "KUST" {
			t.Fatalf("items = %#v", items)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for streamed news")
	}
	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after context cancellation")
	}
}

func TestNewNewsClient_RejectsIncompleteCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		keyID  string
		secret string
	}{
		{name: "missing key id", secret: testSecret},
		{name: "missing secret", keyID: testAPIKeyID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := alpaca.NewNewsClient(
				test.keyID,
				test.secret,
			); err == nil {
				t.Fatal("NewNewsClient() error = nil")
			}
		})
	}
}
