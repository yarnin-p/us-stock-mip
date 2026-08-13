package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/momentum-intelligence-platform/mip/internal/bracket"
)

// BracketSource is the terminal's view of the bracket service. It is supplied
// through Options rather than added to Repository so the existing test doubles
// keep compiling.
type BracketSource interface {
	List(context.Context, int) ([]bracket.Record, error)
	Get(context.Context, int64) (bracket.Record, error)
	Adjustments(context.Context, int64) ([]bracket.AdjustmentRecord, error)
	Open(context.Context, bracket.OpenInput, string) (bracket.Record, error)
	Amend(context.Context, int64, bracket.AmendInput) (bracket.Record, error)
	Close(context.Context, int64, bracket.State, string) (bracket.Record, error)
	Mode() string
}

// BracketArmer is the part of the bracket service that reaches a broker. It is
// asked for separately because a deployment wired without one can still plan and
// read brackets, and the endpoint should say that plainly rather than fail on a
// nil.
type BracketArmer interface {
	Arm(context.Context, int64, bracket.ArmInput) (bracket.Record, error)
}

// BookDepth reports the shares resting at the best bid for a symbol, which is the
// only thing that says whether a position can be sold at the price a plan assumes.
//
// Optional: without it a preview still sizes from the money and says the depth is
// unknown, which is honest. Silently sizing as though the book were unlimited is not.
type BookDepth interface {
	ExitDepth(ctx context.Context, ticker string) (shares float64, price float64, err error)
}

// previewBracket sizes an intent and states its risk without touching a broker.
// It is a pure calculation, so the terminal can call it on every keystroke.
func (handler *Handler) previewBracket(
	response http.ResponseWriter, request *http.Request,
) {
	var input bracket.OpenInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	// The book is read here, not in the domain: Preview stays a pure function of its
	// input, and the caller that knows how to reach a quote is the one that does.
	// A caller may also pass the depth itself, which is what makes the plan replayable.
	if input.BidShares <= 0 && handler.bookDepth != nil {
		shares, price, depthErr := handler.bookDepth.ExitDepth(
			request.Context(), input.Ticker,
		)
		if depthErr != nil {
			// Not fatal. A preview without depth reports the depth as unknown, which
			// is strictly better than refusing to price the trade at all.
			handler.logger.Warn(
				"could not read the book for a bracket preview; depth will be reported "+
					"as unknown",
				"ticker", input.Ticker, "error", depthErr,
			)
		} else {
			input.BidShares, input.BidPrice = shares, price
		}
	}
	plan, err := bracket.Preview(input)
	if err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (handler *Handler) brackets(
	response http.ResponseWriter, request *http.Request,
) {
	source, ok := handler.requireBrackets(response)
	if !ok {
		return
	}
	limit := 100
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeAPIError(response, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	records, err := source.List(request.Context(), limit)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"mode": source.Mode(), "brackets": records,
	})
}

// bracketDetail returns the bracket with its full adjustment history. The
// history is the point of the screen: when a stop turns out to have been in the
// wrong place, the question is what was known when it moved.
func (handler *Handler) bracketDetail(
	response http.ResponseWriter, request *http.Request,
) {
	source, ok := handler.requireBrackets(response)
	if !ok {
		return
	}
	id, ok := bracketID(response, request)
	if !ok {
		return
	}
	record, err := source.Get(request.Context(), id)
	if err != nil {
		writeAPIError(response, http.StatusNotFound, err.Error())
		return
	}
	adjustments, err := source.Adjustments(request.Context(), id)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"bracket": record, "adjustments": adjustments,
	})
}

// openBracket records the intent in PENDING. It places nothing: the entry order
// goes through the execution path so a bracket cannot bypass the risk gate or
// the kill switch, which is the same reason the manual ticket does not submit
// from here either.
func (handler *Handler) openBracket(
	response http.ResponseWriter, request *http.Request,
) {
	source, ok := handler.requireBrackets(response)
	if !ok {
		return
	}
	var input bracket.OpenInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	record, err := source.Open(request.Context(), input, "")
	if err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, record)
}

func (handler *Handler) amendBracket(
	response http.ResponseWriter, request *http.Request,
) {
	source, ok := handler.requireBrackets(response)
	if !ok {
		return
	}
	id, ok := bracketID(response, request)
	if !ok {
		return
	}
	var body bracket.AmendInput
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	record, err := source.Amend(request.Context(), id, body)
	if err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, record)
}

type closeBracketRequest struct {
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

// armBracket puts the protective orders into the market and turns the bracket on.
// It is a separate call from opening one because the two happen at different
// moments: a plan is written before the entry, and the levels that protect it can
// only be derived from the price that actually filled.
func (handler *Handler) armBracket(
	response http.ResponseWriter, request *http.Request,
) {
	source, ok := handler.requireBrackets(response)
	if !ok {
		return
	}
	id, ok := bracketID(response, request)
	if !ok {
		return
	}
	armer, ok := source.(BracketArmer)
	if !ok {
		writeAPIError(
			response, http.StatusNotImplemented,
			"this deployment cannot place protective orders",
		)
		return
	}
	var body bracket.ArmInput
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	record, err := armer.Arm(request.Context(), id, body)
	if err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, record)
}

func (handler *Handler) closeBracket(
	response http.ResponseWriter, request *http.Request,
) {
	source, ok := handler.requireBrackets(response)
	if !ok {
		return
	}
	id, ok := bracketID(response, request)
	if !ok {
		return
	}
	var body closeBracketRequest
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	record, err := source.Close(
		request.Context(), id, bracket.State(body.State), body.Note,
	)
	if err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, record)
}

func (handler *Handler) requireBrackets(
	response http.ResponseWriter,
) (BracketSource, bool) {
	if handler.bracketSource == nil {
		writeAPIError(
			response, http.StatusServiceUnavailable,
			"the bracket terminal is not configured on this deployment",
		)
		return nil, false
	}
	return handler.bracketSource, true
}

func bracketID(
	response http.ResponseWriter, request *http.Request,
) (int64, bool) {
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(
			response, http.StatusBadRequest, errors.New("invalid bracket id").Error(),
		)
		return 0, false
	}
	return id, true
}
