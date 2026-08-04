package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/config"
)

func TestLoad_UsesDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://mip:secret@localhost:5432/mip?sslmode=disable")
	t.Setenv("MASSIVE_API_KEY", "test-key")
	t.Setenv("MASSIVE_BASE_URL", "")
	t.Setenv("HTTP_TIMEOUT", "")
	t.Setenv("DB_MAX_CONNS", "")
	t.Setenv("DB_MIN_CONNS", "")
	t.Setenv("LOG_LEVEL", "")

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.MassiveBaseURL != "https://api.massive.com" {
		t.Errorf("MassiveBaseURL = %q", got.MassiveBaseURL)
	}
	if got.HTTPTimeout != 15*time.Second {
		t.Errorf("HTTPTimeout = %s", got.HTTPTimeout)
	}
	if got.DBMaxConns != 10 || got.DBMinConns != 2 {
		t.Errorf("pool = %d/%d, want 10/2", got.DBMaxConns, got.DBMinConns)
	}
	if got.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", got.LogLevel)
	}
	if got.TradingMode != "paper" || got.PaperStartingCapital != 100000 {
		t.Errorf("execution defaults = %q/%v", got.TradingMode, got.PaperStartingCapital)
	}
	if len(got.TradingAllowedSessions) != 3 ||
		got.TradingAllowedSessions[0] != "PRE_MARKET" ||
		got.TradingAllowedSessions[1] != "REGULAR" ||
		got.TradingAllowedSessions[2] != "AFTER_HOURS" {
		t.Errorf(
			"TradingAllowedSessions = %#v, want supported Webull sessions",
			got.TradingAllowedSessions,
		)
	}
}

func TestLoadDatabaseRejectsUnsafeTradingConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("TRADING_MODE", "automatic")

	if _, err := config.LoadDatabase(); err == nil {
		t.Fatal("LoadDatabase() error = nil, want trading mode validation error")
	}
}

func TestLoadDatabaseRejectsIncompleteAlpacaNewsCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("ALPACA_NEWS_ENABLED", "true")
	t.Setenv("APCA_API_KEY_ID", "paper-key")
	t.Setenv("APCA_API_SECRET_KEY", "")

	if _, err := config.LoadDatabase(); err == nil {
		t.Fatal("LoadDatabase() error = nil, want Alpaca credential error")
	}
}

func TestLoadDatabaseReadsAutonomousStrategyConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("AUTO_TRADING_ENABLED", "true")
	t.Setenv("AUTO_LIVE_ENTRIES_ENABLED", "true")
	t.Setenv("AUTO_MAX_CANDIDATES", "2")
	t.Setenv("AUTO_RISK_PER_TRADE", "125")
	t.Setenv("SHADOW_TRADING_ENABLED", "true")
	t.Setenv("SHADOW_MAX_CANDIDATES", "8")
	t.Setenv("MARKET_NEWS_MONITOR_ENABLED", "true")
	t.Setenv("MARKET_NEWS_MONITOR_INTERVAL", "1m")
	t.Setenv("ALPACA_NEWS_ENABLED", "true")
	t.Setenv("APCA_API_KEY_ID", "paper-key")
	t.Setenv("APCA_API_SECRET_KEY", "paper-secret")
	t.Setenv("ALPACA_DATA_BASE_URL", "https://data.example.test")
	t.Setenv("ALPACA_NEWS_STREAM_URL", "wss://stream.example.test/news")
	t.Setenv("BOUNDARY_SHADOW_ENABLED", "true")
	t.Setenv("BOUNDARY_SHADOW_MAX_CANDIDATES", "3")
	t.Setenv("BOUNDARY_SHADOW_TOTAL_NOTIONAL_THB", "100000")
	t.Setenv("BOUNDARY_SHADOW_USD_THB", "33.6")
	t.Setenv("BOUNDARY_SHADOW_NEWS_LOOKBACK", "6h")
	t.Setenv("TRADING_ALLOWED_SESSIONS", "regular")
	t.Setenv("STRATEGY_STOP_LOSS_PCT", "0.05")
	t.Setenv("STRATEGY_MIN_ENTRY_HEADROOM_PCT", "0.015")
	t.Setenv("MAX_GROSS_EXPOSURE", "85")
	t.Setenv("STRATEGY_HOLD_TICKERS", "stfs, OPK,stfs")

	got, err := config.LoadDatabase()
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutomaticTrading || !got.AutoLiveEntriesEnabled ||
		got.AutoMaxCandidates != 2 ||
		got.AutoRiskPerTrade != 125 || got.StrategyStopLoss != 0.05 ||
		!got.ShadowTradingEnabled || got.ShadowMaxCandidates != 8 ||
		!got.MarketNewsMonitorEnabled ||
		got.MarketNewsMonitorInterval != time.Minute ||
		!got.AlpacaNewsEnabled ||
		got.AlpacaAPIKeyID != "paper-key" ||
		got.AlpacaAPISecretKey != "paper-secret" ||
		got.AlpacaDataBaseURL != "https://data.example.test" ||
		got.AlpacaNewsStreamURL != "wss://stream.example.test/news" ||
		!got.BoundaryShadowEnabled ||
		got.BoundaryShadowMaxCandidates != 3 ||
		got.BoundaryShadowTotalNotionalTHB != 100000 ||
		got.BoundaryShadowUSDTHB != 33.6 ||
		got.BoundaryShadowNewsLookback != 6*time.Hour ||
		got.StrategyMinEntryHeadroom != 0.015 ||
		got.MaxGrossExposure != 85 ||
		len(got.TradingAllowedSessions) != 1 ||
		got.TradingAllowedSessions[0] != "REGULAR" ||
		len(got.StrategyHoldTickers) != 2 ||
		got.StrategyHoldTickers[0] != "STFS" ||
		got.StrategyHoldTickers[1] != "OPK" {
		t.Fatalf("autonomous strategy config = %+v", got)
	}
}

func TestLoadDatabaseRejectsInvalidShadowCandidateLimit(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("SHADOW_MAX_CANDIDATES", "11")

	if _, err := config.LoadDatabase(); err == nil {
		t.Fatal("LoadDatabase() error = nil, want shadow candidate validation error")
	}
}

func TestLoadDatabaseRejectsInvalidHoldTicker(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("STRATEGY_HOLD_TICKERS", "STFS,not valid")

	if _, err := config.LoadDatabase(); err == nil {
		t.Fatal("LoadDatabase() error = nil, want invalid hold ticker error")
	}
}

func TestLoadDatabaseSupportsEveryLiveTradingSession(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("TRADING_MODE", "live")
	t.Setenv(
		"TRADING_ALLOWED_SESSIONS",
		"OVERNIGHT,PRE_MARKET,REGULAR,AFTER_HOURS",
	)

	got, err := config.LoadDatabase()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TradingAllowedSessions) != 4 {
		t.Fatalf("TradingAllowedSessions = %#v", got.TradingAllowedSessions)
	}
}

func TestLoad_RejectsMissingSecrets(t *testing.T) {
	tests := []struct {
		name        string
		databaseURL string
		apiKey      string
	}{
		{name: "missing database URL", apiKey: "test-key"},
		{name: "missing Massive API key", databaseURL: "postgres://localhost/mip"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", test.databaseURL)
			t.Setenv("MASSIVE_API_KEY", test.apiKey)

			if _, err := config.Load(); err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
		})
	}
}

func TestLoad_RejectsInvalidPool(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("MASSIVE_API_KEY", "test-key")
	t.Setenv("DB_MIN_CONNS", "8")
	t.Setenv("DB_MAX_CONNS", "4")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want pool validation error")
	}
}

func TestLoadDatabase_DoesNotRequireMassiveAPIKey(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("MASSIVE_API_KEY", "")
	t.Setenv("HTTP_TIMEOUT", "not-a-duration")

	got, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("LoadDatabase() error = %v", err)
	}
	if got.DatabaseURL != "postgres://localhost/mip" {
		t.Errorf("DatabaseURL = %q", got.DatabaseURL)
	}
}

func TestLoadWebullRequiresCredentialsAndSupportsSHA256(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("WEBULL_APP_KEY", "key")
	t.Setenv("WEBULL_APP_SECRET", "secret")
	t.Setenv("WEBULL_ACCESS_TOKEN", "token")
	t.Setenv("WEBULL_SIGNATURE_ALGORITHM", "HMAC-SHA256")

	got, err := config.LoadWebull()
	if err != nil {
		t.Fatal(err)
	}
	if got.WebullAlgorithm != "HMAC-SHA256" {
		t.Errorf("algorithm = %q", got.WebullAlgorithm)
	}
	if got.WebullTradingBaseURL != "https://api.webull.co.th" {
		t.Errorf("trading base URL = %q", got.WebullTradingBaseURL)
	}
}

func TestLoadIntelligenceRequiresSECUserAgent(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("MASSIVE_API_KEY", "test-key")
	t.Setenv("SEC_USER_AGENT", "")

	if _, err := config.LoadIntelligence(); err == nil {
		t.Fatal("LoadIntelligence() error = nil, want SEC user-agent error")
	}
}

func TestLoadWebullReadsAccessTokenFile(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("WEBULL_APP_KEY", "key")
	t.Setenv("WEBULL_APP_SECRET", "secret")
	t.Setenv("WEBULL_BASE_URL", "https://api.webull.co.th")
	t.Setenv("WEBULL_ACCESS_TOKEN", "")
	tokenFile := t.TempDir() + "/token.txt"
	if err := os.WriteFile(tokenFile, []byte("file-token\n123\nNORMAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBULL_ACCESS_TOKEN_FILE", tokenFile)

	got, err := config.LoadWebull()
	if err != nil {
		t.Fatal(err)
	}
	if got.WebullAccessToken != "file-token" {
		t.Fatal("access token was not loaded from file")
	}
	if got.WebullTokenExpires != 123 || got.WebullTokenStatus != "NORMAL" {
		t.Fatalf("token metadata = %d/%s", got.WebullTokenExpires, got.WebullTokenStatus)
	}
}

func TestLoadLLMRequiresCredentialAndUsesDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/mip")
	t.Setenv("LLM_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_BASE_URL", "")

	got, err := config.LoadLLM()
	if err != nil {
		t.Fatal(err)
	}
	if got.LLMModel != "gpt-5.6-luna" ||
		got.LLMBaseURL != "https://api.openai.com/v1" {
		t.Fatalf("LLM config = %+v", got)
	}
}
