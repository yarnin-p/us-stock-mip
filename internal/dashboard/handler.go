package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

const maximumRequestBody = 1 << 20

var tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

type Options struct {
	AllowedOrigin      string
	Logger             *slog.Logger
	Events             EventSource
	RedisPing          func(context.Context) error
	Execution          *execution.Service
	LiveEntriesEnabled bool
	StrategyPlans      func() []strategy.Plan
	SpikeWatcher       SpikeWatcher
	NewsCatalysts      NewsCatalystSource
	RuntimeHealth      func() []ComponentHealth
	TicketLimits       TicketLimitSource
}

type Handler struct {
	repository         Repository
	allowedOrigin      string
	logger             *slog.Logger
	mux                *http.ServeMux
	events             EventSource
	redisPing          func(context.Context) error
	execution          *execution.Service
	liveEntriesEnabled bool
	strategyPlans      func() []strategy.Plan
	spikeWatcher       SpikeWatcher
	newsCatalysts      NewsCatalystSource
	runtimeHealth      func() []ComponentHealth
	ticketLimits       TicketLimitSource
}

func NewHandler(repository Repository, options Options) *Handler {
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	handler := &Handler{
		repository: repository, allowedOrigin: options.AllowedOrigin,
		logger: logger, mux: http.NewServeMux(), events: options.Events,
		redisPing: options.RedisPing, execution: options.Execution,
		liveEntriesEnabled: options.LiveEntriesEnabled,
		strategyPlans:      options.StrategyPlans,
		spikeWatcher:       options.SpikeWatcher,
		newsCatalysts:      options.NewsCatalysts,
		runtimeHealth:      options.RuntimeHealth,
		ticketLimits:       options.TicketLimits,
	}
	handler.mux.HandleFunc("GET /healthz", handler.health)
	handler.mux.HandleFunc("GET /scan", handler.scan)
	handler.mux.HandleFunc("GET /candidates", handler.candidates)
	handler.mux.HandleFunc("GET /watchlist", handler.watchlist)
	handler.mux.HandleFunc("POST /watchlist", handler.upsertWatchlist)
	handler.mux.HandleFunc("DELETE /watchlist/{ticker}", handler.deleteWatchlist)
	handler.mux.HandleFunc("GET /positions", handler.positions)
	handler.mux.HandleFunc("GET /trades", handler.trades)
	handler.mux.HandleFunc("POST /trades", handler.createTrade)
	handler.mux.HandleFunc("POST /trades/{id}/events", handler.applyTradeEvent)
	handler.mux.HandleFunc("GET /score-history/{ticker}", handler.scoreHistory)
	handler.mux.HandleFunc("GET /alerts", handler.alerts)
	handler.mux.HandleFunc("POST /alerts/{id}/acknowledge", handler.acknowledgeAlert)
	handler.mux.HandleFunc("GET /system-health", handler.systemHealth)
	handler.mux.HandleFunc("GET /broker-positions", handler.brokerPositions)
	handler.mux.HandleFunc("GET /broker-orders", handler.brokerOrders)
	handler.mux.HandleFunc("GET /learning-report", handler.learningReport)
	// Sizing and validation only. Submission stays on the execution path so a
	// manual ticket cannot bypass approval or the kill switch.
	handler.mux.HandleFunc("POST /ticket/preview", handler.previewTicket)
	if handler.spikeWatcher != nil {
		handler.mux.HandleFunc("GET /spike-watch", handler.spikeWatch)
	}
	if handler.newsCatalysts != nil {
		handler.mux.HandleFunc("GET /news-catalysts", handler.newsCatalystList)
	}
	handler.mux.HandleFunc("GET /events", handler.eventStream)
	if handler.execution != nil {
		handler.mux.HandleFunc("GET /execution/config", handler.executionConfig)
		handler.mux.HandleFunc("POST /execution/size", handler.executionSize)
		handler.mux.HandleFunc("GET /execution/orders", handler.executionOrders)
		handler.mux.HandleFunc("POST /execution/orders", handler.createExecutionOrder)
		handler.mux.HandleFunc(
			"GET /execution/orders/{id}/transitions",
			handler.executionTransitions,
		)
		handler.mux.HandleFunc(
			"POST /execution/orders/{id}/preview", handler.previewExecutionOrder,
		)
		handler.mux.HandleFunc(
			"POST /execution/orders/{id}/approve", handler.approveExecutionOrder,
		)
		handler.mux.HandleFunc(
			"POST /execution/orders/{id}/submit", handler.submitExecutionOrder,
		)
		handler.mux.HandleFunc(
			"POST /execution/orders/{id}/cancel", handler.cancelExecutionOrder,
		)
		handler.mux.HandleFunc(
			"GET /execution/positions", handler.executionPositions,
		)
		handler.mux.HandleFunc(
			"GET /execution/transactions", handler.executionTransactions,
		)
		handler.mux.HandleFunc(
			"GET /execution/daily-pnl", handler.executionDailyPnL,
		)
	}
	if handler.strategyPlans != nil {
		handler.mux.HandleFunc("GET /strategy/plans", handler.strategyPlanList)
	}
	return handler
}

func (handler *Handler) executionConfig(
	response http.ResponseWriter, _ *http.Request,
) {
	writeJSON(response, http.StatusOK, map[string]any{
		"mode":                 handler.execution.Mode(),
		"automatic_trading":    handler.execution.AutomaticTradingEnabled(),
		"approval_required":    !handler.execution.AutomaticTradingEnabled(),
		"kill_switch":          handler.execution.KillSwitchActive(),
		"allowed_sessions":     handler.execution.AllowedSessions(),
		"live_entries_enabled": handler.liveEntriesEnabled,
	})
}

func (handler *Handler) executionSize(
	response http.ResponseWriter, request *http.Request,
) {
	var input execution.SizingInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	result, err := handler.execution.Size(request.Context(), input)
	if err != nil {
		handler.executionError(response, err, http.StatusBadRequest)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *Handler) executionOrders(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.execution.Orders(request.Context())
	handler.writeExecutionResult(response, data, err)
}

func (handler *Handler) executionPositions(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.execution.Positions(request.Context())
	handler.writeExecutionResult(response, data, err)
}

func (handler *Handler) executionTransactions(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.execution.Transactions(request.Context())
	handler.writeExecutionResult(response, data, err)
}

func (handler *Handler) executionDailyPnL(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.execution.DailyPnL(request.Context())
	handler.writeExecutionResult(response, data, err)
}

func (handler *Handler) strategyPlanList(
	response http.ResponseWriter, _ *http.Request,
) {
	writeJSON(response, http.StatusOK, map[string]any{
		"data": handler.strategyPlans(), "generated_at": time.Now().UTC(),
	})
}

func (handler *Handler) executionTransitions(
	response http.ResponseWriter, request *http.Request,
) {
	id, ok := executionOrderID(response, request)
	if !ok {
		return
	}
	data, err := handler.execution.Transitions(request.Context(), id)
	handler.writeExecutionResult(response, data, err)
}

func (handler *Handler) createExecutionOrder(
	response http.ResponseWriter, request *http.Request,
) {
	var input execution.CreateOrderInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	order, err := handler.execution.Create(request.Context(), input)
	if err != nil {
		handler.executionError(response, err, http.StatusBadRequest)
		return
	}
	writeJSON(response, http.StatusCreated, order)
}

func (handler *Handler) previewExecutionOrder(
	response http.ResponseWriter, request *http.Request,
) {
	id, ok := executionOrderID(response, request)
	if !ok {
		return
	}
	order, err := handler.execution.Preview(request.Context(), id)
	if err != nil {
		handler.executionError(response, err, http.StatusBadGateway)
		return
	}
	writeJSON(response, http.StatusOK, order)
}

func (handler *Handler) approveExecutionOrder(
	response http.ResponseWriter, request *http.Request,
) {
	id, ok := executionOrderID(response, request)
	if !ok {
		return
	}
	approval, err := handler.execution.Approve(request.Context(), id)
	if err != nil {
		handler.executionError(response, err, http.StatusInternalServerError)
		return
	}
	writeJSON(response, http.StatusOK, approval)
}

func (handler *Handler) submitExecutionOrder(
	response http.ResponseWriter, request *http.Request,
) {
	id, ok := executionOrderID(response, request)
	if !ok {
		return
	}
	var input struct {
		ConfirmationToken string `json:"confirmation_token"`
		ConfirmationText  string `json:"confirmation_text"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	order, err := handler.execution.Submit(
		request.Context(), id, input.ConfirmationToken, input.ConfirmationText,
	)
	if err != nil {
		handler.executionError(response, err, http.StatusBadGateway)
		return
	}
	writeJSON(response, http.StatusOK, order)
}

func (handler *Handler) cancelExecutionOrder(
	response http.ResponseWriter, request *http.Request,
) {
	id, ok := executionOrderID(response, request)
	if !ok {
		return
	}
	order, err := handler.execution.Cancel(request.Context(), id)
	if err != nil {
		handler.executionError(response, err, http.StatusBadGateway)
		return
	}
	writeJSON(response, http.StatusOK, order)
}

func (handler *Handler) writeExecutionResult(
	response http.ResponseWriter, data any, err error,
) {
	if err != nil {
		handler.executionError(response, err, http.StatusInternalServerError)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"data": data, "generated_at": time.Now().UTC(),
	})
}

func (handler *Handler) executionError(
	response http.ResponseWriter, err error, fallback int,
) {
	status, message := fallback, "execution request failed"
	switch {
	case errors.Is(err, execution.ErrNotFound):
		status, message = http.StatusNotFound, "execution order not found"
	case errors.Is(err, execution.ErrConflict):
		status, message = http.StatusConflict, "order is not valid for this action"
	case errors.Is(err, execution.ErrApprovalRequired):
		status, message = http.StatusForbidden, "valid human approval is required"
	default:
		handler.logger.Error("execution request failed", "error", err)
	}
	writeAPIError(response, status, message)
}

func executionOrderID(
	response http.ResponseWriter, request *http.Request,
) (int64, bool) {
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(response, http.StatusBadRequest, "invalid execution order id")
		return 0, false
	}
	return id, true
}

func (handler *Handler) brokerOrders(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.repository.BrokerOrders(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) brokerPositions(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.repository.BrokerPositions(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) learningReport(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.repository.LearningReport(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) spikeWatch(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.spikeWatcher.SpikeWatch(request.Context())
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"data": data, "generated_at": time.Now().UTC(),
	})
}

func (handler *Handler) newsCatalystList(
	response http.ResponseWriter, request *http.Request,
) {
	hours, ok := boundedQueryInteger(
		response, request, "hours", 24, 1, 168,
	)
	if !ok {
		return
	}
	limit, ok := boundedQueryInteger(
		response, request, "limit", 100, 1, 200,
	)
	if !ok {
		return
	}
	data, err := handler.newsCatalysts.NewsCatalysts(
		request.Context(),
		time.Now().UTC(),
		time.Duration(hours)*time.Hour,
		limit,
	)
	handler.writeRepositoryResult(response, data, err)
}

func boundedQueryInteger(
	response http.ResponseWriter,
	request *http.Request,
	name string,
	fallback, minimum, maximum int,
) (int, bool) {
	value := fallback
	if raw := strings.TrimSpace(request.URL.Query().Get(name)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeAPIError(
				response,
				http.StatusBadRequest,
				fmt.Sprintf("%s must be an integer", name),
			)
			return 0, false
		}
		value = parsed
	}
	if value < minimum || value > maximum {
		writeAPIError(
			response,
			http.StatusBadRequest,
			fmt.Sprintf("%s must be between %d and %d", name, minimum, maximum),
		)
		return 0, false
	}
	return value, true
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if origin := allowedCORSOrigin(
		handler.allowedOrigin,
		request.Header.Get("Origin"),
	); origin != "" {
		response.Header().Set("Access-Control-Allow-Origin", origin)
		response.Header().Set("Vary", "Origin")
		response.Header().Set(
			"Access-Control-Allow-Headers",
			"Content-Type, Accept",
		)
		response.Header().Set(
			"Access-Control-Allow-Methods",
			"GET, POST, DELETE, OPTIONS",
		)
	}
	if request.Method == http.MethodOptions {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	handler.mux.ServeHTTP(response, request)
}

func allowedCORSOrigin(configured, requested string) string {
	origins := strings.Split(configured, ",")
	if len(origins) == 1 {
		return strings.TrimSpace(configured)
	}
	requested = strings.TrimSpace(requested)
	for _, origin := range origins {
		if requested != "" && requested == strings.TrimSpace(origin) {
			return requested
		}
	}
	return ""
}

func (handler *Handler) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) systemHealth(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.repository.SystemHealth(request.Context())
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	now := time.Now().UTC()
	data.Components = append(data.Components, ComponentHealth{
		Name: "API", Status: "CONNECTED", Detail: "Dashboard API",
		LastUpdate: &now,
	})
	if handler.redisPing != nil {
		started := time.Now()
		status, detail := "CONNECTED", "Redis"
		if err := handler.redisPing(request.Context()); err != nil {
			status, detail = "DISCONNECTED", "Redis unavailable"
		}
		latency := time.Since(started).Milliseconds()
		data.Components = append(data.Components, ComponentHealth{
			Name: "Redis", Status: status, Detail: detail,
			LastUpdate: &now, LatencyMS: &latency,
		})
	} else {
		data.Components = append(data.Components, ComponentHealth{
			Name: "Redis", Status: "DISABLED", Detail: "Cache not configured",
		})
	}
	if handler.runtimeHealth != nil {
		data.Components = append(
			data.Components,
			handler.runtimeHealth()...,
		)
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"data": data, "generated_at": now,
	})
}

func (handler *Handler) scoreHistory(
	response http.ResponseWriter, request *http.Request,
) {
	ticker := normalizeTicker(request.PathValue("ticker"))
	if !tickerPattern.MatchString(ticker) {
		writeAPIError(response, http.StatusBadRequest, "invalid ticker")
		return
	}
	data, err := handler.repository.ScoreHistory(request.Context(), ticker)
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) alerts(response http.ResponseWriter, request *http.Request) {
	data, err := handler.repository.Alerts(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) acknowledgeAlert(
	response http.ResponseWriter, request *http.Request,
) {
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(response, http.StatusBadRequest, "invalid alert id")
		return
	}
	if err := handler.repository.AcknowledgeAlert(request.Context(), id); err != nil {
		handler.repositoryError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) eventStream(
	response http.ResponseWriter, request *http.Request,
) {
	if handler.events == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "event stream unavailable")
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeAPIError(response, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	events, err := handler.events.Events(request.Context())
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache, no-transform")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	writeSSE(response, "ready", Event{
		Scope: "candidates,scan,watchlist,positions,trades,score_history," +
			"alerts,health,execution,strategy,spike_watch",
		OccurredAt: time.Now().UTC(),
	})
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(response, ": heartbeat\n\n")
			flusher.Flush()
		case event, open := <-events:
			if !open {
				return
			}
			writeSSE(response, "change", event)
			flusher.Flush()
		}
	}
}

func writeSSE(response io.Writer, eventName string, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", eventName, encoded)
}

func (handler *Handler) candidates(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.repository.Candidates(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) scan(response http.ResponseWriter, request *http.Request) {
	data, err := handler.repository.Scan(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) watchlist(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.repository.Watchlist(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) positions(
	response http.ResponseWriter, request *http.Request,
) {
	data, err := handler.repository.Positions(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) trades(response http.ResponseWriter, request *http.Request) {
	data, err := handler.repository.Trades(request.Context())
	handler.writeRepositoryResult(response, data, err)
}

func (handler *Handler) upsertWatchlist(
	response http.ResponseWriter, request *http.Request,
) {
	var input WatchlistInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.Ticker = normalizeTicker(input.Ticker)
	input.Thesis = strings.TrimSpace(input.Thesis)
	if !tickerPattern.MatchString(input.Ticker) {
		writeAPIError(response, http.StatusBadRequest, "invalid ticker")
		return
	}
	item, err := handler.repository.UpsertWatchlist(request.Context(), input)
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, item)
}

func (handler *Handler) deleteWatchlist(
	response http.ResponseWriter, request *http.Request,
) {
	ticker := normalizeTicker(request.PathValue("ticker"))
	if !tickerPattern.MatchString(ticker) {
		writeAPIError(response, http.StatusBadRequest, "invalid ticker")
		return
	}
	if err := handler.repository.DeleteWatchlist(request.Context(), ticker); err != nil {
		handler.repositoryError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) createTrade(
	response http.ResponseWriter, request *http.Request,
) {
	var input TradeInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.Ticker = normalizeTicker(input.Ticker)
	input.Side = strings.ToUpper(strings.TrimSpace(input.Side))
	input.Strategy = strings.TrimSpace(input.Strategy)
	input.Notes = strings.TrimSpace(input.Notes)
	if input.Side == "" {
		input.Side = "LONG"
	}
	if input.EnteredAt.IsZero() {
		input.EnteredAt = time.Now().UTC()
	}
	switch {
	case !tickerPattern.MatchString(input.Ticker):
		writeAPIError(response, http.StatusBadRequest, "invalid ticker")
		return
	case input.Side != "LONG" && input.Side != "SHORT":
		writeAPIError(response, http.StatusBadRequest, "side must be LONG or SHORT")
		return
	case input.Quantity <= 0:
		writeAPIError(response, http.StatusBadRequest, "quantity must be positive")
		return
	case input.EntryPrice <= 0:
		writeAPIError(response, http.StatusBadRequest, "entry_price must be positive")
		return
	case input.Strategy == "":
		writeAPIError(response, http.StatusBadRequest, "strategy is required")
		return
	case input.Fees < 0:
		writeAPIError(response, http.StatusBadRequest, "fees must not be negative")
		return
	}
	trade, err := handler.repository.CreateTrade(request.Context(), input)
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, trade)
}

func (handler *Handler) applyTradeEvent(
	response http.ResponseWriter, request *http.Request,
) {
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(response, http.StatusBadRequest, "invalid trade id")
		return
	}
	var input TradeEventInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	input.Type = strings.ToUpper(strings.TrimSpace(input.Type))
	input.Notes = strings.TrimSpace(input.Notes)
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now().UTC()
	}
	switch {
	case input.Type != "SCALE_IN" &&
		input.Type != "PARTIAL_EXIT" &&
		input.Type != "FULL_EXIT":
		writeAPIError(
			response, http.StatusBadRequest,
			"type must be SCALE_IN, PARTIAL_EXIT, or FULL_EXIT",
		)
		return
	case input.Quantity <= 0:
		writeAPIError(response, http.StatusBadRequest, "quantity must be positive")
		return
	case input.Price <= 0:
		writeAPIError(response, http.StatusBadRequest, "price must be positive")
		return
	case input.Fees < 0:
		writeAPIError(response, http.StatusBadRequest, "fees must not be negative")
		return
	}
	trade, err := handler.repository.ApplyTradeEvent(request.Context(), id, input)
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, trade)
}

func (handler *Handler) writeRepositoryResult(
	response http.ResponseWriter, data any, err error,
) {
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"data": data, "generated_at": time.Now().UTC(),
	})
}

func (handler *Handler) repositoryError(
	response http.ResponseWriter, err error,
) {
	handler.logger.Error("dashboard repository request failed", "error", err)
	writeAPIError(response, http.StatusInternalServerError, "internal server error")
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(response, request.Body, maximumRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON body")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func normalizeTicker(ticker string) string {
	return strings.ToUpper(strings.TrimSpace(ticker))
}

func writeAPIError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		slog.Default().Error("encoding dashboard response", "error", err)
	}
}
