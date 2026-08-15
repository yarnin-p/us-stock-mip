// Package burst detects a price travelling a distance in a hurry.
//
// It answers one question, continuously: has this name moved X% up from its own
// recent low inside the last N minutes. Not "is it up X% today" -- that is a level,
// and a level is mostly yesterday's news by the time anyone reads it. A burst is the
// derivative, and it is the only thing on the screen that says something is happening
// right now.
//
// The distinction is not academic. Percent-change from the session baseline resets
// once a day, at the after-hours bell; for the rest of the day a name that ran
// overnight shows a large number and does nothing, while a name that just started
// moving shows a small one. A scanner ranked on that number spends the morning
// pointing at moves that finished hours ago.
//
// Measured over 106 sessions and 1,217 large after-hours movers, the rule below
// (+20% inside 5 minutes) surfaces 91.6% of them at 17.4 alerts a day, and the median
// alert still has +15.6% ahead of it when it fires. Those numbers are in the package
// tests as the reason the defaults are what they are.
package burst

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// Alert is one name, once, the moment it moved.
type Alert struct {
	Ticker string
	// At is the print that triggered it.
	At time.Time
	// Price is that print. Low is the bottom of the window it travelled from, and
	// Gain is the distance between them -- the three together are the whole claim,
	// so a reader never has to trust the number without seeing what made it.
	Price float64
	Low   float64
	Gain  float64
	// Window is how far back Low was found, so an alert can say "+22% in 5 minutes"
	// rather than "+22%", which is a different and much weaker statement.
	Window time.Duration
	// LowAt is when that low printed. The distance between LowAt and At is how long
	// the move actually took, which is usually shorter than the window.
	LowAt time.Time
}

func (alert Alert) String() string {
	return fmt.Sprintf(
		"%s +%.1f%% in %s (%.4f -> %.4f)",
		alert.Ticker, alert.Gain*100,
		alert.At.Sub(alert.LowAt).Round(time.Second), alert.Low, alert.Price,
	)
}

// Options configure a Detector. The zero value of each falls back to the measured
// defaults rather than to nothing, because a detector wired with a zero threshold
// fires on every print and a reader would not notice until the screen filled.
type Options struct {
	// Threshold is the rise that fires an alert, as a fraction. Default 0.20.
	//
	// This is the term that matters. Across the measured window, +5% gives 167 alerts
	// a day at 7% precision and +20% gives 17 at 59% -- the same recall to within a
	// few points, for a tenth of the noise.
	Threshold float64
	// Window is how far back the low is taken from. Default 5 minutes.
	//
	// Widening it barely changes the alert count and buys real recall, because a
	// slower climb still clears the threshold eventually. Narrowing it below two
	// minutes starts missing names that build rather than jump.
	Window time.Duration
	// Cooldown is how long a name is silenced after firing. Default is the rest of
	// the day, expressed as 0.
	//
	// Firing repeatedly on one name is how a watchable stream becomes an unwatchable
	// one: the same rule re-firing turned 582 alerts into 2,783 over the measured
	// window, five times the volume for no additional information. The first alert is
	// the one that carries the news.
	Cooldown time.Duration
	// Now is the clock, for tests. Nil means the wall clock.
	Now func() time.Time
}

// Detector watches prints and reports bursts. It is safe for concurrent use: one
// feed goroutine per symbol is the normal arrangement and they must not have to
// coordinate.
type Detector struct {
	threshold float64
	window    time.Duration
	cooldown  time.Duration
	now       func() time.Time

	mutex sync.Mutex
	// tracked holds the recent prints of every symbol being watched. It is bounded
	// by the window rather than by a count, so a name that trades constantly and one
	// that prints twice an hour both cost what their own activity costs.
	tracked map[string]*trail
}

type trail struct {
	prints []print
	// firedAt is when this symbol last alerted. Zero means never.
	firedAt time.Time
}

type print struct {
	at    time.Time
	price float64
}

// New builds a detector. It refuses a threshold that is not a positive fraction and
// a window that is not positive, because both are silent failures: the first fires
// on everything, the second on nothing, and neither announces itself.
func New(options Options) (*Detector, error) {
	detector := &Detector{
		threshold: options.Threshold,
		window:    options.Window,
		cooldown:  options.Cooldown,
		now:       options.Now,
		tracked:   make(map[string]*trail),
	}
	if detector.threshold == 0 {
		detector.threshold = 0.20
	}
	if detector.window == 0 {
		detector.window = 5 * time.Minute
	}
	if detector.now == nil {
		detector.now = func() time.Time { return time.Now().UTC() }
	}
	if detector.threshold <= 0 || !finite(detector.threshold) {
		return nil, errors.New(
			"a burst threshold must be a positive fraction; zero would fire on every print",
		)
	}
	if detector.threshold >= 10 {
		return nil, fmt.Errorf(
			"a burst threshold of %.2f is a %.0f%% move -- this takes a fraction, not "+
				"a percentage", detector.threshold, detector.threshold*100,
		)
	}
	if detector.window <= 0 {
		return nil, errors.New("a burst window must be positive; zero can never fire")
	}
	return detector, nil
}

/* Observe takes one print and reports a burst if this is the one that made it.
 *
 * It answers on every print rather than at the close of each minute. The measurement
 * behind the defaults was made on completed one-minute bars, because that is what
 * historical data comes as; live, waiting for the minute to close would delay every
 * alert by up to sixty seconds for no gain. Firing on the print is never later than
 * firing on the bar and is usually earlier, and the comparison is the same one --
 * the print is the running high of the minute it belongs to.
 *
 * A nil return is the normal case and means nothing happened.
 */
func (detector *Detector) Observe(
	ticker string, price float64, at time.Time,
) *Alert {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	if ticker == "" || !(price > 0) || !finite(price) || at.IsZero() {
		return nil
	}
	detector.mutex.Lock()
	defer detector.mutex.Unlock()

	watched, ok := detector.tracked[ticker]
	if !ok {
		watched = &trail{}
		detector.tracked[ticker] = watched
	}

	// Trim first, so the low this print is measured against is the low of the window
	// as it stands now rather than as it stood when the last print arrived.
	cutoff := at.Add(-detector.window)
	kept := watched.prints[:0]
	for _, earlier := range watched.prints {
		if !earlier.at.Before(cutoff) {
			kept = append(kept, earlier)
		}
	}
	watched.prints = append(kept, print{at: at, price: price})

	if !detector.ready(watched, at) {
		return nil
	}
	low, lowAt := watched.low()
	if !(low > 0) {
		return nil
	}
	gain := price/low - 1
	if gain < detector.threshold {
		return nil
	}
	watched.firedAt = at
	return &Alert{
		Ticker: ticker, At: at, Price: price, Low: low, Gain: gain,
		Window: detector.window, LowAt: lowAt,
	}
}

// ready reports whether this symbol is allowed to fire. A cooldown of zero means the
// alert stands until Reset is called, which the caller does once a session.
func (detector *Detector) ready(watched *trail, at time.Time) bool {
	if watched.firedAt.IsZero() {
		return true
	}
	if detector.cooldown <= 0 {
		return false
	}
	return at.Sub(watched.firedAt) >= detector.cooldown
}

func (watched *trail) low() (float64, time.Time) {
	low := math.Inf(1)
	var lowAt time.Time
	for _, earlier := range watched.prints {
		if earlier.price < low {
			low, lowAt = earlier.price, earlier.at
		}
	}
	if math.IsInf(low, 1) {
		return 0, time.Time{}
	}
	return low, lowAt
}

// Reset forgets everything, which is what a new session is. Called at the bell rather
// than left to expire: a low from yesterday's close is not part of today's move, and
// a name silenced by yesterday's alert has to be able to fire again.
func (detector *Detector) Reset() {
	detector.mutex.Lock()
	defer detector.mutex.Unlock()
	detector.tracked = make(map[string]*trail)
}

// Forget drops one symbol, for a watchlist that changed under the detector rather
// than a session that ended.
func (detector *Detector) Forget(ticker string) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	detector.mutex.Lock()
	defer detector.mutex.Unlock()
	delete(detector.tracked, ticker)
}

// Watching reports how many symbols are being tracked, for a health line that can
// say whether the feed is actually arriving.
func (detector *Detector) Watching() int {
	detector.mutex.Lock()
	defer detector.mutex.Unlock()
	return len(detector.tracked)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
