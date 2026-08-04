package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/llm"
)

func TestClientAnalyzeUsesResponsesAPIAndReadsNestedText(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/v1/responses" ||
			request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected request: %s headers=%v", request.URL, request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"output":[{"content":[{"text":"Catalyst is credible; uncertainty remains."}]}]}`,
		))
	}))
	defer server.Close()

	client, err := llm.NewClient(server.URL+"/v1", "test-key", "test-model", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.Analyze(context.Background(), llm.WorkflowNews, "aapl", "headline")
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if !strings.Contains(result.Text, "credible") || result.Model != "test-model" {
		t.Fatalf("result = %+v", result)
	}
}

func TestClientRejectsTradingWorkflow(t *testing.T) {
	t.Parallel()
	client, err := llm.NewClient(
		"https://example.com/v1", "key", "model", http.DefaultClient,
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Analyze(
		context.Background(), llm.Workflow("trade"), "AAPL", "source",
	); err == nil {
		t.Fatal("Analyze() error = nil")
	}
}
