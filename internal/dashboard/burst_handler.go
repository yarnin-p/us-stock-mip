package dashboard

import (
	"net/http"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/burst"
)

/* What the scanner has seen, for a screen to read.
 *
 * Alerts are held in memory rather than written to the database. They are worth
 * something for the minutes after they fire and nothing at all the next morning --
 * the whole claim is "this is moving right now" -- and a burst that has to survive a
 * restart is a burst nobody acted on. Keeping them here means the screen can ask for
 * them without a query, and losing them on a restart costs the operator nothing they
 * would have used.
 *
 * The health line matters as much as the list. A screen showing no alerts and a
 * screen showing a dead feed look identical, and one of them means the market is
 * quiet while the other means nothing is being watched at all.
 */

// BurstFeed is what the scanner tells this handler. Declared here beside its
// consumer: the dashboard asks for alerts, not for a scanner.
type BurstFeed interface {
	// Health reports how many symbols are subscribed and how many alerts have fired,
	// so silence can be told apart from a dead feed.
	Health() (watching int, alerts int)
}

// BurstLog keeps the alerts a screen shows. It is the scanner's sink and the
// handler's source, which is why it lives between them rather than inside either.
type BurstLog struct {
	mutex sync.Mutex
	// recent is newest-first and bounded. A day of alerts at the measured rate is
	// under twenty; the cap is for the day the market goes mad, not the normal one.
	recent []BurstAlert
	limit  int
	feed   BurstFeed
}

// BurstAlert is one alert as a screen reads it: the claim, and the evidence for it.
type BurstAlert struct {
	Ticker string  `json:"ticker"`
	Gain   float64 `json:"gain"`
	Price  float64 `json:"price"`
	Low    float64 `json:"low"`
	// TookSeconds is how long the move actually took, which is usually shorter than
	// the window it was measured over. "+23% in 47 seconds" is a different statement
	// from "+23%", and it is the one an operator can act on.
	TookSeconds float64   `json:"took_seconds"`
	At          time.Time `json:"at"`
}

func NewBurstLog(limit int) *BurstLog {
	if limit <= 0 {
		limit = 200
	}
	return &BurstLog{limit: limit}
}

// Attach gives the log the scanner to report health from. Separate from
// construction because the scanner needs the log before the log can have it.
func (log *BurstLog) Attach(feed BurstFeed) { log.feed = feed }

// Burst makes this the scanner's sink.
func (log *BurstLog) Burst(alert burst.Alert) {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	entry := BurstAlert{
		Ticker: alert.Ticker, Gain: alert.Gain, Price: alert.Price, Low: alert.Low,
		TookSeconds: alert.At.Sub(alert.LowAt).Seconds(), At: alert.At,
	}
	log.recent = append([]BurstAlert{entry}, log.recent...)
	if len(log.recent) > log.limit {
		log.recent = log.recent[:log.limit]
	}
}

func (log *BurstLog) snapshot() []BurstAlert {
	log.mutex.Lock()
	defer log.mutex.Unlock()
	return append([]BurstAlert(nil), log.recent...)
}

func (handler *Handler) bursts(response http.ResponseWriter, request *http.Request) {
	if handler.bursts_ == nil {
		// Not an error. A deployment can legitimately run without a scanner, and the
		// screen should say so rather than show an empty list that reads as calm.
		writeJSON(response, http.StatusOK, map[string]any{
			"running": false,
			"alerts":  []BurstAlert{},
			"detail":  "no scanner is wired in this deployment",
		})
		return
	}
	alerts := handler.bursts_.snapshot()
	payload := map[string]any{
		"running": true,
		"alerts":  alerts,
	}
	if handler.bursts_.feed != nil {
		watching, total := handler.bursts_.feed.Health()
		payload["watching"] = watching
		payload["fired_today"] = total
		if watching == 0 {
			// The one case worth saying out loud: an empty screen because nothing is
			// subscribed, which looks exactly like a quiet market and is not one.
			payload["detail"] = "the scanner is running but watching nothing"
		}
	}
	writeJSON(response, http.StatusOK, payload)
}
