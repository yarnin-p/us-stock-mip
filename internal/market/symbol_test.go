package market

import (
	"reflect"
	"testing"
)

func TestNormalizeUSStockSymbolsKeepsOnlyPlainSnapshotSymbols(t *testing.T) {
	got := NormalizeUSStockSymbols(
		[]string{" aapl ", "ACHR.WS", "", "osCG"},
		[]string{"AAPL", "BRK B", "LVWR.WS", "STFS"},
	)
	want := []string{"AAPL", "OSCG", "STFS"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols = %v, want %v", got, want)
	}
}
