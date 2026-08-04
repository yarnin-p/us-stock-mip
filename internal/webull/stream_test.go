package webull

import (
	"testing"
	"time"
)

func TestDecodeSnapshot(t *testing.T) {
	t.Parallel()
	basic := protoString(1, "AAPL")
	basic = append(basic, protoString(3, "1710849600000")...)
	payload := protoBytes(1, basic)
	payload = append(payload, protoString(2, "1710849600000")...)
	payload = append(payload, protoString(3, "185.50")...)
	payload = append(payload, protoString(7, "184.00")...)
	payload = append(payload, protoString(8, "52340000")...)
	payload = append(payload, protoString(10, "0.0082")...)

	snapshot, err := decodeSnapshot(payload)
	if err != nil {
		t.Fatalf("decodeSnapshot() error = %v", err)
	}
	if snapshot.Symbol != "AAPL" || snapshot.Price != 185.5 ||
		snapshot.ObservedAt != time.UnixMilli(1710849600000).UTC() {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestDecodeQuotePreservesBestBidAndAsk(t *testing.T) {
	t.Parallel()

	basic := protoString(1, "OPK")
	basic = append(basic, protoString(3, "1785268423000")...)
	bestAsk := protoString(1, "1.64")
	bestAsk = append(bestAsk, protoString(2, "1200")...)
	secondAsk := protoString(1, "1.65")
	secondAsk = append(secondAsk, protoString(2, "3400")...)
	bestBid := protoString(1, "1.63")
	bestBid = append(bestBid, protoString(2, "2500")...)
	secondBid := protoString(1, "1.62")
	secondBid = append(secondBid, protoString(2, "4100")...)

	payload := protoBytes(1, basic)
	payload = append(payload, protoBytes(2, bestAsk)...)
	payload = append(payload, protoBytes(2, secondAsk)...)
	payload = append(payload, protoBytes(3, bestBid)...)
	payload = append(payload, protoBytes(3, secondBid)...)

	quote, err := decodeQuote(payload)
	if err != nil {
		t.Fatalf("decodeQuote() error = %v", err)
	}
	if quote.Symbol != "OPK" || len(quote.Asks) != 2 || len(quote.Bids) != 2 {
		t.Fatalf("quote = %+v", quote)
	}
	if quote.Asks[0].Price != 1.64 || quote.Asks[0].Size != 1200 {
		t.Errorf("best ask = %+v", quote.Asks[0])
	}
	if quote.Bids[0].Price != 1.63 || quote.Bids[0].Size != 2500 {
		t.Errorf("best bid = %+v", quote.Bids[0])
	}
}

func TestDecodeTickPreservesTradeSideAndExchangeTimestamp(t *testing.T) {
	t.Parallel()

	basic := protoString(1, "OPK")
	basic = append(basic, protoString(3, "1785268423000")...)
	payload := protoBytes(1, basic)
	payload = append(payload, protoString(2, "04:00:23")...)
	payload = append(payload, protoString(3, "1.64")...)
	payload = append(payload, protoString(4, "800")...)
	payload = append(payload, protoString(5, "BUY")...)

	tick, err := decodeTick(payload)
	if err != nil {
		t.Fatalf("decodeTick() error = %v", err)
	}
	if tick.Symbol != "OPK" || tick.Price != 1.64 || tick.Volume != 800 ||
		tick.Side != "BUY" ||
		len(tick.EventID) != 64 ||
		tick.ObservedAt != time.UnixMilli(1785268423000).UTC() {
		t.Fatalf("tick = %+v", tick)
	}
}

func protoString(field int, value string) []byte {
	return protoBytes(field, []byte(value))
}

func protoBytes(field int, value []byte) []byte {
	result := appendVarint(nil, uint64(field<<3|2))
	result = appendVarint(result, uint64(len(value)))
	return append(result, value...)
}

func appendVarint(destination []byte, value uint64) []byte {
	for value >= 0x80 {
		destination = append(destination, byte(value)|0x80)
		value >>= 7
	}
	return append(destination, byte(value))
}
