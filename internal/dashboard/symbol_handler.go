package dashboard

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

/* Answering "is this a symbol I can actually trade" before the ticket is written.
 *
 * The order used to be the other way round: type anything, fill in the size and the
 * levels, and find out at submit that the broker has never heard of it. Checking at
 * the field turns a wasted ticket into a red underline.
 *
 * Two questions, kept apart because they fail differently. `Known` means the symbol
 * is in this database -- that catches the typo, instantly, with no network call.
 * `Tradable` means the venue returned a quote for it, which is the only answer that
 * actually decides whether an order can be sent, and it costs a round trip. A symbol
 * can be known and not tradable (delisted, or a class the venue does not carry), and
 * that difference is worth showing rather than flattening into "invalid".
 */

// SymbolInfo is what the terminal needs to decide whether to let a ticket be written.
type SymbolInfo struct {
	Ticker   string `json:"ticker"`
	Known    bool   `json:"known"`
	Tradable bool   `json:"tradable"`
	Name     string `json:"name,omitempty"`
	Exchange string `json:"exchange,omitempty"`
	// Reason carries why it was refused, in words meant to be shown.
	Reason string `json:"reason,omitempty"`
}

// SymbolDirectory is the local universe: every symbol this system knows about.
type SymbolDirectory interface {
	LookupSymbol(ctx context.Context, ticker string) (SymbolInfo, error)
}

// SymbolQuoter asks the venue whether it will quote the symbol. A quote coming back
// is the venue saying it carries it; that is the same test the stream subscription
// applies, so agreeing with it here means the terminal cannot accept something the
// feed will later reject.
type SymbolQuoter interface {
	QuotableSymbols(ctx context.Context, symbols []string) ([]string, error)
}

// symbolVerdicts caches what the venue said. Answers are stable over a session and
// the field asks on every pause in typing, so without this a slow morning of ticket
// writing is a few hundred identical round trips.
type symbolVerdicts struct {
	mutex sync.RWMutex
	seen  map[string]symbolVerdict
	ttl   time.Duration
}

type symbolVerdict struct {
	tradable bool
	at       time.Time
}

func newSymbolVerdicts(ttl time.Duration) *symbolVerdicts {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &symbolVerdicts{seen: map[string]symbolVerdict{}, ttl: ttl}
}

func (cache *symbolVerdicts) get(ticker string) (bool, bool) {
	cache.mutex.RLock()
	defer cache.mutex.RUnlock()
	verdict, ok := cache.seen[ticker]
	if !ok || time.Since(verdict.at) > cache.ttl {
		return false, false
	}
	return verdict.tradable, true
}

func (cache *symbolVerdicts) put(ticker string, tradable bool, now time.Time) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.seen[ticker] = symbolVerdict{tradable: tradable, at: now}
}

// NormalizeTicker applies the same rule the input field applies, so a symbol that
// passed there cannot be rejected here for its shape. Measured against the 20,750 US
// symbols in the database: A-Z, "." and "-" only, nine characters at most, no digits.
func NormalizeTicker(raw string) string {
	upper := strings.ToUpper(strings.TrimSpace(raw))
	var out strings.Builder
	for _, r := range upper {
		switch {
		case r >= 'A' && r <= 'Z', r == '.', r == '-':
			out.WriteRune(r)
		}
	}
	trimmed := out.String()
	if len(trimmed) > 9 {
		trimmed = trimmed[:9]
	}
	return trimmed
}

func (handler *Handler) symbolLookup(
	response http.ResponseWriter, request *http.Request,
) {
	ticker := NormalizeTicker(request.PathValue("ticker"))
	if ticker == "" {
		writeAPIError(response, http.StatusBadRequest, "ticker is required")
		return
	}

	info := SymbolInfo{Ticker: ticker}
	if handler.symbols != nil {
		found, err := handler.symbols.LookupSymbol(request.Context(), ticker)
		if err != nil {
			handler.repositoryError(response, err)
			return
		}
		info = found
		info.Ticker = ticker
	}
	if !info.Known {
		info.Reason = "not in the symbol directory — check the spelling"
		writeJSON(response, http.StatusOK, info)
		return
	}

	// Known but unquoted is still worth sending: without a quoter wired the terminal
	// should not refuse a symbol it has no way to disprove.
	if handler.symbolQuoter == nil {
		info.Tradable = true
		writeJSON(response, http.StatusOK, info)
		return
	}
	if tradable, cached := handler.symbolVerdicts.get(ticker); cached {
		info.Tradable = tradable
		if !tradable {
			info.Reason = "the broker does not quote this symbol"
		}
		writeJSON(response, http.StatusOK, info)
		return
	}

	quotable, err := handler.symbolQuoter.QuotableSymbols(request.Context(), []string{ticker})
	if err != nil {
		// A failed check is not a refusal. Saying so lets the field show that it could
		// not confirm rather than claiming the symbol is bad.
		info.Tradable = true
		info.Reason = "could not reach the broker to confirm this symbol"
		writeJSON(response, http.StatusOK, info)
		return
	}
	for _, symbol := range quotable {
		if NormalizeTicker(symbol) == ticker {
			info.Tradable = true
			break
		}
	}
	handler.symbolVerdicts.put(ticker, info.Tradable, time.Now())
	if !info.Tradable {
		info.Reason = "the broker does not quote this symbol"
	}
	writeJSON(response, http.StatusOK, info)
}
