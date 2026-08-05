package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/ticket"
)

func ticketHandler(t *testing.T, limits ticket.Limits) *Handler {
	t.Helper()
	return NewHandler(&fakeRepository{}, Options{
		TicketLimits: func() (ticket.Limits, error) { return limits, nil },
	})
}

func postTicket(
	t *testing.T,
	handler *Handler,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost, "/ticket/preview", bytes.NewReader(payload),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestPreviewTicketSizesFromRisk(t *testing.T) {
	handler := ticketHandler(t, ticket.Limits{
		MaxRiskPerTicket: 2000, MaxNotional: 40000,
		MaxStopDistance: 0.10, MaxDailyLoss: 6000, MaxTicketsPerDay: 6,
	})
	recorder := postTicket(t, handler, map[string]any{
		"ticker": "abcd", "side": "BUY",
		"entry": 10.0, "stop": 9.5, "target": 11.5, "risk_amount": 2000.0,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var got struct {
		Ticket ticket.Ticket `json:"ticket"`
		Mode   string        `json:"mode"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Ticket.Shares != 4000 || got.Ticket.RewardRisk != 3 {
		t.Fatalf("ticket = %#v", got.Ticket)
	}
	// The screen must never imply a live order while the system is on paper.
	if got.Mode == "" {
		t.Fatal("the response must state which mode the ticket belongs to")
	}
}

// A ticket over a ceiling is the ordinary case this endpoint exists for, so it
// answers with the ceiling it crossed rather than a server error.
func TestPreviewTicketExplainsWhyItRefused(t *testing.T) {
	handler := ticketHandler(t, ticket.Limits{MaxRiskPerTicket: 500})
	recorder := postTicket(t, handler, map[string]any{
		"ticker": "ABCD", "side": "BUY",
		"entry": 10.0, "stop": 9.5, "risk_amount": 5000.0,
	})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["error"] == "" {
		t.Fatal("a refusal must name the ceiling it crossed")
	}
}

// A ticket with no stop must never be sized: the stop is the leg that never
// gets placed in time by hand, which is the reason this path exists.
func TestPreviewTicketRefusesAMissingStop(t *testing.T) {
	handler := ticketHandler(t, ticket.Limits{MaxRiskPerTicket: 2000})
	recorder := postTicket(t, handler, map[string]any{
		"ticker": "ABCD", "side": "BUY", "entry": 10.0, "risk_amount": 500.0,
	})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
}

func TestPreviewTicketNeedsLimitsConfigured(t *testing.T) {
	handler := NewHandler(&fakeRepository{}, Options{})
	recorder := postTicket(t, handler, map[string]any{
		"ticker": "ABCD", "side": "BUY",
		"entry": 10.0, "stop": 9.5, "risk_amount": 500.0,
	})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no ceilings are configured",
			recorder.Code)
	}
}
