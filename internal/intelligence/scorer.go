package intelligence

import (
	"strings"
	"time"
)

type NewsItem struct {
	ExternalID  string
	PublishedAt time.Time
	AvailableAt time.Time
	Title       string
	Description string
	URL         string
	Sentiment   string
}

type TickerNewsItem struct {
	Ticker string
	News   NewsItem
}

type Filing struct {
	AccessionNo string
	FiledAt     time.Time
	FormType    string
	SourceURL   string
}

type Split struct {
	ExternalID    string
	ExecutionDate time.Time
	From, To      float64
	Reverse       bool
}

type Input struct {
	AsOf    time.Time
	News    []NewsItem
	Filings []Filing
	Splits  []Split
}

type Scores struct {
	NewsScore, FDAScore, MAScore, ThemeScore float64
	ATMRisk, OfferingRisk                    float64
	ReverseSplitCount                        int32
}

type Scorer struct{}

func NewScorer() *Scorer { return &Scorer{} }

func (*Scorer) Score(input Input) Scores {
	var scores Scores
	for _, item := range input.News {
		if item.PublishedAt.After(input.AsOf) {
			continue
		}
		text := strings.ToLower(item.Title + " " + item.Description)
		classification := ClassifyNews(item)
		classificationStrength := 0.0
		if classification.Tradeable && !classification.Negative {
			classificationStrength = classification.Strength
		}
		providerSentiment := sentimentScore(item.Sentiment)
		if classification.Negative {
			providerSentiment = min(providerSentiment, 0.2)
		}
		scores.NewsScore = max(
			scores.NewsScore,
			providerSentiment,
			classificationStrength,
		)
		if containsAny(text, "fda", "clinical trial", "drug approval") {
			scores.FDAScore = 1
		}
		if containsAny(text, "acquisition", "merger", "buyout", "strategic transaction") {
			scores.MAScore = 1
		}
		if containsAny(text, "artificial intelligence", " ai ", "ai-", "blockchain", "quantum", "robotics") {
			scores.ThemeScore = 1
		}
	}
	for _, filing := range input.Filings {
		if filing.FiledAt.After(input.AsOf) {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(filing.FormType)) {
		case "424B5":
			scores.OfferingRisk = 1
			scores.ATMRisk = max(scores.ATMRisk, 0.9)
		case "S-3", "S-1":
			scores.OfferingRisk = max(scores.OfferingRisk, 0.8)
			scores.ATMRisk = max(scores.ATMRisk, 0.6)
		}
	}
	for _, split := range input.Splits {
		if split.Reverse && !split.ExecutionDate.After(input.AsOf) {
			scores.ReverseSplitCount++
		}
	}
	return scores
}

func sentimentScore(sentiment string) float64 {
	switch strings.ToLower(strings.TrimSpace(sentiment)) {
	case "positive":
		return 0.8
	case "negative":
		return 0.2
	case "neutral":
		return 0.5
	default:
		return 0
	}
}

func containsAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}
