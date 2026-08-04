package sec_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/sec"
)

func TestClient_CurrentFilingsMapsSubjectCIKToTicker(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/files/company_tickers.json":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"0": map[string]any{
					"cik_str": 1342958,
					"ticker":  "KUST",
					"title":   "Kustom Entertainment, Inc.",
				},
			})
		case "/cgi-bin/browse-edgar":
			writer.Header().Set("Content-Type", "application/atom+xml")
			_, _ = writer.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>SCHEDULE 13D - KUSTOM ENTERTAINMENT, INC. (0001342958) (Subject)</title>
    <link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/1342958/000214802926000001/index.htm"/>
    <updated>2026-07-30T15:16:00-04:00</updated>
    <category label="form type" term="SCHEDULE 13D"/>
    <id>urn:tag:sec.gov,2008:accession-number=0002148029-26-000001</id>
  </entry>
</feed>`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := sec.NewClient(
		"mip test@example.com",
		server.Client(),
		sec.WithBaseURL(server.URL),
		sec.WithWebBaseURL(server.URL),
	)
	if err != nil {
		t.Fatal(err)
	}

	filings, err := client.CurrentFilings(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(filings) != 1 {
		t.Fatalf("filings = %+v", filings)
	}
	if filings[0].Ticker != "KUST" ||
		filings[0].FormType != "SCHEDULE 13D" ||
		filings[0].AccessionNo != "0002148029-26-000001" {
		t.Fatalf("filing = %+v", filings[0])
	}
}

func TestFilingsUsesUserAgentAndPointInTimeFilter(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("User-Agent") != "Researcher research@example.com" {
			t.Errorf("user agent = %q", request.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte(`{"filings":{"recent":{
			"accessionNumber":["0001-26-000001","0001-27-000001"],
			"filingDate":["2026-01-01","2027-01-01"],"form":["S-3","10-K"],
			"primaryDocument":["xsl/offering.htm","annual.htm"]}}}`))
	}))
	defer server.Close()

	client, err := sec.NewClient(
		"Researcher research@example.com",
		server.Client(),
		sec.WithBaseURL(server.URL),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Filings(context.Background(), "123", time.Now()); err == nil {
		t.Fatal("expected invalid CIK error")
	}
	filings, err := client.Filings(
		context.Background(),
		"0000320193",
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(filings) != 1 || filings[0].FormType != "S-3" {
		t.Fatalf("filings = %+v", filings)
	}
	if filings[0].SourceURL !=
		"https://www.sec.gov/Archives/edgar/data/320193/000126000001/xsl/offering.htm" {
		t.Fatalf("source URL = %q", filings[0].SourceURL)
	}
}

func TestRelevantExcerptsKeepsCapitalStructureEvidence(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("intro ", 5_000) +
		"material at-the-market offering and warrant dilution terms " +
		strings.Repeat("tail ", 5_000)
	excerpt := sec.RelevantExcerpts(text, 20_000)
	if len(excerpt) > 20_000 ||
		!strings.Contains(excerpt, "at-the-market offering") ||
		!strings.Contains(excerpt, "[filing tail]") {
		t.Fatalf("unexpected excerpt length/content: %d", len(excerpt))
	}
}
