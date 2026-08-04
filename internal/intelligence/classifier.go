package intelligence

import "strings"

type NewsClassification struct {
	Kind      string   `json:"kind"`
	Strength  float64  `json:"strength"`
	Tradeable bool     `json:"tradeable"`
	Negative  bool     `json:"negative"`
	Reasons   []string `json:"reasons"`
}

// ClassifyNews applies deterministic materiality rules. Provider sentiment is
// deliberately not enough to make an event tradeable: the article must state
// a concrete catalyst and not merely announce a future event or summarize
// price action.
func ClassifyNews(item NewsItem) NewsClassification {
	title := strings.ToLower(strings.TrimSpace(item.Title))
	text := strings.TrimSpace(title + " " + strings.ToLower(item.Description))
	classification := NewsClassification{
		Kind: "OTHER", Reasons: make([]string, 0, 3),
	}
	if text == "" {
		return classification
	}
	fdaContext := containsAny(
		text,
		"fda",
		"food and drug administration",
	)
	negativeFDAOutcome := fdaContext && containsAny(
		text,
		"fda rejects",
		"fda rejected",
		"fda declines",
		"fda declined",
		"complete response letter",
		"panel rejects",
		"panel rejected",
		"panel votes against",
		"panel voted against",
		"committee votes against",
		"committee voted against",
		"votes against",
		"voted against",
		"against approval",
		"recommends against",
		"recommended against",
		"not clinically meaningful",
		"not interpretable",
		"fails to meet primary endpoint",
		"failed to meet primary endpoint",
		"missed primary endpoint",
	)
	hardFDAOutcome := containsAny(
		title,
		"fda approves",
		"fda approved",
		"fda grants approval",
		"fda granted approval",
		"received fda approval",
		"positive topline",
		"positive top-line",
		"met primary endpoint",
	)
	pendingFDAEvent := fdaContext && !hardFDAOutcome && containsAny(
		title,
		"upcoming",
		"scheduled",
		"will review",
		"to discuss",
		"meeting set",
		"meeting scheduled",
		"agenda",
		"briefing documents",
		"expected",
		"initiates",
		"initiation of",
		"commences",
		"begins",
		"will hold",
		"will convene",
	)
	positiveFDAAdvisoryOutcome := fdaContext && containsAny(
		text,
		"favorable outcome",
		"panel backs",
		"committee backs",
		"voted in favor",
		"votes in favor",
		"recommends approval",
		"recommended approval",
	) && !negativeFDAOutcome && !pendingFDAEvent
	positiveFDAAdvisoryOutcome = positiveFDAAdvisoryOutcome ||
		(fdaContext && !negativeFDAOutcome && !pendingFDAEvent &&
			containsAny(
				text,
				"committee votes",
				"committee voted",
				"panel votes",
				"panel voted",
			) &&
			containsAny(text, "clinically meaningful", "evaluable"))

	if containsAny(
		text,
		"public offering",
		"registered direct offering",
		"at-the-market offering",
		"reverse stock split",
		"bankruptcy",
		"chapter 11",
		"going concern",
	) {
		classification.Kind = "DILUTION_OR_DISTRESS"
		classification.Strength = 1
		classification.Negative = true
		classification.Reasons = append(
			classification.Reasons,
			"dilution or financial-distress language",
		)
		return classification
	}
	if negativeFDAOutcome {
		classification.Kind = "FDA_CLINICAL_NEGATIVE"
		classification.Strength = 1
		classification.Negative = true
		classification.Reasons = append(
			classification.Reasons,
			"negative FDA or clinical outcome",
		)
		return classification
	}
	if containsAny(
		title,
		"class action",
		"law firm",
		"investigation of breaches",
		"investor deadline",
		"final summary judgment",
		"restitution",
		"litigation",
		"lawsuit",
	) {
		classification.Kind = "LEGAL"
		classification.Strength = 0.15
		classification.Negative = true
		classification.Reasons = append(
			classification.Reasons,
			"legal or shareholder-solicitation headline",
		)
		return classification
	}
	if containsWord(title, "guidance") && containsAny(
		title,
		"lowers",
		"lowered",
		"cuts",
		"cut ",
		"reduces",
		"reduced",
		"withdraws",
		"withdrew",
	) {
		classification.Kind = "GUIDANCE_NEGATIVE"
		classification.Strength = 0.92
		classification.Negative = true
		classification.Reasons = append(
			classification.Reasons,
			"forward guidance reduced or withdrawn",
		)
		return classification
	}
	if strings.Contains(title, "?") ||
		strings.HasPrefix(title, "is ") ||
		strings.Contains(title, ", is ") ||
		containsAny(
			title,
			"should you buy",
			"is this stock a buy",
			"here's why",
			"here’s why",
			"what that means",
			"too cheap to ignore",
			"could be the next",
			"investors need to know",
			"why shares",
			"why stock",
			"stock is sinking",
			"stock is moving",
		) {
		classification.Kind = "COMMENTARY"
		classification.Strength = 0.15
		classification.Reasons = append(
			classification.Reasons,
			"commentary or question rather than a new issuer event",
		)
		return classification
	}
	if (containsAny(
		text,
		"schedules earnings",
		"earnings release for",
		"will report",
		"conference call",
		"earnings call transcript",
	) || pendingFDAEvent) && !containsAny(
		text,
		"beats",
		"topped estimates",
		"raises guidance",
		"revenue rose",
		"reported quarterly",
	) {
		classification.Kind = "SCHEDULED_EVENT"
		classification.Strength = 0.25
		classification.Reasons = append(
			classification.Reasons,
			"future or commentary-only event without reported result",
		)
		return classification
	}
	if containsAny(
		text,
		"schedule 13d/a",
		"schedule 13d amendment",
		"amended beneficial ownership",
	) && !containsAny(
		text,
		"increased stake",
		"increases stake",
		"raised stake",
		"acquired additional",
	) {
		classification.Kind = "OWNERSHIP_AMENDMENT"
		classification.Strength = 0.35
		classification.Reasons = append(
			classification.Reasons,
			"ownership amendment without confirmed stake increase",
		)
		return classification
	}

	if hardFDAOutcome || positiveFDAAdvisoryOutcome {
		classification.Kind = "FDA_CLINICAL"
		classification.Strength = 0.92
		classification.Tradeable = true
		classification.Reasons = append(
			classification.Reasons,
			"positive regulatory or clinical outcome",
		)
		if hardFDAOutcome {
			classification.Strength = 0.95
		}
		return classification
	}

	if containsAny(
		title,
		"urges stockholders to reject",
		"urges shareholders to reject",
		"recommends stockholders reject",
		"recommends shareholders reject",
		"rejects acquisition",
		"rejects takeover",
		"rejects unsolicited",
		"rejected acquisition",
		"rejected takeover",
		"rejected unsolicited",
		"declines acquisition proposal",
	) {
		classification.Kind = "UNCONFIRMED_TRANSACTION"
		classification.Strength = 0.35
		classification.Reasons = append(
			classification.Reasons,
			"transaction rejected or contested rather than definitive",
		)
		return classification
	}

	if containsAny(
		title,
		"definitive merger",
		"definitive agreement",
		"to acquire",
		"will acquire",
		"acquisition agreement",
		"buyout",
	) {
		classification.Kind = "MERGER_ACQUISITION"
		classification.Strength = 0.95
		classification.Tradeable = true
		classification.Reasons = append(
			classification.Reasons,
			"definitive strategic transaction",
		)
		return classification
	}

	if containsWord(title, "guidance") && containsAny(
		title,
		"raises",
		"raised",
		"increases",
		"increased",
		"boosts",
		"boosted",
	) && !containsAny(
		title,
		"reports",
		"reported",
		"earnings",
		"quarterly results",
		"financial results",
	) {
		classification.Kind = "GUIDANCE"
		classification.Strength = 0.92
		classification.Tradeable = true
		classification.Reasons = append(
			classification.Reasons,
			"forward guidance increased",
		)
		return classification
	}

	directCommercialAward := containsAny(
		title,
		"awarded",
		"lands",
		"purchase order",
		"selected by",
	) ||
		(containsWord(title, "wins") &&
			containsAny(title, "contract", "award", "order")) ||
		((containsWord(title, "receives") ||
			containsWord(title, "secures")) &&
			containsWord(title, "order")) ||
		containsWord(title, "contract") ||
		containsWord(title, "award")
	if directCommercialAward {
		if containsAny(
			text,
			"awarded",
			"wins",
			"lands",
			"largest",
			"million",
			"billion",
			"$",
		) {
			classification.Kind = "CONTRACT"
			classification.Strength = 0.90
			classification.Tradeable = true
			classification.Reasons = append(
				classification.Reasons,
				"quantified or explicitly material commercial award",
			)
			return classification
		}
	}

	earningsResult := containsAny(
		title,
		"reports results",
		"reports financial results",
		"reported quarterly",
		"earnings per share",
		" eps ",
		"earnings summary",
	) || (containsWord(title, "reports") &&
		containsAny(
			title,
			"results",
			"earnings",
			"quarter",
		))
	positiveResult := containsAny(
		text,
		"beats",
		"beat estimates",
		"higher than",
		"topped estimates",
		"raises guidance",
		"raised guidance",
		"increases guidance",
		"revenue rose",
		"revenue grew",
		"record revenue",
		"record results",
		"impressive",
	)
	if earningsResult && positiveResult {
		classification.Kind = "EARNINGS"
		classification.Strength = 0.90
		classification.Tradeable = true
		classification.Reasons = append(
			classification.Reasons,
			"reported earnings or revenue improvement",
		)
		if containsAny(
			text,
			"raises guidance",
			"raised guidance",
			"increases guidance",
		) {
			classification.Strength = 0.96
			classification.Reasons = append(
				classification.Reasons,
				"forward guidance increased",
			)
		}
		return classification
	}
	if containsAny(title, "beats eps", "beats quarterly eps") &&
		containsAny(text, "revenue", "guidance", "earnings") {
		classification.Kind = "EARNINGS"
		classification.Strength = 0.90
		classification.Tradeable = true
		classification.Reasons = append(
			classification.Reasons,
			"earnings beat",
		)
		if containsAny(text, "raises guidance", "raised guidance") {
			classification.Strength = 0.96
			classification.Reasons = append(
				classification.Reasons,
				"forward guidance increased",
			)
		}
		return classification
	}

	if containsAny(
		title,
		"schedule 13d",
		"beneficial ownership",
		"discloses stake",
		"acquired a stake",
		"acquires stake",
		"100% stake",
	) {
		classification.Kind = "OWNERSHIP"
		classification.Strength = 0.82
		classification.Tradeable = true
		classification.Reasons = append(
			classification.Reasons,
			"material ownership disclosure",
		)
		return classification
	}

	if containsAny(
		title,
		"shares are trading",
		"price target",
		"analyst rating",
	) {
		classification.Kind = "COMMENTARY"
		classification.Strength = 0.15
		classification.Reasons = append(
			classification.Reasons,
			"commentary rather than a new issuer event",
		)
		return classification
	}
	if strings.EqualFold(strings.TrimSpace(item.Sentiment), "positive") {
		classification.Strength = 0.35
		classification.Reasons = append(
			classification.Reasons,
			"positive provider sentiment without a hard catalyst",
		)
	}
	return classification
}

func containsWord(text, word string) bool {
	for start := 0; ; {
		index := strings.Index(text[start:], word)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !isAlphaNumeric(text[index-1])
		after := index + len(word)
		afterOK := after == len(text) || !isAlphaNumeric(text[after])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
	}
}

func isAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9'
}
