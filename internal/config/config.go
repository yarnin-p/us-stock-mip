package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultMassiveBaseURL = "https://api.massive.com"
const defaultWebullBaseURL = "https://api.webull.co.th"
const defaultAlpacaDataBaseURL = "https://data.alpaca.markets"
const defaultAlpacaNewsStreamURL = "wss://stream.data.alpaca.markets/v1beta1/news"

type Config struct {
	DatabaseURL                    string
	RedisURL                       string
	MassiveAPIKey                  string
	MassiveBaseURL                 string
	AlpacaNewsEnabled              bool
	AlpacaAPIKeyID                 string
	AlpacaAPISecretKey             string
	AlpacaDataBaseURL              string
	AlpacaNewsStreamURL            string
	WebullAppKey                   string
	WebullSecret                   string
	WebullAccountID                string
	WebullAccessToken              string
	WebullTokenFile                string
	WebullTokenExpires             int64
	WebullTokenStatus              string
	WebullBaseURL                  string
	WebullTradingBaseURL           string
	WebullMQTTURL                  string
	WebullAlgorithm                string
	SECUserAgent                   string
	SECBaseURL                     string
	LLMAPIKey                      string
	LLMBaseURL                     string
	LLMModel                       string
	HTTPTimeout                    time.Duration
	DBMaxConns                     int32
	DBMinConns                     int32
	LogLevel                       string
	TradingMode                    string
	MaxPositionValue               float64
	MaxGrossExposure               float64
	MaxCapitalAllocation           float64
	MaxDailyLoss                   float64
	MaxRiskPerTrade                float64
	PaperStartingCapital           float64
	ApprovalTTL                    time.Duration
	LossCooldown                   time.Duration
	TradingKillSwitch              bool
	TradingAllowedSessions         []string
	AutomaticTrading               bool
	AutoLiveEntriesEnabled         bool
	AutoMaxCandidates              int
	BoundaryMinRelativeVolume      float64
	FilingWatchPinnedTickers       []string
	StrategyRetentionRank          int
	StrategyRetentionGrace         time.Duration
	AutoRiskPerTrade               float64
	AutoRetryDelay                 time.Duration
	AutoEntryOrderTimeout          time.Duration
	ShadowTradingEnabled           bool
	ShadowMaxCandidates            int
	MarketNewsMonitorEnabled       bool
	MarketNewsMonitorInterval      time.Duration
	MarketNewsMonitorLookback      time.Duration
	MarketNewsMonitorLimit         int
	MarketNewsAlertThreshold       float64
	BoundaryShadowEnabled          bool
	BoundaryShadowMaxCandidates    int
	BoundaryShadowTotalNotionalTHB float64
	BoundaryShadowUSDTHB           float64
	BoundaryShadowNewsLookback     time.Duration
	BoundaryShadowMinCatalyst      float64
	BoundaryShadowMinVolume        float64
	BoundaryShadowMinPrice         float64
	BoundaryShadowMaxPrice         float64
	BoundaryShadowMaxSpread        float64
	BoundaryShadowQuoteMaxAge      time.Duration
	BoundaryShadowSignalMaxAge     time.Duration
	BoundaryShadowInterval         time.Duration
	BoundaryShadowStopLoss         float64
	BoundaryShadowTrailStart       float64
	BoundaryShadowTrailDistance    float64
	BoundaryShadowProfitLock       float64
	StrategyMinPullback            float64
	StrategyMaxPullback            float64
	StrategyReclaim                float64
	StrategyMinEntryHeadroom       float64
	StrategyStopLoss               float64
	StrategyTrailStart             float64
	StrategyTrailDistance          float64
	StrategyBreakEvenStart         float64
	StrategyBreakEvenBuffer        float64
	StrategyProfitLockStart        float64
	StrategyProfitLockFloor        float64
	StrategyPartialTPStart         float64
	StrategyPartialTPFraction      float64
	StrategyPartialTPMinShares     int
	StrategyExitFeeMinimum         float64
	StrategyExitFeePerShare        float64
	StrategySlippageReserve        float64
	StrategyMinimumNetProfit       float64
	StrategyReentryCooldown        time.Duration
	StrategyExitMomentumEnabled    bool
	StrategyMinExpectedProfit      float64
	StrategyBuyerPressure          float64
	StrategyMaxSpread              float64
	StrategyExitBuffer             float64
	StrategyQuoteMaxAge            time.Duration
	StrategyTradeQuoteMaxLag       time.Duration
	StrategyFlowWindow             time.Duration
	StrategyMinQuoteUpdates        int
	StrategyMinTradeTicks          int
	StrategyAggressiveBuyRatio     float64
	StrategyUptickRatio            float64
	StrategyAverageBookPressure    float64
	StrategyMinPriceVelocity       float64
	StrategyHoldTickers            []string
}

var tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

func Load() (Config, error) {
	return load(true)
}

// LoadDatabase loads shared application and PostgreSQL settings for commands
// that do not call Massive.
func LoadDatabase() (Config, error) {
	return load(false)
}

func LoadIntelligence() (Config, error) {
	config, err := load(true)
	if err != nil {
		return Config{}, err
	}
	if config.SECUserAgent == "" {
		return Config{}, errors.New("SEC_USER_AGENT is required")
	}
	return config, nil
}

func LoadWebull() (Config, error) {
	return loadWebull(true)
}

func LoadWebullAuth() (Config, error) {
	return loadWebull(false)
}

func loadWebull(requireToken bool) (Config, error) {
	config, err := load(false)
	if err != nil {
		return Config{}, err
	}
	config.HTTPTimeout, err = durationEnv("HTTP_TIMEOUT", config.HTTPTimeout)
	if err != nil {
		return Config{}, err
	}
	if config.WebullAppKey == "" || config.WebullSecret == "" {
		return Config{}, errors.New("WEBULL_APP_KEY and WEBULL_APP_SECRET are required")
	}
	if config.WebullAccessToken == "" && config.WebullTokenFile != "" {
		tokenFile, readErr := os.ReadFile(config.WebullTokenFile)
		if readErr != nil {
			if requireToken || !errors.Is(readErr, os.ErrNotExist) {
				return Config{}, fmt.Errorf("reading WEBULL_ACCESS_TOKEN_FILE: %w", readErr)
			}
		} else {
			lines := strings.Split(string(tokenFile), "\n")
			if len(lines) < 3 {
				return Config{}, errors.New("WEBULL_ACCESS_TOKEN_FILE is invalid")
			}
			config.WebullAccessToken = strings.TrimSpace(lines[0])
			config.WebullTokenExpires, err = strconv.ParseInt(
				strings.TrimSpace(lines[1]), 10, 64,
			)
			if err != nil {
				return Config{}, errors.New("WEBULL_ACCESS_TOKEN_FILE expiry is invalid")
			}
			config.WebullTokenStatus = strings.TrimSpace(lines[2])
		}
	}
	if requireToken && strings.HasSuffix(config.WebullBaseURL, ".co.th") &&
		config.WebullAccessToken == "" {
		return Config{}, errors.New(
			"WEBULL_ACCESS_TOKEN is required for Webull Thailand after 2FA",
		)
	}
	if config.WebullAlgorithm != "HMAC-SHA1" &&
		config.WebullAlgorithm != "HMAC-SHA256" {
		return Config{}, fmt.Errorf(
			"unsupported WEBULL_SIGNATURE_ALGORITHM %q",
			config.WebullAlgorithm,
		)
	}
	return config, nil
}

func LoadLLM() (Config, error) {
	config, err := load(false)
	if err != nil {
		return Config{}, err
	}
	config.HTTPTimeout, err = durationEnv("HTTP_TIMEOUT", config.HTTPTimeout)
	if err != nil {
		return Config{}, err
	}
	if config.LLMAPIKey == "" || config.LLMModel == "" {
		return Config{}, errors.New("LLM_API_KEY and LLM_MODEL are required")
	}
	return config, nil
}

func load(requireMassive bool) (Config, error) {
	config := Config{
		DatabaseURL:    strings.TrimSpace(os.Getenv("DATABASE_URL")),
		RedisURL:       strings.TrimSpace(os.Getenv("REDIS_URL")),
		MassiveAPIKey:  strings.TrimSpace(os.Getenv("MASSIVE_API_KEY")),
		MassiveBaseURL: envOrDefault("MASSIVE_BASE_URL", defaultMassiveBaseURL),
		AlpacaAPIKeyID: strings.TrimSpace(os.Getenv("APCA_API_KEY_ID")),
		AlpacaAPISecretKey: strings.TrimSpace(
			os.Getenv("APCA_API_SECRET_KEY"),
		),
		AlpacaDataBaseURL: envOrDefault(
			"ALPACA_DATA_BASE_URL",
			defaultAlpacaDataBaseURL,
		),
		AlpacaNewsStreamURL: envOrDefault(
			"ALPACA_NEWS_STREAM_URL",
			defaultAlpacaNewsStreamURL,
		),
		WebullAppKey:      strings.TrimSpace(os.Getenv("WEBULL_APP_KEY")),
		WebullSecret:      strings.TrimSpace(os.Getenv("WEBULL_APP_SECRET")),
		WebullAccountID:   strings.TrimSpace(os.Getenv("WEBULL_ACCOUNT_ID")),
		WebullAccessToken: strings.TrimSpace(os.Getenv("WEBULL_ACCESS_TOKEN")),
		WebullTokenFile:   strings.TrimSpace(os.Getenv("WEBULL_ACCESS_TOKEN_FILE")),
		WebullBaseURL:     envOrDefault("WEBULL_BASE_URL", defaultWebullBaseURL),
		WebullTradingBaseURL: envOrDefault(
			"WEBULL_TRADING_BASE_URL", defaultWebullBaseURL,
		),
		WebullMQTTURL:   envOrDefault("WEBULL_MQTT_URL", "wss://data-api.webull.co.th:8883/mqtt"),
		WebullAlgorithm: envOrDefault("WEBULL_SIGNATURE_ALGORITHM", "HMAC-SHA256"),
		SECUserAgent:    strings.TrimSpace(os.Getenv("SEC_USER_AGENT")),
		SECBaseURL:      envOrDefault("SEC_BASE_URL", "https://data.sec.gov"),
		LLMAPIKey:       strings.TrimSpace(os.Getenv("LLM_API_KEY")),
		LLMBaseURL:      envOrDefault("LLM_BASE_URL", "https://api.openai.com/v1"),
		LLMModel:        envOrDefault("LLM_MODEL", "gpt-5.6-luna"),
		HTTPTimeout:     15 * time.Second,
		LogLevel:        strings.ToLower(envOrDefault("LOG_LEVEL", "info")),
		TradingMode:     strings.ToLower(envOrDefault("TRADING_MODE", "paper")),
	}

	var err error
	if requireMassive {
		config.HTTPTimeout, err = durationEnv("HTTP_TIMEOUT", config.HTTPTimeout)
		if err != nil {
			return Config{}, err
		}
	}
	config.DBMaxConns, err = int32Env("DB_MAX_CONNS", 10)
	if err != nil {
		return Config{}, err
	}
	config.DBMinConns, err = int32Env("DB_MIN_CONNS", 2)
	if err != nil {
		return Config{}, err
	}
	config.MaxPositionValue, err = floatEnv("MAX_POSITION_VALUE", 5000)
	if err != nil {
		return Config{}, err
	}
	config.MaxGrossExposure, err = floatEnv("MAX_GROSS_EXPOSURE", 0)
	if err != nil {
		return Config{}, err
	}
	config.MaxCapitalAllocation, err = floatEnv(
		"MAX_CAPITAL_ALLOCATION", 0.20,
	)
	if err != nil {
		return Config{}, err
	}
	config.MaxDailyLoss, err = floatEnv("MAX_DAILY_LOSS", 500)
	if err != nil {
		return Config{}, err
	}
	config.MaxRiskPerTrade, err = floatEnv("MAX_RISK_PER_TRADE", 200)
	if err != nil {
		return Config{}, err
	}
	config.PaperStartingCapital, err = floatEnv(
		"PAPER_STARTING_CAPITAL", 100000,
	)
	if err != nil {
		return Config{}, err
	}
	config.ApprovalTTL, err = durationEnv("EXECUTION_APPROVAL_TTL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	config.LossCooldown, err = durationEnv("LOSS_COOLDOWN", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	config.TradingKillSwitch, err = boolEnv("TRADING_KILL_SWITCH", false)
	if err != nil {
		return Config{}, err
	}
	config.TradingAllowedSessions, err = sessionListEnv(
		"TRADING_ALLOWED_SESSIONS",
		"PRE_MARKET,REGULAR,AFTER_HOURS",
	)
	if err != nil {
		return Config{}, err
	}
	config.AutomaticTrading, err = boolEnv("AUTO_TRADING_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	config.AutoLiveEntriesEnabled, err = boolEnv(
		"AUTO_LIVE_ENTRIES_ENABLED",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	config.AutoMaxCandidates, err = intEnv("AUTO_MAX_CANDIDATES", 2)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryMinRelativeVolume, err = floatEnv(
		"BOUNDARY_MIN_RELATIVE_VOLUME", 0,
	)
	if err != nil {
		return Config{}, err
	}
	config.FilingWatchPinnedTickers, err = tickerListEnv(
		"FILING_WATCH_PINNED_TICKERS",
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyRetentionRank, err = intEnv("STRATEGY_RETENTION_RANK", 0)
	if err != nil {
		return Config{}, err
	}
	config.StrategyRetentionGrace, err = durationEnv(
		"STRATEGY_RETENTION_GRACE", 0,
	)
	if err != nil {
		return Config{}, err
	}
	config.AutoRiskPerTrade, err = floatEnv("AUTO_RISK_PER_TRADE", 100)
	if err != nil {
		return Config{}, err
	}
	config.AutoRetryDelay, err = durationEnv("AUTO_RETRY_DELAY", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	config.AutoEntryOrderTimeout, err = durationEnv(
		"AUTO_ENTRY_ORDER_TIMEOUT", 5*time.Second,
	)
	if err != nil {
		return Config{}, err
	}
	config.ShadowTradingEnabled, err = boolEnv(
		"SHADOW_TRADING_ENABLED", false,
	)
	if err != nil {
		return Config{}, err
	}
	config.ShadowMaxCandidates, err = intEnv(
		"SHADOW_MAX_CANDIDATES", 10,
	)
	if err != nil {
		return Config{}, err
	}
	config.MarketNewsMonitorEnabled, err = boolEnv(
		"MARKET_NEWS_MONITOR_ENABLED",
		true,
	)
	if err != nil {
		return Config{}, err
	}
	config.AlpacaNewsEnabled, err = boolEnv(
		"ALPACA_NEWS_ENABLED",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	config.MarketNewsMonitorInterval, err = durationEnv(
		"MARKET_NEWS_MONITOR_INTERVAL",
		time.Minute,
	)
	if err != nil {
		return Config{}, err
	}
	config.MarketNewsMonitorLookback, err = durationEnv(
		"MARKET_NEWS_MONITOR_LOOKBACK",
		24*time.Hour,
	)
	if err != nil {
		return Config{}, err
	}
	config.MarketNewsMonitorLimit, err = intEnv(
		"MARKET_NEWS_MONITOR_LIMIT",
		1000,
	)
	if err != nil {
		return Config{}, err
	}
	config.MarketNewsAlertThreshold, err = floatEnv(
		"MARKET_NEWS_ALERT_THRESHOLD",
		0.75,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowEnabled, err = boolEnv(
		"BOUNDARY_SHADOW_ENABLED",
		false,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowMaxCandidates, err = intEnv(
		"BOUNDARY_SHADOW_MAX_CANDIDATES",
		3,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowTotalNotionalTHB, err = floatEnv(
		"BOUNDARY_SHADOW_TOTAL_NOTIONAL_THB",
		100_000,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowUSDTHB, err = floatEnv(
		"BOUNDARY_SHADOW_USD_THB",
		33.6,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowNewsLookback, err = durationEnv(
		"BOUNDARY_SHADOW_NEWS_LOOKBACK",
		8*time.Hour,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowMinCatalyst, err = floatEnv(
		"BOUNDARY_SHADOW_MIN_CATALYST",
		0.75,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowMinVolume, err = floatEnv(
		"BOUNDARY_SHADOW_MIN_VOLUME",
		25_000,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowMinPrice, err = floatEnv(
		"BOUNDARY_SHADOW_MIN_PRICE",
		0.20,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowMaxPrice, err = floatEnv(
		"BOUNDARY_SHADOW_MAX_PRICE",
		100,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowMaxSpread, err = floatEnv(
		"BOUNDARY_SHADOW_MAX_SPREAD_PCT",
		0.03,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowQuoteMaxAge, err = durationEnv(
		"BOUNDARY_SHADOW_QUOTE_MAX_AGE",
		30*time.Second,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowSignalMaxAge, err = durationEnv(
		"BOUNDARY_SHADOW_SIGNAL_MAX_AGE",
		2*time.Minute,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowInterval, err = durationEnv(
		"BOUNDARY_SHADOW_EVALUATION_INTERVAL",
		5*time.Second,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowStopLoss, err = floatEnv(
		"BOUNDARY_SHADOW_STOP_LOSS_PCT",
		0.06,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowTrailStart, err = floatEnv(
		"BOUNDARY_SHADOW_TRAIL_START_PCT",
		0.08,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowTrailDistance, err = floatEnv(
		"BOUNDARY_SHADOW_TRAIL_DISTANCE_PCT",
		0.04,
	)
	if err != nil {
		return Config{}, err
	}
	config.BoundaryShadowProfitLock, err = floatEnv(
		"BOUNDARY_SHADOW_PROFIT_LOCK_FLOOR_PCT",
		0.015,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMinPullback, err = floatEnv("STRATEGY_MIN_PULLBACK_PCT", 0.02)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMaxPullback, err = floatEnv("STRATEGY_MAX_PULLBACK_PCT", 0.08)
	if err != nil {
		return Config{}, err
	}
	config.StrategyReclaim, err = floatEnv("STRATEGY_RECLAIM_PCT", 0.01)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMinEntryHeadroom, err = floatEnv(
		"STRATEGY_MIN_ENTRY_HEADROOM_PCT", 0.01,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyStopLoss, err = floatEnv("STRATEGY_STOP_LOSS_PCT", 0.04)
	if err != nil {
		return Config{}, err
	}
	config.StrategyTrailStart, err = floatEnv("STRATEGY_TRAIL_START_PCT", 0.08)
	if err != nil {
		return Config{}, err
	}
	config.StrategyTrailDistance, err = floatEnv(
		"STRATEGY_TRAIL_DISTANCE_PCT", 0.04,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyBreakEvenStart, err = floatEnv(
		"STRATEGY_BREAK_EVEN_START_PCT", 0.04,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyBreakEvenBuffer, err = floatEnv(
		"STRATEGY_BREAK_EVEN_BUFFER_PCT", 0.003,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyProfitLockStart, err = floatEnv(
		"STRATEGY_PROFIT_LOCK_START_PCT", 0.05,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyProfitLockFloor, err = floatEnv(
		"STRATEGY_PROFIT_LOCK_FLOOR_PCT", 0.015,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyPartialTPStart, err = floatEnv(
		"STRATEGY_PARTIAL_TP_START_PCT", 0.12,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyPartialTPFraction, err = floatEnv(
		"STRATEGY_PARTIAL_TP_FRACTION", 0.25,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyPartialTPMinShares, err = intEnv(
		"STRATEGY_PARTIAL_TP_MIN_SHARES", 4,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyExitFeeMinimum, err = floatEnv(
		"STRATEGY_EXIT_FEE_MINIMUM", 0.03,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyExitFeePerShare, err = floatEnv(
		"STRATEGY_EXIT_FEE_PER_SHARE", 0.006,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategySlippageReserve, err = floatEnv(
		"STRATEGY_SLIPPAGE_RESERVE_PCT", 0.005,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMinimumNetProfit, err = floatEnv(
		"STRATEGY_MINIMUM_NET_PROFIT", 0.01,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyReentryCooldown, err = durationEnv(
		"STRATEGY_REENTRY_COOLDOWN", time.Minute,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyExitMomentumEnabled, err = boolEnv(
		"STRATEGY_EXIT_MOMENTUM_ENABLED", false,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMinExpectedProfit, err = floatEnv(
		"STRATEGY_MIN_EXPECTED_NET_PROFIT", 0.25,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyBuyerPressure, err = floatEnv(
		"STRATEGY_MIN_BUYER_PRESSURE", 0.55,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMaxSpread, err = floatEnv("STRATEGY_MAX_SPREAD_PCT", 0.02)
	if err != nil {
		return Config{}, err
	}
	config.StrategyExitBuffer, err = floatEnv(
		"STRATEGY_EXIT_LIMIT_BUFFER_PCT", 0.002,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyQuoteMaxAge, err = durationEnv(
		"STRATEGY_QUOTE_MAX_AGE", 3*time.Second,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyTradeQuoteMaxLag, err = durationEnv(
		"STRATEGY_TRADE_QUOTE_MAX_LAG", 250*time.Millisecond,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyFlowWindow, err = durationEnv(
		"STRATEGY_ORDER_FLOW_WINDOW", 5*time.Second,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMinQuoteUpdates, err = intEnv(
		"STRATEGY_MIN_QUOTE_UPDATES", 2,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMinTradeTicks, err = intEnv(
		"STRATEGY_MIN_TRADE_TICKS", 3,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyAggressiveBuyRatio, err = floatEnv(
		"STRATEGY_MIN_AGGRESSIVE_BUY_RATIO", 0.55,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyUptickRatio, err = floatEnv(
		"STRATEGY_MIN_UPTICK_RATIO", 0.50,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyAverageBookPressure, err = floatEnv(
		"STRATEGY_MIN_AVERAGE_BOOK_PRESSURE", 0.50,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyMinPriceVelocity, err = floatEnv(
		"STRATEGY_MIN_PRICE_VELOCITY", 0,
	)
	if err != nil {
		return Config{}, err
	}
	config.StrategyHoldTickers, err = tickerListEnv("STRATEGY_HOLD_TICKERS")
	if err != nil {
		return Config{}, err
	}

	if config.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if requireMassive && config.MassiveAPIKey == "" {
		return Config{}, errors.New("MASSIVE_API_KEY is required")
	}
	if config.DBMaxConns < 1 {
		return Config{}, errors.New("DB_MAX_CONNS must be positive")
	}
	if config.DBMinConns < 0 {
		return Config{}, errors.New("DB_MIN_CONNS must not be negative")
	}
	if config.DBMinConns > config.DBMaxConns {
		return Config{}, errors.New("DB_MIN_CONNS must not exceed DB_MAX_CONNS")
	}
	switch config.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("unsupported LOG_LEVEL %q", config.LogLevel)
	}
	if config.TradingMode != "paper" && config.TradingMode != "live" {
		return Config{}, fmt.Errorf(
			"unsupported TRADING_MODE %q; use paper or live",
			config.TradingMode,
		)
	}
	if config.MaxPositionValue <= 0 {
		return Config{}, errors.New("MAX_POSITION_VALUE must be positive")
	}
	if config.MaxGrossExposure < 0 {
		return Config{}, errors.New("MAX_GROSS_EXPOSURE must not be negative")
	}
	if config.MaxCapitalAllocation <= 0 ||
		config.MaxCapitalAllocation > 1 {
		return Config{}, errors.New(
			"MAX_CAPITAL_ALLOCATION must be greater than zero and at most one",
		)
	}
	if config.MaxDailyLoss <= 0 {
		return Config{}, errors.New("MAX_DAILY_LOSS must be positive")
	}
	if config.MaxRiskPerTrade <= 0 {
		return Config{}, errors.New("MAX_RISK_PER_TRADE must be positive")
	}
	if config.PaperStartingCapital <= 0 {
		return Config{}, errors.New("PAPER_STARTING_CAPITAL must be positive")
	}
	if config.AutoMaxCandidates < 1 || config.AutoMaxCandidates > 10 {
		return Config{}, errors.New("AUTO_MAX_CANDIDATES must be between 1 and 10")
	}
	if config.BoundaryMinRelativeVolume < 0 ||
		config.BoundaryMinRelativeVolume > 1000 {
		return Config{}, errors.New(
			"BOUNDARY_MIN_RELATIVE_VOLUME must be between 0 and 1000",
		)
	}
	if config.StrategyRetentionRank < 0 ||
		config.StrategyRetentionRank > 100 {
		return Config{}, errors.New(
			"STRATEGY_RETENTION_RANK must be between 0 and 100",
		)
	}
	if config.StrategyRetentionGrace < 0 ||
		config.StrategyRetentionGrace > time.Hour {
		return Config{}, errors.New(
			"STRATEGY_RETENTION_GRACE must not exceed one hour",
		)
	}
	if config.ShadowMaxCandidates < 1 || config.ShadowMaxCandidates > 10 {
		return Config{}, errors.New("SHADOW_MAX_CANDIDATES must be between 1 and 10")
	}
	if config.MarketNewsMonitorInterval < 30*time.Second ||
		config.MarketNewsMonitorInterval > 30*time.Minute ||
		config.MarketNewsMonitorLookback < time.Hour ||
		config.MarketNewsMonitorLookback > 7*24*time.Hour ||
		config.MarketNewsMonitorLimit < 1 ||
		config.MarketNewsMonitorLimit > 1000 ||
		config.MarketNewsAlertThreshold <= 0 ||
		config.MarketNewsAlertThreshold > 1 {
		return Config{}, errors.New(
			"invalid all-market news monitor configuration",
		)
	}
	if config.AlpacaNewsEnabled &&
		(config.AlpacaAPIKeyID == "" || config.AlpacaAPISecretKey == "") {
		return Config{}, errors.New(
			"ALPACA_NEWS_ENABLED requires APCA_API_KEY_ID and APCA_API_SECRET_KEY",
		)
	}
	if config.BoundaryShadowMaxCandidates < 1 ||
		config.BoundaryShadowMaxCandidates > 3 ||
		config.BoundaryShadowTotalNotionalTHB <= 0 ||
		config.BoundaryShadowUSDTHB <= 0 ||
		config.BoundaryShadowNewsLookback < time.Hour ||
		config.BoundaryShadowNewsLookback > 24*time.Hour ||
		config.BoundaryShadowMinCatalyst <= 0 ||
		config.BoundaryShadowMinCatalyst > 1 ||
		config.BoundaryShadowMinVolume < 0 ||
		config.BoundaryShadowMinPrice <= 0 ||
		config.BoundaryShadowMaxPrice <= config.BoundaryShadowMinPrice ||
		config.BoundaryShadowMaxSpread <= 0 ||
		config.BoundaryShadowMaxSpread >= 1 ||
		config.BoundaryShadowQuoteMaxAge <= 0 ||
		config.BoundaryShadowSignalMaxAge <= 0 ||
		config.BoundaryShadowInterval < time.Second ||
		config.BoundaryShadowInterval > time.Minute ||
		config.BoundaryShadowStopLoss <= 0 ||
		config.BoundaryShadowStopLoss >= 1 ||
		config.BoundaryShadowTrailStart <= 0 ||
		config.BoundaryShadowTrailStart >= 1 ||
		config.BoundaryShadowTrailDistance <= 0 ||
		config.BoundaryShadowTrailDistance >= 1 ||
		config.BoundaryShadowProfitLock <= 0 ||
		config.BoundaryShadowProfitLock >=
			config.BoundaryShadowTrailStart {
		return Config{}, errors.New(
			"invalid AH boundary shadow configuration",
		)
	}
	if config.AutoRiskPerTrade <= 0 ||
		config.AutoRiskPerTrade > config.MaxRiskPerTrade {
		return Config{}, errors.New(
			"AUTO_RISK_PER_TRADE must be positive and not exceed MAX_RISK_PER_TRADE",
		)
	}
	if config.StrategyFlowWindow <= 0 ||
		config.StrategyTradeQuoteMaxLag <= 0 ||
		config.StrategyTradeQuoteMaxLag > config.StrategyQuoteMaxAge ||
		config.StrategyMinQuoteUpdates < 0 ||
		config.StrategyMinTradeTicks < 0 ||
		config.StrategyAggressiveBuyRatio < 0 ||
		config.StrategyAggressiveBuyRatio > 1 ||
		config.StrategyUptickRatio < 0 ||
		config.StrategyUptickRatio > 1 ||
		config.StrategyAverageBookPressure < 0 ||
		config.StrategyAverageBookPressure > 1 ||
		config.StrategyMinPriceVelocity < -1 ||
		config.StrategyMinPriceVelocity > 1 {
		return Config{}, errors.New("invalid strategy order-flow configuration")
	}
	if config.StrategyMinPullback <= 0 ||
		config.StrategyMaxPullback <= config.StrategyMinPullback ||
		config.StrategyMaxPullback >= 1 ||
		config.StrategyReclaim <= 0 || config.StrategyReclaim >= 1 ||
		config.StrategyMinEntryHeadroom < 0 ||
		config.StrategyMinEntryHeadroom >= 1 ||
		config.StrategyStopLoss <= 0 || config.StrategyStopLoss >= 1 ||
		config.StrategyTrailStart <= 0 || config.StrategyTrailStart >= 1 ||
		config.StrategyTrailDistance <= 0 || config.StrategyTrailDistance >= 1 ||
		config.StrategyBreakEvenStart <= 0 ||
		config.StrategyBreakEvenStart >= config.StrategyProfitLockStart ||
		config.StrategyBreakEvenBuffer <= 0 ||
		config.StrategyBreakEvenBuffer >= config.StrategyProfitLockFloor ||
		config.StrategyProfitLockStart >= config.StrategyTrailStart ||
		config.StrategyProfitLockFloor >= config.StrategyProfitLockStart ||
		config.StrategyPartialTPStart <= config.StrategyTrailStart ||
		config.StrategyPartialTPStart >= 1 ||
		config.StrategyPartialTPFraction <= 0 ||
		config.StrategyPartialTPFraction >= 1 ||
		config.StrategyPartialTPMinShares < 2 ||
		config.StrategyExitFeeMinimum < 0 ||
		config.StrategyExitFeePerShare < 0 ||
		config.StrategySlippageReserve < 0 ||
		config.StrategySlippageReserve >= 1 ||
		config.StrategyMinimumNetProfit < 0 ||
		config.StrategyReentryCooldown <= 0 ||
		config.StrategyMinExpectedProfit < 0 ||
		config.StrategyBuyerPressure < 0 || config.StrategyBuyerPressure > 1 ||
		config.StrategyMaxSpread <= 0 || config.StrategyMaxSpread >= 1 ||
		config.StrategyExitBuffer < 0 || config.StrategyExitBuffer >= 1 {
		return Config{}, errors.New("invalid autonomous strategy configuration")
	}

	return config, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	rawValue := strings.TrimSpace(os.Getenv(name))
	if rawValue == "" {
		return fallback, nil
	}

	value, err := time.ParseDuration(rawValue)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return value, nil
}

func int32Env(name string, fallback int32) (int32, error) {
	rawValue := strings.TrimSpace(os.Getenv(name))
	if rawValue == "" {
		return fallback, nil
	}

	value, err := strconv.ParseInt(rawValue, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", name, err)
	}
	return int32(value), nil
}

func intEnv(name string, fallback int) (int, error) {
	rawValue := strings.TrimSpace(os.Getenv(name))
	if rawValue == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(rawValue)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", name, err)
	}
	return value, nil
}

func floatEnv(name string, fallback float64) (float64, error) {
	rawValue := strings.TrimSpace(os.Getenv(name))
	if rawValue == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", name, err)
	}
	return value, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	rawValue := strings.TrimSpace(os.Getenv(name))
	if rawValue == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(rawValue)
	if err != nil {
		return false, fmt.Errorf("parsing %s: %w", name, err)
	}
	return value, nil
}

func sessionListEnv(name, fallback string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		raw = fallback
	}
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		session := strings.ToUpper(strings.TrimSpace(part))
		switch session {
		case "OVERNIGHT", "PRE_MARKET", "REGULAR", "AFTER_HOURS":
		default:
			return nil, fmt.Errorf("unsupported %s session %q", name, part)
		}
		if _, exists := seen[session]; exists {
			continue
		}
		seen[session] = struct{}{}
		result = append(result, session)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s must include at least one session", name)
	}
	return result, nil
}

func tickerListEnv(name string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		ticker := strings.ToUpper(strings.TrimSpace(part))
		if !tickerPattern.MatchString(ticker) {
			return nil, fmt.Errorf("invalid ticker %q in %s", part, name)
		}
		if _, exists := seen[ticker]; exists {
			continue
		}
		seen[ticker] = struct{}{}
		result = append(result, ticker)
	}
	return result, nil
}
