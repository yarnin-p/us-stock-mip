package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

type executionRepository struct {
	order       execution.Order
	transitions []execution.Transition
	positions   []execution.Position
}

func (repo *executionRepository) RiskSnapshot(
	context.Context, string, execution.Mode, string,
) (execution.RiskSnapshot, error) {
	return execution.RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", BuyingPower: 10000,
		PortfolioEquity: 10000,
	}, nil
}
func (repo *executionRepository) DefaultBrokerAccount(context.Context) (string, error) {
	return "", nil
}
func (repo *executionRepository) CreateExecutionOrder(
	_ context.Context, order execution.Order, transition execution.Transition,
) (execution.Order, error) {
	order.ID = 1
	transition.OrderID = order.ID
	repo.order = order
	repo.transitions = append(repo.transitions, transition)
	return order, nil
}
func (repo *executionRepository) ExecutionOrder(
	context.Context, int64,
) (execution.Order, error) {
	return repo.order, nil
}
func (repo *executionRepository) ExecutionOrders(
	context.Context,
) ([]execution.Order, error) {
	return []execution.Order{repo.order}, nil
}
func (repo *executionRepository) ExecutionTransitions(
	context.Context, int64,
) ([]execution.Transition, error) {
	return repo.transitions, nil
}
func (repo *executionRepository) TransitionExecutionOrder(
	_ context.Context,
	order execution.Order,
	expected execution.State,
	transition execution.Transition,
	fill *execution.Fill,
) (execution.Order, error) {
	if repo.order.State != expected {
		return execution.Order{}, execution.ErrConflict
	}
	transition.OrderID = order.ID
	repo.order = order
	repo.transitions = append(repo.transitions, transition)
	if fill != nil {
		repo.positions = []execution.Position{{
			Mode: order.Mode, Ticker: order.Ticker, Quantity: fill.Quantity,
			AverageCost: fill.Price, CurrentPrice: fill.Price,
		}}
	}
	return order, nil
}
func (repo *executionRepository) ExecutionPositions(
	context.Context,
) ([]execution.Position, error) {
	return repo.positions, nil
}
func (repo *executionRepository) ExecutionTransactions(
	context.Context,
) ([]execution.Transaction, error) {
	return nil, nil
}
func (repo *executionRepository) ExecutionDailyPnL(
	context.Context,
) ([]execution.DailyPnL, error) {
	return nil, nil
}

func TestExecutionHTTPWorkflowRequiresTypedHumanConfirmation(t *testing.T) {
	repository := &executionRepository{}
	service, err := execution.NewService(repository, execution.ServiceOptions{
		Mode: execution.ModePaper,
		Limits: execution.Limits{
			MaxPositionValue: 5000, MaxCapitalAllocation: 0.5,
			MaxDailyLoss: 500, DefaultBuyingPower: 10000,
		},
		Clock: func() time.Time {
			return time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
		},
		Random: func(destination []byte) error {
			for index := range destination {
				destination[index] = byte(index + 1)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(&fakeRepository{}, Options{Execution: service})

	order := executionRequest(t, handler, http.MethodPost, "/execution/orders",
		`{"ticker":"OPK","side":"BUY","quantity":10,"limit_price":1.62}`,
		http.StatusCreated,
	)
	if order.State != execution.StateCreated {
		t.Fatalf("created order = %#v", order)
	}
	order = executionRequest(t, handler, http.MethodPost,
		"/execution/orders/1/preview", "", http.StatusOK,
	)
	if order.State != execution.StatePreviewed {
		t.Fatalf("previewed order = %#v", order)
	}
	request := httptest.NewRequest(
		http.MethodPost, "/execution/orders/1/approve", nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var approval execution.Approval
	if err := json.Unmarshal(recorder.Body.Bytes(), &approval); err != nil {
		t.Fatal(err)
	}
	executionRequest(t, handler, http.MethodPost, "/execution/orders/1/submit",
		`{"confirmation_token":"wrong","confirmation_text":"wrong"}`,
		http.StatusForbidden,
	)
	body, err := json.Marshal(map[string]string{
		"confirmation_token": approval.ConfirmationToken,
		"confirmation_text":  approval.ConfirmationText,
	})
	if err != nil {
		t.Fatal(err)
	}
	order = executionRequest(t, handler, http.MethodPost,
		"/execution/orders/1/submit", string(body), http.StatusOK,
	)
	if order.State != execution.StateFilled {
		t.Fatalf("filled order = %#v", order)
	}
}

func executionRequest(
	t *testing.T,
	handler http.Handler,
	method, path, body string,
	status int,
) execution.Order {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != status {
		t.Fatalf(
			"%s %s status=%d body=%s",
			method, path, recorder.Code, recorder.Body.String(),
		)
	}
	var order execution.Order
	if status < 400 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &order); err != nil {
			t.Fatal(err)
		}
	}
	return order
}
