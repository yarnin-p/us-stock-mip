package market

import (
	"regexp"
	"strings"
)

var plainUSStockSymbol = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,19}$`)

// NormalizeUSStockSymbols returns the distinct plain symbols accepted by the
// Webull stock snapshot endpoint. Warrant, unit, right, and class suffixes are
// deliberately excluded from the automatic execution universe.
func NormalizeUSStockSymbols(groups ...[]string) []string {
	seen := make(map[string]struct{})
	symbols := make([]string, 0)
	for _, group := range groups {
		for _, raw := range group {
			symbol := strings.ToUpper(strings.TrimSpace(raw))
			if !plainUSStockSymbol.MatchString(symbol) {
				continue
			}
			if _, exists := seen[symbol]; exists {
				continue
			}
			seen[symbol] = struct{}{}
			symbols = append(symbols, symbol)
		}
	}
	return symbols
}
