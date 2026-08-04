package scanner

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
)

type Quote struct {
	Ticker            string
	Price             float64
	Volume            float64
	ChangeRatio       float64
	ObservedAt        time.Time
	RunnerProbability *float64
}

type Signal struct {
	Quote
	Score float64
}

type Fetch func(context.Context, []string) ([]Quote, error)

type Sink interface {
	SaveScannerSignals(context.Context, []Signal) error
}

type Criteria struct {
	MinPrice, MaxPrice float64
	MinChangeRatio     float64
	MinVolume          float64
}

type Scanner struct {
	fetch    Fetch
	sink     Sink
	criteria Criteria
}

func New(fetch Fetch, sink Sink, criteria Criteria) (*Scanner, error) {
	if fetch == nil || sink == nil {
		return nil, errors.New("scanner fetcher and sink are required")
	}
	if criteria.MinPrice < 0 || criteria.MaxPrice <= criteria.MinPrice ||
		criteria.MinChangeRatio < 0 || criteria.MinVolume < 0 {
		return nil, errors.New("invalid scanner criteria")
	}
	return &Scanner{fetch: fetch, sink: sink, criteria: criteria}, nil
}

func (scanner *Scanner) Scan(
	ctx context.Context,
	tickers []string,
) ([]Signal, error) {
	if len(tickers) == 0 {
		return nil, errors.New("scanner tickers are required")
	}
	quotes, err := scanner.fetch(ctx, tickers)
	if err != nil {
		return nil, err
	}
	return scanner.Evaluate(ctx, quotes)
}

// Evaluate applies scanner criteria to already-delivered realtime quotes.
func (scanner *Scanner) Evaluate(
	ctx context.Context,
	quotes []Quote,
) ([]Signal, error) {
	signals := make([]Signal, 0, len(quotes))
	for _, quote := range quotes {
		if strings.TrimSpace(quote.Ticker) == "" || quote.ObservedAt.IsZero() ||
			quote.Price <= 0 || quote.Volume < 0 {
			return nil, errors.New("scanner received an invalid quote")
		}
		if quote.Price < scanner.criteria.MinPrice ||
			quote.Price > scanner.criteria.MaxPrice ||
			quote.ChangeRatio < scanner.criteria.MinChangeRatio ||
			quote.Volume < scanner.criteria.MinVolume {
			continue
		}
		score := quote.ChangeRatio*100 + math.Log10(max(quote.Volume, 1))
		if quote.RunnerProbability != nil {
			if *quote.RunnerProbability < 0 || *quote.RunnerProbability > 1 {
				return nil, errors.New("scanner runner probability must be between zero and one")
			}
			score += *quote.RunnerProbability * 100
		}
		score = min(score, 100)
		signals = append(signals, Signal{Quote: quote, Score: score})
	}
	slices.SortStableFunc(signals, func(left, right Signal) int {
		if left.Score > right.Score {
			return -1
		}
		if left.Score < right.Score {
			return 1
		}
		return 0
	})
	if err := scanner.sink.SaveScannerSignals(ctx, signals); err != nil {
		return nil, err
	}
	return signals, nil
}
