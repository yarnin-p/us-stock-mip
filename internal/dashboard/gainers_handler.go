package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/gainers"
)

// GainersSource serves the stored session gainer history. It is supplied
// through Options rather than added to Repository so the existing test doubles
// keep compiling; a screen that reads history has no business forcing every
// stub to grow a method.
type GainersSource interface {
	SessionGainers(context.Context, time.Time, gainers.Session) ([]gainers.Entry, error)
	SessionGainerDates(context.Context, int) ([]time.Time, error)
}

type gainerReason struct {
	Tag   string `json:"tag"`
	Label string `json:"label"`
}

// gainerRow is the wire shape. Every number the screen shows is sent
// pre-computed rather than derived in the browser, so the page and any study
// run against the same figures.
type gainerRow struct {
	Rank            int            `json:"rank"`
	Ticker          string         `json:"ticker"`
	ReferencePrice  float64        `json:"reference_price"`
	ReferenceSource string         `json:"reference_source"`
	High            float64        `json:"high,omitempty"`
	Low             float64        `json:"low,omitempty"`
	Close           float64        `json:"close"`
	ChangePct       float64        `json:"change_pct"`
	MaxChangePct    float64        `json:"max_change_pct,omitempty"`
	Volume          float64        `json:"volume,omitempty"`
	RelativeVolume  float64        `json:"relative_volume,omitempty"`
	FloatShares     float64        `json:"float_shares,omitempty"`
	FloatRotation   float64        `json:"float_rotation,omitempty"`
	MarketCap       float64        `json:"market_cap,omitempty"`
	HasNews         bool           `json:"has_news"`
	NewsTitle       string         `json:"news_title,omitempty"`
	NewsPublishedAt *string        `json:"news_published_at,omitempty"`
	CatalystScore   float64        `json:"catalyst_score,omitempty"`
	Reasons         []gainerReason `json:"reasons"`
}

type gainersResponse struct {
	TradingDate string      `json:"trading_date"`
	Session     string      `json:"session"`
	Rows        []gainerRow `json:"rows"`
	// Dates lets the screen build its day picker from what actually exists
	// rather than from a calendar, so a missing capture is visible.
	Dates []string `json:"dates,omitempty"`
	// Note explains an empty field. A blank table with no reason reads as "no
	// gainers", which is a different claim from "not collected".
	Note string `json:"note,omitempty"`
}

// reasonLabels turn the stored tags into something a person reads without a
// legend. The tag is kept alongside so the screen can still filter on it.
var reasonLabels = map[string]string{
	gainers.ReasonExtremeRotation: "float หมุนหนักมาก",
	gainers.ReasonHighRotation:    "float หมุนสูง",
	gainers.ReasonMicroFloat:      "float จิ๋ว",
	gainers.ReasonLowFloat:        "float ต่ำ",
	gainers.ReasonExtremeVolume:   "RVol สูงมาก",
	gainers.ReasonHighVolume:      "RVol สูง",
	gainers.ReasonStrongCatalyst:  "ข่าวแรง",
	gainers.ReasonNewsCatalyst:    "มีข่าว",
	gainers.ReasonNoStoredNews:    "ไม่มีข่าวในฐานข้อมูล",
	gainers.ReasonSubDollar:       "ต่ำกว่า $1",
	gainers.ReasonNanoCap:         "mcap จิ๋ว",
	gainers.ReasonFadedFromHigh:   "ถอยจาก high",
	gainers.ReasonClosedAtHigh:    "ปิดใกล้ high",
	gainers.ReasonNewHigh52Week:   "ทำ 52w high ใหม่",
	gainers.ReasonNearLow52Week:   "ใกล้ 52w low",
}

func (handler *Handler) gainers(
	response http.ResponseWriter, request *http.Request,
) {
	if handler.gainersSource == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{
			"error": "gainer history is not configured",
		})
		return
	}
	session := gainers.Session(request.URL.Query().Get("session"))
	if session == "" {
		session = gainers.SessionRegular
	}
	if !session.Valid() {
		writeJSON(response, http.StatusBadRequest, map[string]string{
			"error": "session must be PRE_MARKET, REGULAR or AFTER_HOURS",
		})
		return
	}
	dates, err := handler.gainersSource.SessionGainerDates(request.Context(), 120)
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	tradingDate, err := resolveGainerDate(request.URL.Query().Get("date"), dates)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
		return
	}
	payload := gainersResponse{
		Session: string(session),
		Rows:    []gainerRow{},
		Dates:   make([]string, 0, len(dates)),
	}
	for _, date := range dates {
		payload.Dates = append(payload.Dates, date.Format(time.DateOnly))
	}
	if tradingDate.IsZero() {
		payload.Note = "ยังไม่มีข้อมูลที่เก็บไว้"
		writeJSON(response, http.StatusOK, payload)
		return
	}
	payload.TradingDate = tradingDate.Format(time.DateOnly)

	entries, err := handler.gainersSource.SessionGainers(
		request.Context(), tradingDate, session,
	)
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	for _, entry := range entries {
		payload.Rows = append(payload.Rows, toGainerRow(entry))
	}
	if len(payload.Rows) == 0 {
		// The extended sessions only exist for dates the collector was awake.
		// Saying so is the difference between "nothing moved" and "nobody
		// watched", and only one of those is a fact about the market.
		payload.Note = "ไม่มีข้อมูล session นี้ของวันดังกล่าว " +
			"(ตัวเก็บข้อมูลอาจยังไม่ทำงานในวันนั้น)"
	}
	writeJSON(response, http.StatusOK, payload)
}

// resolveGainerDate picks the day to show: the one asked for, or the newest
// captured day when none was.
func resolveGainerDate(raw string, dates []time.Time) (time.Time, error) {
	if raw == "" {
		if len(dates) == 0 {
			return time.Time{}, nil
		}
		return dates[0], nil
	}
	parsed, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		return time.Time{}, errInvalidGainerDate
	}
	return parsed, nil
}

var errInvalidGainerDate = &gainerDateError{}

type gainerDateError struct{}

func (*gainerDateError) Error() string { return "date must be YYYY-MM-DD" }

func toGainerRow(entry gainers.Entry) gainerRow {
	row := gainerRow{
		Rank: entry.Rank, Ticker: entry.Ticker,
		ReferencePrice:  entry.ReferencePrice,
		ReferenceSource: entry.ReferenceSource,
		High:            entry.High, Low: entry.Low, Close: entry.Close,
		ChangePct:      round2(entry.ChangeRatio * 100),
		MaxChangePct:   round2(entry.MaxChangeRatio * 100),
		Volume:         entry.Volume,
		RelativeVolume: round2(entry.RelativeVolume),
		FloatShares:    entry.FloatShares,
		FloatRotation:  round2(entry.FloatRotation),
		MarketCap:      entry.MarketCap,
		HasNews:        entry.HasNews,
		NewsTitle:      entry.NewsTitle,
		CatalystScore:  entry.NewsCatalystScore,
		Reasons:        make([]gainerReason, 0, len(entry.Reasons)),
	}
	if !entry.NewsPublishedAt.IsZero() {
		stamp := entry.NewsPublishedAt.UTC().Format(time.RFC3339)
		row.NewsPublishedAt = &stamp
	}
	for _, tag := range entry.Reasons {
		label := reasonLabels[tag]
		if label == "" {
			label = tag
		}
		row.Reasons = append(row.Reasons, gainerReason{Tag: tag, Label: label})
	}
	return row
}

func round2(value float64) float64 {
	scaled := value * 100
	if scaled < 0 {
		return float64(int64(scaled-0.5)) / 100
	}
	return float64(int64(scaled+0.5)) / 100
}
