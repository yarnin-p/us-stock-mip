package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/momentum-intelligence-platform/mip/internal/alpaca"
	"github.com/momentum-intelligence-platform/mip/internal/automation"
	"github.com/momentum-intelligence-platform/mip/internal/catalyst"
	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
	"github.com/momentum-intelligence-platform/mip/internal/discovery"
	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/market"
	"github.com/momentum-intelligence-platform/mip/internal/massive"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
	"github.com/momentum-intelligence-platform/mip/internal/ranking"
	"github.com/momentum-intelligence-platform/mip/internal/scanner"
	secclient "github.com/momentum-intelligence-platform/mip/internal/sec"
	"github.com/momentum-intelligence-platform/mip/internal/strategy"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func runServe(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var address, allowedOrigin, quoteTickers string
	flags.StringVar(
		&address, "addr", "127.0.0.1:8080",
		"dashboard API listen address",
	)
	flags.StringVar(
		&allowedOrigin, "allowed-origin", "http://localhost:3001",
		"dashboard browser origin",
	)
	flags.StringVar(
		&quoteTickers, "quote-tickers", "",
		"optional Webull QUOTE symbols to keep live",
	)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing serve flags: %w", err)
	}
	if strings.TrimSpace(address) == "" {
		return errors.New("serve address is required")
	}

	appConfig, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()

	logger := configuredLogger(appConfig.LogLevel)
	store := postgres.NewStore(pool)
	reclassified, err := store.ReclassifyRecentNews(
		ctx,
		time.Now().UTC(),
		24*time.Hour,
	)
	if err != nil {
		return fmt.Errorf("reclassifying recent news: %w", err)
	}
	if reclassified > 0 {
		logger.Info(
			"recent news classifications refreshed",
			"articles", reclassified,
		)
	}
	var repository dashboard.Repository = store
	var cachedRepository *dashboard.CachedRepository
	var redisPing func(context.Context) error
	if appConfig.RedisURL != "" {
		redisOptions, err := redis.ParseURL(appConfig.RedisURL)
		if err != nil {
			return fmt.Errorf("parsing REDIS_URL: %w", err)
		}
		redisClient := redis.NewClient(redisOptions)
		if err := redisClient.Ping(ctx).Err(); err != nil {
			_ = redisClient.Close()
			return fmt.Errorf("connecting to Redis: %w", err)
		}
		defer func() {
			if err := redisClient.Close(); err != nil {
				logger.Warn("closing Redis client", "error", err)
			}
		}()
		cachedRepository = dashboard.NewCachedRepository(store, redisClient)
		repository = cachedRepository
		redisPing = func(ctx context.Context) error {
			return redisClient.Ping(ctx).Err()
		}
	}
	eventHub := dashboard.NewEventHub(ctx, store, func(event dashboard.Event) {
		if cachedRepository == nil {
			return
		}
		if err := cachedRepository.Invalidate(ctx, event.Scope); err != nil &&
			ctx.Err() == nil {
			logger.Warn(
				"invalidating dashboard cache",
				"scope", event.Scope, "error", err,
			)
		}
	})
	executionService, err := buildExecutionService(appConfig, store)
	if err != nil {
		return err
	}
	strategyHandlers, strategyPlans, err := startAutonomousStrategy(
		ctx, appConfig, store, executionService, logger,
	)
	if err != nil {
		return err
	}
	if appConfig.ShadowTradingEnabled &&
		appConfig.TradingMode == string(execution.ModeLive) {
		shadowConfig := appConfig
		shadowConfig.TradingMode = string(execution.ModeShadow)
		shadowConfig.AutomaticTrading = true
		shadowConfig.AutoMaxCandidates = appConfig.ShadowMaxCandidates
		shadowExecutionService, shadowErr := buildExecutionService(
			shadowConfig,
			store,
		)
		if shadowErr != nil {
			return fmt.Errorf("creating shadow execution service: %w", shadowErr)
		}
		shadowHandlers, _, shadowErr := startAutonomousStrategy(
			ctx,
			shadowConfig,
			store,
			shadowExecutionService,
			logger,
		)
		if shadowErr != nil {
			return fmt.Errorf("starting shadow strategy: %w", shadowErr)
		}
		strategyHandlers = combineAutonomousStrategyHandlers(
			strategyHandlers,
			func(eventType string, err error) {
				logger.Error(
					"shadow strategy handler failed",
					"event_type", eventType,
					"error", err,
				)
			},
			shadowHandlers,
		)
		logger.Info(
			"parallel shadow trading started",
			"max_candidates", shadowConfig.AutoMaxCandidates,
			"broker_calls", false,
		)
	}
	if appConfig.BoundaryShadowEnabled {
		boundaryHandlers, boundaryErr := startBoundaryShadow(
			ctx,
			appConfig,
			store,
			logger,
		)
		if boundaryErr != nil {
			return boundaryErr
		}
		strategyHandlers = combineAutonomousStrategyHandlers(
			strategyHandlers,
			func(eventType string, err error) {
				logger.Error(
					"AH boundary shadow handler failed",
					"event_type", eventType,
					"error", err,
				)
			},
			boundaryHandlers,
		)
	}
	runtimeHealth := newRuntimeHealthRegistry()
	handler := dashboard.NewHandler(repository, dashboard.Options{
		AllowedOrigin:      allowedOrigin,
		Logger:             logger,
		Events:             eventHub,
		RedisPing:          redisPing,
		Execution:          executionService,
		LiveEntriesEnabled: appConfig.AutoLiveEntriesEnabled,
		StrategyPlans:      strategyPlans,
		SpikeWatcher:       store,
		NewsCatalysts:      store,
		RuntimeHealth:      runtimeHealth.Snapshot,
	})
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// SSE responses are intentionally long-lived.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	tickers := normalizedTickers(quoteTickers)
	if err := startMarketNewsMonitor(
		ctx,
		appConfig,
		store,
		logger,
		runtimeHealth,
	); err != nil {
		return err
	}
	if err := startCurrentFilingMonitor(
		ctx,
		appConfig,
		store,
		logger,
	); err != nil {
		return err
	}
	if appConfig.WebullAppKey != "" && appConfig.WebullSecret != "" {
		if err := startBookStream(
			ctx,
			store,
			tickers,
			strategyHandlers,
			tradingSessionConfigured(
				appConfig.TradingAllowedSessions,
				market.SessionOvernight,
			),
			logger,
		); err != nil {
			return err
		}
	} else {
		logger.Warn("Webull credentials absent; live book stream disabled")
	}
	if err := startCandidateEnrichment(
		ctx,
		appConfig,
		store,
		logger,
	); err != nil {
		return err
	}
	startFeedHealthMonitor(ctx, store, logger)
	startBrokerSync(ctx, appConfig, store, logger)
	if err := startDailyScheduler(ctx, appConfig, store, logger); err != nil {
		return err
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("dashboard API listening", "address", address)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutting down dashboard API: %w", err)
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serving dashboard API: %w", err)
	}
}

func buildExecutionService(
	appConfig config.Config, store *postgres.Store,
) (*execution.Service, error) {
	mode := execution.Mode(appConfig.TradingMode)
	paperAdapter, err := execution.NewPaperAdapterWithFees(
		appConfig.StrategyExitFeeMinimum,
		appConfig.StrategyExitFeePerShare,
	)
	if err != nil {
		return nil, fmt.Errorf("creating paper execution adapter: %w", err)
	}
	var liveAdapter execution.BrokerAdapter
	if mode == execution.ModeLive {
		webullConfig, err := config.LoadWebull()
		if err != nil {
			return nil, fmt.Errorf(
				"live execution requires valid Webull configuration: %w", err,
			)
		}
		client, err := webull.NewClient(
			webullConfig.WebullAppKey, webullConfig.WebullSecret,
			webull.WithBaseURL(webullConfig.WebullTradingBaseURL),
			webull.WithAlgorithm(webullConfig.WebullAlgorithm),
			webull.WithAccessToken(webullConfig.WebullAccessToken),
			webull.WithHTTPClient(&http.Client{Timeout: webullConfig.HTTPTimeout}),
		)
		if err != nil {
			return nil, fmt.Errorf("creating live execution adapter: %w", err)
		}
		liveAdapter = client
	}
	service, err := execution.NewService(store, execution.ServiceOptions{
		Mode: mode, AutomaticTrading: appConfig.AutomaticTrading,
		LiveAccountID: appConfig.WebullAccountID,
		PaperAdapter:  paperAdapter,
		Limits: execution.Limits{
			MaxPositionValue:     appConfig.MaxPositionValue,
			MaxGrossExposure:     appConfig.MaxGrossExposure,
			MaxCapitalAllocation: appConfig.MaxCapitalAllocation,
			MaxDailyLoss:         appConfig.MaxDailyLoss,
			MaxRiskPerTrade:      appConfig.MaxRiskPerTrade,
			AllowedSessions:      appConfig.TradingAllowedSessions,
			DefaultBuyingPower:   appConfig.PaperStartingCapital,
			ApprovalTTL:          appConfig.ApprovalTTL,
			LossCooldown:         appConfig.LossCooldown,
			KillSwitch:           appConfig.TradingKillSwitch,
		},
		LiveAdapter: liveAdapter,
	})
	if err != nil {
		return nil, fmt.Errorf("creating execution service: %w", err)
	}
	return service, nil
}

func startMarketNewsMonitor(
	ctx context.Context,
	appConfig config.Config,
	store *postgres.Store,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
	health *runtimeHealthRegistry,
) error {
	if !appConfig.MarketNewsMonitorEnabled {
		logger.Info("all-market news monitor disabled")
		return nil
	}
	var alpacaClient *alpaca.NewsClient
	if appConfig.AlpacaNewsEnabled {
		var err error
		alpacaClient, err = alpaca.NewNewsClient(
			appConfig.AlpacaAPIKeyID,
			appConfig.AlpacaAPISecretKey,
			alpaca.WithRESTBaseURL(appConfig.AlpacaDataBaseURL),
			alpaca.WithStreamURL(appConfig.AlpacaNewsStreamURL),
			alpaca.WithHTTPClient(&http.Client{
				Timeout: appConfig.HTTPTimeout,
			}),
		)
		if err != nil {
			return fmt.Errorf("creating Alpaca news client: %w", err)
		}
	}

	var (
		primarySource catalyst.MarketNewsSource
		primaryName   string
	)
	if strings.TrimSpace(appConfig.MassiveAPIKey) != "" {
		client, err := massive.NewClient(
			appConfig.MassiveAPIKey,
			massive.WithBaseURL(appConfig.MassiveBaseURL),
			massive.WithHTTPClient(&http.Client{
				Timeout: appConfig.HTTPTimeout,
			}),
		)
		if err != nil {
			return fmt.Errorf("creating all-market news client: %w", err)
		}
		primarySource = client
		primaryName = "massive_reference"
	} else if alpacaClient != nil {
		primarySource = alpacaClient
		primaryName = "alpaca_rest"
	} else {
		return errors.New(
			"market news monitor requires Massive or Alpaca credentials",
		)
	}
	monitor, err := catalyst.NewMonitor(
		primarySource,
		store,
		catalyst.MonitorConfig{
			Interval:       appConfig.MarketNewsMonitorInterval,
			Lookback:       appConfig.MarketNewsMonitorLookback,
			Limit:          appConfig.MarketNewsMonitorLimit,
			AlertThreshold: appConfig.MarketNewsAlertThreshold,
		},
	)
	if err != nil {
		return fmt.Errorf("creating all-market news monitor: %w", err)
	}
	health.Set(newsComponentHealth(
		newsProviderName(primaryName),
		"WAITING",
		newsProviderDetail(primaryName),
		time.Time{},
		nil,
	))
	go monitor.Run(
		ctx,
		func(report catalyst.MonitorReport) {
			health.Set(newsComponentHealth(
				newsProviderName(primaryName),
				"CONNECTED",
				newsProviderDetail(primaryName),
				report.CompletedAt,
				nil,
			))
			logger.Info(
				"all-market news synchronized",
				"articles", report.Articles,
				"tickers", report.Tickers,
				"strong_catalysts", report.StrongCatalysts,
				"completed_at", report.CompletedAt,
				"provider", primaryName,
			)
		},
		func(err error) {
			health.Set(newsComponentHealth(
				newsProviderName(primaryName),
				"DISCONNECTED",
				"Synchronization failed",
				time.Now().UTC(),
				nil,
			))
			logger.Error(
				"all-market news synchronization failed",
				"provider", primaryName,
				"error", err,
			)
		},
	)
	logger.Info(
		"all-market news monitor started",
		"provider", primaryName,
		"interval", appConfig.MarketNewsMonitorInterval,
		"lookback", appConfig.MarketNewsMonitorLookback,
		"limit", appConfig.MarketNewsMonitorLimit,
	)
	if alpacaClient != nil {
		health.Set(newsComponentHealth(
			"Alpaca News Stream",
			"WAITING",
			"Realtime WebSocket connecting",
			time.Time{},
			nil,
		))
		startAlpacaNewsStream(
			ctx,
			alpacaClient,
			monitor,
			logger,
			health,
		)
		if primaryName != "alpaca_rest" {
			startAlpacaNewsBackfill(
				ctx,
				alpacaClient,
				monitor,
				appConfig,
				logger,
				health,
			)
		}
		logger.Info(
			"Alpaca realtime news started",
			"stream_url", appConfig.AlpacaNewsStreamURL,
			"rest_backfill", primaryName != "alpaca_rest",
		)
	}
	return nil
}

func startAlpacaNewsStream(
	ctx context.Context,
	client *alpaca.NewsClient,
	monitor *catalyst.Monitor,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
	health *runtimeHealthRegistry,
) {
	go func() {
		backoff := time.Second
		for {
			startedAt := time.Now()
			err := client.StreamWithReady(
				ctx,
				func(
					handlerContext context.Context,
					items []intelligence.TickerNewsItem,
				) error {
					now := time.Now().UTC()
					report, err := monitor.Ingest(
						handlerContext,
						now,
						items,
					)
					if err != nil {
						return err
					}
					if report.Articles == 0 {
						return nil
					}
					latency := newsLatency(now, items)
					latencyMS := latency.Milliseconds()
					health.Set(newsComponentHealth(
						"Alpaca News Stream",
						"CONNECTED",
						"Realtime WebSocket",
						now,
						&latencyMS,
					))
					logger.Info(
						"Alpaca realtime news ingested",
						"articles", report.Articles,
						"tickers", report.Tickers,
						"strong_catalysts", report.StrongCatalysts,
						"source_latency_ms", latency.Milliseconds(),
					)
					return nil
				},
				func() {
					health.Set(newsComponentHealth(
						"Alpaca News Stream",
						"CONNECTED",
						"Realtime WebSocket subscribed",
						time.Time{},
						nil,
					))
				},
			)
			if ctx.Err() != nil {
				return
			}
			health.Set(newsComponentHealth(
				"Alpaca News Stream",
				"DISCONNECTED",
				"Realtime WebSocket reconnecting",
				time.Now().UTC(),
				nil,
			))
			logger.Error(
				"Alpaca realtime news disconnected",
				"retry_in", backoff,
				"error", err,
			)
			if time.Since(startedAt) >= time.Minute {
				backoff = time.Second
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()
}

func startAlpacaNewsBackfill(
	ctx context.Context,
	client *alpaca.NewsClient,
	monitor *catalyst.Monitor,
	appConfig config.Config,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
	health *runtimeHealthRegistry,
) {
	go func() {
		syncNews := func() {
			now := time.Now().UTC()
			items, err := client.LatestMarketNews(
				ctx,
				now.Add(-appConfig.MarketNewsMonitorLookback),
				now,
				appConfig.MarketNewsMonitorLimit,
			)
			if err != nil {
				if ctx.Err() == nil {
					logger.Error(
						"Alpaca news backfill failed",
						"error", err,
					)
				}
				return
			}
			report, err := monitor.Ingest(ctx, now, items)
			if err != nil {
				if ctx.Err() == nil {
					logger.Error(
						"Alpaca news backfill ingestion failed",
						"error", err,
					)
				}
				return
			}
			health.Set(newsComponentHealth(
				"Alpaca News REST",
				"CONNECTED",
				"REST reconciliation active",
				now,
				nil,
			))
			if report.Articles > 0 {
				logger.Info(
					"Alpaca news backfill synchronized",
					"articles", report.Articles,
					"tickers", report.Tickers,
					"strong_catalysts", report.StrongCatalysts,
				)
			}
		}
		syncNews()
		ticker := time.NewTicker(appConfig.MarketNewsMonitorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				syncNews()
			}
		}
	}()
}

func newsProviderName(provider string) string {
	switch provider {
	case "alpaca_rest":
		return "Alpaca News REST"
	default:
		return "Massive News"
	}
}

func newsProviderDetail(provider string) string {
	switch provider {
	case "alpaca_rest":
		return "REST reconciliation"
	default:
		return "Hourly reference coverage"
	}
}

func newsComponentHealth(
	name string,
	status string,
	detail string,
	updatedAt time.Time,
	latencyMS *int64,
) dashboard.ComponentHealth {
	component := dashboard.ComponentHealth{
		Name: name, Status: status, Detail: detail, LatencyMS: latencyMS,
	}
	if !updatedAt.IsZero() {
		value := updatedAt.UTC()
		component.LastUpdate = &value
	}
	return component
}

func newsLatency(
	now time.Time,
	items []intelligence.TickerNewsItem,
) time.Duration {
	var latency time.Duration
	for _, item := range items {
		if item.News.PublishedAt.IsZero() ||
			item.News.PublishedAt.After(now) {
			continue
		}
		value := now.Sub(item.News.PublishedAt)
		if value > latency {
			latency = value
		}
	}
	return latency
}

func startCurrentFilingMonitor(
	ctx context.Context,
	appConfig config.Config,
	store *postgres.Store,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) error {
	enabled, err := boolEnvironment("CURRENT_SEC_MONITOR_ENABLED", true)
	if err != nil {
		return err
	}
	if !enabled {
		logger.Info("SEC current-filing monitor disabled")
		return nil
	}
	if strings.TrimSpace(appConfig.SECUserAgent) == "" {
		return errors.New(
			"CURRENT_SEC_MONITOR_ENABLED requires SEC_USER_AGENT",
		)
	}
	interval, err := time.ParseDuration(
		environmentOr("CURRENT_SEC_MONITOR_INTERVAL", "1m"),
	)
	if err != nil {
		return fmt.Errorf("parsing CURRENT_SEC_MONITOR_INTERVAL: %w", err)
	}
	lookback, err := time.ParseDuration(
		environmentOr("CURRENT_SEC_MONITOR_LOOKBACK", "24h"),
	)
	if err != nil {
		return fmt.Errorf("parsing CURRENT_SEC_MONITOR_LOOKBACK: %w", err)
	}
	count, err := intEnvironment("CURRENT_SEC_MONITOR_COUNT", 500)
	if err != nil {
		return err
	}
	client, err := secclient.NewClient(
		appConfig.SECUserAgent,
		&http.Client{Timeout: appConfig.HTTPTimeout},
		secclient.WithBaseURL(appConfig.SECBaseURL),
	)
	if err != nil {
		return fmt.Errorf("creating SEC current-filing client: %w", err)
	}
	monitor, err := catalyst.NewFilingMonitor(
		client,
		store,
		catalyst.FilingMonitorConfig{
			Interval: interval,
			Lookback: lookback,
			Count:    count,
		},
	)
	if err != nil {
		return fmt.Errorf("creating SEC current-filing monitor: %w", err)
	}
	go monitor.Run(
		ctx,
		func(report catalyst.FilingMonitorReport) {
			logger.Info(
				"SEC current filings synchronized",
				"filings", report.Filings,
				"ownership_catalysts", report.OwnershipCatalysts,
				"completed_at", report.CompletedAt,
			)
		},
		func(err error) {
			logger.Error("SEC current-filing synchronization failed", "error", err)
		},
	)
	logger.Info(
		"SEC current-filing monitor started",
		"interval", interval,
		"lookback", lookback,
		"count", count,
	)
	return nil
}

func startBoundaryShadow(
	ctx context.Context,
	appConfig config.Config,
	store *postgres.Store,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) (autonomousStrategyHandlers, error) {
	selector, err := catalyst.NewSelector(catalyst.BoundaryConfig{
		MaxCandidates:       appConfig.BoundaryShadowMaxCandidates,
		TotalNotionalTHB:    appConfig.BoundaryShadowTotalNotionalTHB,
		USDTHB:              appConfig.BoundaryShadowUSDTHB,
		NewsLookback:        appConfig.BoundaryShadowNewsLookback,
		MinCatalystStrength: appConfig.BoundaryShadowMinCatalyst,
		MinVolume:           appConfig.BoundaryShadowMinVolume,
		MinPrice:            appConfig.BoundaryShadowMinPrice,
		MaxPrice:            appConfig.BoundaryShadowMaxPrice,
		MaxSpread:           appConfig.BoundaryShadowMaxSpread,
		QuoteMaxAge:         appConfig.BoundaryShadowQuoteMaxAge,
		SignalMaxAge:        appConfig.BoundaryShadowSignalMaxAge,
		StopLoss:            appConfig.BoundaryShadowStopLoss,
		TrailActivation:     appConfig.BoundaryShadowTrailStart,
		TrailDistance:       appConfig.BoundaryShadowTrailDistance,
		ProfitLockFloor:     appConfig.BoundaryShadowProfitLock,
		ExitFeeMinimum:      appConfig.StrategyExitFeeMinimum,
		ExitFeePerShare:     appConfig.StrategyExitFeePerShare,
		SlippageReserve:     appConfig.StrategySlippageReserve,
		MinimumNetProfit:    appConfig.StrategyMinimumNetProfit,
	})
	if err != nil {
		return autonomousStrategyHandlers{}, fmt.Errorf(
			"creating AH boundary selector: %w",
			err,
		)
	}
	totalUSD := appConfig.BoundaryShadowTotalNotionalTHB /
		appConfig.BoundaryShadowUSDTHB
	paperAdapter, err := execution.NewPaperAdapterWithFees(
		appConfig.StrategyExitFeeMinimum,
		appConfig.StrategyExitFeePerShare,
	)
	if err != nil {
		return autonomousStrategyHandlers{}, fmt.Errorf(
			"creating boundary paper adapter: %w",
			err,
		)
	}
	executionService, err := execution.NewService(
		store,
		execution.ServiceOptions{
			Mode:             execution.ModeShadow,
			AutomaticTrading: true,
			PaperAdapter:     paperAdapter,
			Limits: execution.Limits{
				MaxPositionValue:     totalUSD,
				MaxGrossExposure:     totalUSD,
				MaxCapitalAllocation: 1,
				MaxDailyLoss:         totalUSD,
				MaxRiskPerTrade:      totalUSD,
				AllowedSessions: []string{
					string(market.SessionRegular),
					string(market.SessionAfterHours),
				},
				DefaultBuyingPower: totalUSD,
				ApprovalTTL:        appConfig.ApprovalTTL,
				KillSwitch:         false,
			},
		},
	)
	if err != nil {
		return autonomousStrategyHandlers{}, fmt.Errorf(
			"creating boundary shadow execution service: %w",
			err,
		)
	}
	runner, err := catalyst.NewRunner(
		store,
		executionService,
		selector,
		catalyst.RunnerConfig{
			EvaluationInterval:  appConfig.BoundaryShadowInterval,
			CandidateQueryLimit: 250,
		},
	)
	if err != nil {
		return autonomousStrategyHandlers{}, fmt.Errorf(
			"creating AH boundary shadow runner: %w",
			err,
		)
	}
	if err := runner.Restore(ctx); err != nil {
		return autonomousStrategyHandlers{}, err
	}
	go runner.Run(
		ctx,
		func(report catalyst.RunnerReport) {
			logger.Info(
				"AH boundary shadow evaluated",
				"trading_date", report.TradingDate.Format(time.DateOnly),
				"eligible", report.Eligible,
				"entries", report.Entries,
				"skipped", report.Skipped,
			)
		},
		func(err error) {
			logger.Error("AH boundary shadow evaluation failed", "error", err)
		},
	)
	logger.Info(
		"AH boundary catalyst shadow started",
		"entry_window", "15:55-16:00 America/New_York",
		"max_candidates", appConfig.BoundaryShadowMaxCandidates,
		"total_notional_thb", appConfig.BoundaryShadowTotalNotionalTHB,
		"usd_thb", appConfig.BoundaryShadowUSDTHB,
		"news_lookback", appConfig.BoundaryShadowNewsLookback,
		"broker_calls", false,
	)
	return autonomousStrategyHandlers{
		quote: func(ctx context.Context, quote webull.BookQuote) error {
			if len(quote.Bids) == 0 {
				return nil
			}
			decision, err := runner.HandleQuote(
				ctx,
				time.Now().UTC(),
				quote.Symbol,
				quote.Bids[0].Price,
			)
			if err != nil {
				return err
			}
			if decision.Exit {
				logger.Info(
					"AH boundary shadow exit",
					"ticker", quote.Symbol,
					"reason", decision.Reason,
					"limit", decision.LimitPrice,
					"stop", decision.StopPrice,
				)
			}
			return nil
		},
	}, nil
}

type autonomousStrategyHandlers struct {
	quote func(context.Context, webull.BookQuote) error
	tick  func(context.Context, webull.Tick) error
}

func combineAutonomousStrategyHandlers(
	primary autonomousStrategyHandlers,
	reportShadowError func(string, error),
	shadows ...autonomousStrategyHandlers,
) autonomousStrategyHandlers {
	return autonomousStrategyHandlers{
		quote: func(ctx context.Context, quote webull.BookQuote) error {
			for _, shadow := range shadows {
				if shadow.quote == nil {
					continue
				}
				if err := shadow.quote(ctx, quote); err != nil &&
					reportShadowError != nil {
					reportShadowError("quote", err)
				}
			}
			if primary.quote == nil {
				return nil
			}
			return primary.quote(ctx, quote)
		},
		tick: func(ctx context.Context, tick webull.Tick) error {
			for _, shadow := range shadows {
				if shadow.tick == nil {
					continue
				}
				if err := shadow.tick(ctx, tick); err != nil &&
					reportShadowError != nil {
					reportShadowError("tick", err)
				}
			}
			if primary.tick == nil {
				return nil
			}
			return primary.tick(ctx, tick)
		},
	}
}

func startAutonomousStrategy(
	ctx context.Context,
	appConfig config.Config,
	store *postgres.Store,
	executionService *execution.Service,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) (
	autonomousStrategyHandlers,
	func() []strategy.Plan,
	error,
) {
	if !appConfig.AutomaticTrading {
		logger.Info("autonomous strategy disabled")
		return autonomousStrategyHandlers{},
			func() []strategy.Plan { return nil },
			nil
	}
	executor, err := strategy.NewExecutionAdapter(
		executionService,
		appConfig.AutoEntryOrderTimeout,
		strategy.EconomicsConfig{
			ProfitFloor:              appConfig.StrategyProfitLockFloor,
			ExitFeeMinimum:           appConfig.StrategyExitFeeMinimum,
			ExitFeePerShare:          appConfig.StrategyExitFeePerShare,
			SlippageReserve:          appConfig.StrategySlippageReserve,
			MinimumExpectedNetProfit: appConfig.StrategyMinExpectedProfit,
		},
	)
	if err != nil {
		return autonomousStrategyHandlers{}, nil, err
	}
	coordinator, err := strategy.NewCoordinator(
		store,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: appConfig.TradingMode,
			BlockEntries: appConfig.TradingMode == string(execution.ModeLive) &&
				!appConfig.AutoLiveEntriesEnabled,
			MaxCandidates:  appConfig.AutoMaxCandidates,
			RetentionRank:  appConfig.StrategyRetentionRank,
			RetentionGrace: appConfig.StrategyRetentionGrace,
			RiskAmount:     appConfig.AutoRiskPerTrade,
			RetryDelay:     appConfig.AutoRetryDelay,
			FlowWindow:     appConfig.StrategyFlowWindow,
			HoldTickers:    appConfig.StrategyHoldTickers,
			Engine:         strategyEngineConfig(appConfig),
		},
	)
	if err != nil {
		return autonomousStrategyHandlers{}, nil,
			fmt.Errorf("creating autonomous strategy: %w", err)
	}
	if err := coordinator.Refresh(ctx, time.Now().UTC()); err != nil {
		return autonomousStrategyHandlers{}, nil,
			fmt.Errorf("loading autonomous strategy plans: %w", err)
	}
	type marketEvent struct {
		quote *webull.BookQuote
		tick  *webull.Tick
	}
	events := make(chan marketEvent, 8192)
	exitCriticalEvents := make(chan marketEvent, 2048)
	processEvent := func(event marketEvent) {
		var (
			decision strategy.Decision
			err      error
			ticker   string
		)
		if event.quote != nil {
			quote := *event.quote
			ticker = quote.Symbol
			bestBid, bestAsk := quote.Bids[0], quote.Asks[0]
			decision, err = coordinator.HandleQuote(
				ctx,
				time.Now().UTC(),
				strategy.Quote{
					Ticker: quote.Symbol,
					Bid:    bestBid.Price, Ask: bestAsk.Price,
					BidSize: bestBid.Size, AskSize: bestAsk.Size,
					ObservedAt: quote.ObservedAt,
				},
			)
		} else if event.tick != nil {
			tick := *event.tick
			ticker = tick.Symbol
			decision, err = coordinator.HandleTrade(
				ctx,
				time.Now().UTC(),
				strategy.TradeTick{
					Ticker: tick.Symbol, Price: tick.Price,
					Size: tick.Volume, Side: tick.Side,
					ObservedAt: tick.ObservedAt,
				},
			)
		}
		if err != nil {
			logger.Error(
				"autonomous strategy market event failed",
				"mode", appConfig.TradingMode,
				"ticker", ticker, "error", err,
			)
		} else if decision.Action != strategy.ActionNone {
			logger.Info(
				"autonomous strategy decision",
				"mode", appConfig.TradingMode,
				"ticker", ticker, "action", decision.Action,
				"limit", decision.LimitPrice,
				"stop", decision.StopPrice,
				"reason", decision.Reason,
			)
		}
	}
	go func() {
		refresh := time.NewTicker(5 * time.Second)
		reconcile := time.NewTicker(time.Second)
		defer refresh.Stop()
		defer reconcile.Stop()
		for {
			// Capital-protection events bypass discovery traffic. The default
			// clause gives this lane strict priority without blocking shutdown.
			select {
			case event := <-exitCriticalEvents:
				processEvent(event)
				continue
			default:
			}
			select {
			case <-ctx.Done():
				return
			case event := <-exitCriticalEvents:
				processEvent(event)
			case event := <-events:
				processEvent(event)
			case <-refresh.C:
				if err := coordinator.Refresh(ctx, time.Now().UTC()); err != nil {
					logger.Error(
						"refreshing autonomous strategy",
						"mode", appConfig.TradingMode,
						"error", err,
					)
				}
			case <-reconcile.C:
				if err := coordinator.Reconcile(ctx, time.Now().UTC()); err != nil {
					logger.Error(
						"reconciling autonomous strategy",
						"mode", appConfig.TradingMode,
						"error", err,
					)
				}
			}
		}
	}()
	logger.Info(
		"autonomous strategy started",
		"mode", appConfig.TradingMode,
		"live_entries_enabled",
		appConfig.TradingMode != string(execution.ModeLive) ||
			appConfig.AutoLiveEntriesEnabled,
		"max_candidates", appConfig.AutoMaxCandidates,
		"risk_per_trade", appConfig.AutoRiskPerTrade,
		"order_flow_window", appConfig.StrategyFlowWindow,
	)
	enqueue := func(ctx context.Context, event marketEvent) error {
		ticker := ""
		if event.quote != nil {
			ticker = event.quote.Symbol
		} else if event.tick != nil {
			ticker = event.tick.Symbol
		}
		if !coordinator.Tracks(ticker) {
			return nil
		}
		target := events
		if coordinator.ExitCritical(ticker) {
			target = exitCriticalEvents
		}
		select {
		case target <- event:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		default:
			// Keep the newest market state. Raw quotes and ticks remain fully
			// persisted for replay; only the latency-sensitive strategy view is
			// coalesced when a burst exceeds its processing capacity.
			select {
			case <-target:
			default:
			}
			select {
			case target <- event:
			default:
			}
			return nil
		}
	}
	return autonomousStrategyHandlers{
		quote: func(ctx context.Context, quote webull.BookQuote) error {
			return enqueue(ctx, marketEvent{quote: &quote})
		},
		tick: func(ctx context.Context, tick webull.Tick) error {
			return enqueue(ctx, marketEvent{tick: &tick})
		},
	}, coordinator.Plans, nil
}

func strategyEngineConfig(appConfig config.Config) strategy.Config {
	return strategy.Config{
		MinPullback:            appConfig.StrategyMinPullback,
		MaxPullback:            appConfig.StrategyMaxPullback,
		Reclaim:                appConfig.StrategyReclaim,
		MinEntryHeadroom:       appConfig.StrategyMinEntryHeadroom,
		StopLoss:               appConfig.StrategyStopLoss,
		TrailActivation:        appConfig.StrategyTrailStart,
		TrailDistance:          appConfig.StrategyTrailDistance,
		BreakEvenActivation:    appConfig.StrategyBreakEvenStart,
		BreakEvenBuffer:        appConfig.StrategyBreakEvenBuffer,
		ProfitLockActivation:   appConfig.StrategyProfitLockStart,
		ProfitLockFloor:        appConfig.StrategyProfitLockFloor,
		PartialTPActivation:    appConfig.StrategyPartialTPStart,
		PartialTPFraction:      appConfig.StrategyPartialTPFraction,
		PartialTPMinShares:     appConfig.StrategyPartialTPMinShares,
		ExitFeeMinimum:         appConfig.StrategyExitFeeMinimum,
		ExitFeePerShare:        appConfig.StrategyExitFeePerShare,
		SlippageReserve:        appConfig.StrategySlippageReserve,
		MinimumNetProfit:       appConfig.StrategyMinimumNetProfit,
		ReentryCooldown:        appConfig.StrategyReentryCooldown,
		ExitMomentumEnabled:    appConfig.StrategyExitMomentumEnabled,
		MinBuyerPressure:       appConfig.StrategyBuyerPressure,
		MaxSpread:              appConfig.StrategyMaxSpread,
		ExitLimitBuffer:        appConfig.StrategyExitBuffer,
		QuoteMaxAge:            appConfig.StrategyQuoteMaxAge,
		TradeQuoteMaxLag:       appConfig.StrategyTradeQuoteMaxLag,
		MinQuoteUpdates:        appConfig.StrategyMinQuoteUpdates,
		MinTradeTicks:          appConfig.StrategyMinTradeTicks,
		MinAggressiveBuyRatio:  appConfig.StrategyAggressiveBuyRatio,
		MinUptickRatio:         appConfig.StrategyUptickRatio,
		MinAverageBookPressure: appConfig.StrategyAverageBookPressure,
		MinPriceVelocity:       appConfig.StrategyMinPriceVelocity,
		AllowedEntrySessions:   appConfig.TradingAllowedSessions,
	}
}

func startBrokerSync(
	ctx context.Context,
	appConfig config.Config,
	store *postgres.Store,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) {
	enabled, err := boolEnvironment("BROKER_SYNC_ENABLED", true)
	if err != nil || !enabled {
		message := ""
		if err != nil {
			message = err.Error()
		}
		_ = store.SetBrokerSyncStatus(ctx, "DISABLED", message)
		return
	}
	webullConfig, err := config.LoadWebull()
	if err != nil {
		_ = store.SetBrokerSyncStatus(ctx, "BLOCKED", err.Error())
		logger.Error("broker sync blocked", "error", err)
		return
	}
	client, err := webull.NewClient(
		webullConfig.WebullAppKey, webullConfig.WebullSecret,
		webull.WithBaseURL(webullConfig.WebullTradingBaseURL),
		webull.WithAlgorithm(webullConfig.WebullAlgorithm),
		webull.WithAccessToken(webullConfig.WebullAccessToken),
		webull.WithHTTPClient(&http.Client{Timeout: webullConfig.HTTPTimeout}),
	)
	if err != nil {
		_ = store.SetBrokerSyncStatus(ctx, "BLOCKED", err.Error())
		logger.Error("broker sync blocked", "error", err)
		return
	}
	interval, err := time.ParseDuration(environmentOr("BROKER_SYNC_INTERVAL", "30s"))
	if err != nil || interval < 10*time.Second {
		message := "BROKER_SYNC_INTERVAL must be at least 10s"
		_ = store.SetBrokerSyncStatus(ctx, "DISABLED", message)
		logger.Error("broker sync disabled", "error", message)
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := syncBrokerOnce(ctx, client, store); err != nil && ctx.Err() == nil {
				status := "FAILED"
				if strings.Contains(err.Error(), "HTTP 401") ||
					strings.Contains(err.Error(), "HTTP 403") ||
					strings.Contains(strings.ToLower(err.Error()), "permission") {
					status = "BLOCKED"
				}
				_ = store.SetBrokerSyncStatus(ctx, status, err.Error())
				logger.Error("Webull broker sync failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	if appConfig.AutomaticTrading &&
		appConfig.TradingMode == string(execution.ModeLive) {
		startFastBrokerOrderSync(ctx, client, store, logger)
	}
	logger.Info("Webull broker sync started", "interval", interval)
}

func startFastBrokerOrderSync(
	ctx context.Context,
	client *webull.Client,
	store *postgres.Store,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) {
	interval, err := time.ParseDuration(
		environmentOr("BROKER_ORDER_DETAIL_INTERVAL", "2s"),
	)
	if err != nil || interval < time.Second {
		logger.Error(
			"fast broker order sync disabled",
			"error", "BROKER_ORDER_DETAIL_INTERVAL must be at least 1s",
		)
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		index := 0
		observedFilled := make(map[string]float64)
		rateLimitDelay := 2 * time.Second
		var rateLimitedUntil time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if time.Now().Before(rateLimitedUntil) {
				continue
			}
			pending, err := store.PendingLiveExecutionOrders(ctx)
			if err != nil {
				logger.Error("querying live orders for fast sync", "error", err)
				continue
			}
			if len(pending) == 0 {
				index = 0
				continue
			}
			if index >= len(pending) {
				index = 0
			}
			ref := pending[index]
			index = (index + 1) % len(pending)
			order, err := client.OrderDetail(
				ctx, ref.AccountID, ref.ClientOrderID,
			)
			if err != nil {
				if webull.IsRateLimitError(err) {
					rateLimitedUntil = time.Now().Add(rateLimitDelay)
					logger.Info(
						"Webull order-detail sync backing off after rate limit",
						"retry_in", rateLimitDelay,
					)
					rateLimitDelay = min(rateLimitDelay*2, 30*time.Second)
					continue
				}
				logger.Error(
					"loading Webull order detail",
					"client_order_id", ref.ClientOrderID, "error", err,
				)
				continue
			}
			rateLimitDelay = 2 * time.Second
			rateLimitedUntil = time.Time{}
			if err := store.SaveBrokerOrders(
				ctx, []webull.BrokerOrder{order},
			); err != nil {
				logger.Error("saving Webull order detail", "error", err)
				continue
			}
			if err := store.SyncExecutionBrokerOrders(
				ctx, []webull.BrokerOrder{order},
			); err != nil {
				logger.Error("reconciling Webull order detail", "error", err)
				continue
			}
			key := ref.AccountID + ":" + ref.ClientOrderID
			if order.FilledQuantity <= observedFilled[key] {
				continue
			}
			observedFilled[key] = order.FilledQuantity
			balance, err := client.Balance(ctx, ref.AccountID)
			if err != nil {
				logger.Error("refreshing Webull balance after fill", "error", err)
				continue
			}
			positions, err := client.AccountPositions(ctx, ref.AccountID)
			if err != nil {
				logger.Error("refreshing Webull positions after fill", "error", err)
				continue
			}
			if err := store.SaveBrokerSnapshot(
				ctx,
				[]webull.Account{{ID: ref.AccountID}},
				[]webull.AccountBalance{balance},
				positions,
				[]webull.BrokerOrder{order},
			); err != nil {
				logger.Error("saving Webull account after fill", "error", err)
			}
		}
	}()
	logger.Info("fast Webull order-detail sync started", "interval", interval)
}

func syncBrokerOnce(
	ctx context.Context,
	client *webull.Client,
	store *postgres.Store,
) error {
	if err := store.SetBrokerSyncStatus(ctx, "RUNNING", ""); err != nil {
		return err
	}
	accounts, err := client.Accounts(ctx)
	if err != nil {
		return fmt.Errorf("loading Webull accounts: %w", err)
	}
	positions := make([]webull.BrokerPosition, 0)
	orders := make([]webull.BrokerOrder, 0)
	balances := make([]webull.AccountBalance, 0, len(accounts))
	var orderError error
	for index, account := range accounts {
		balance, err := client.Balance(ctx, account.ID)
		if err != nil {
			return fmt.Errorf(
				"loading Webull balance for account %s: %w", account.ID, err,
			)
		}
		balances = append(balances, balance)
		accountPositions, err := client.AccountPositions(ctx, account.ID)
		if err != nil {
			return fmt.Errorf(
				"loading Webull positions for account %s: %w", account.ID, err,
			)
		}
		positions = append(positions, accountPositions...)
		accountOrders, err := client.OrderHistory(ctx, account.ID)
		if err != nil {
			orderError = fmt.Errorf(
				"loading Webull orders for account %s: %w", account.ID, err,
			)
		} else {
			orders = append(orders, accountOrders...)
		}
		if index+1 < len(accounts) {
			timer := time.NewTimer(1100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return context.Cause(ctx)
			case <-timer.C:
			}
		}
	}
	if err := store.SaveBrokerSnapshot(
		ctx, accounts, balances, positions, orders,
	); err != nil {
		return err
	}
	if err := store.SyncExecutionBrokerOrders(ctx, orders); err != nil {
		return fmt.Errorf("reconciling execution orders: %w", err)
	}
	if orderError != nil {
		return orderError
	}
	return nil
}

func startFeedHealthMonitor(
	ctx context.Context,
	store *postgres.Store,
	logger interface{ Error(string, ...any) },
) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			if err := store.EvaluateMarketFeed(ctx); err != nil && ctx.Err() == nil {
				logger.Error("evaluating market feed health", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func startDailyScheduler(
	ctx context.Context,
	appConfig config.Config,
	store *postgres.Store,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) error {
	enabled, err := boolEnvironment("SCHEDULER_ENABLED", true)
	if err != nil {
		return err
	}
	locationName := environmentOr("SCHEDULER_TIMEZONE", "Asia/Bangkok")
	location, err := time.LoadLocation(locationName)
	if err != nil {
		return fmt.Errorf("loading scheduler timezone %q: %w", locationName, err)
	}
	if !enabled {
		if err := store.SaveSchedule(ctx, automation.ScheduleState{
			Enabled: false, Timezone: locationName, Updated: time.Now().UTC(),
		}); err != nil {
			return err
		}
		logger.Info("daily scheduler disabled")
		return nil
	}
	retryAttempts, err := intEnvironment("SCHEDULER_RETRY_ATTEMPTS", 3)
	if err != nil {
		return err
	}
	retryDelay, err := time.ParseDuration(
		environmentOr("SCHEDULER_RETRY_DELAY", "15s"),
	)
	if err != nil {
		return fmt.Errorf("parsing SCHEDULER_RETRY_DELAY: %w", err)
	}
	continuousLearning, err := boolEnvironment(
		"CONTINUOUS_LEARNING_ENABLED", true,
	)
	if err != nil {
		return err
	}
	timedJobs := []struct {
		name         string
		environment  string
		defaultClock string
		run          func(context.Context) error
	}{
		{
			name: "market_services", environment: "SCHEDULE_MARKET_SERVICES",
			defaultClock: "14:55",
			run: func(context.Context) error {
				// The API, database, Redis, and Webull stream are already supervised
				// by this service. This stage records that they reached the daily gate.
				return nil
			},
		},
		{
			name: "market_daily_sync", environment: "SCHEDULE_MARKET_DAILY_SYNC",
			defaultClock: "13:20",
			run: func(jobContext context.Context) error {
				return runScheduledCommand(
					jobContext, logger,
					"opening-list",
					"--date="+previousUSTradingDate(time.Now()),
					"--sync=true",
					"--research-limit=0",
					"--format=json",
				)
			},
		},
		{
			// Runs after the 20:00 ET after-hours close (07:10 Bangkok) so the
			// session it labels is complete. Reads only stored observations, so
			// a missed day is recovered by re-running the date.
			name: "ah_outcomes", environment: "SCHEDULE_AH_OUTCOMES",
			defaultClock: "07:10",
			run: func(jobContext context.Context) error {
				tradingDate, err := time.Parse(
					time.DateOnly,
					previousUSTradingDate(time.Now()),
				)
				if err != nil {
					return err
				}
				rows, err := store.CaptureAHBoundaryOutcomes(
					jobContext, tradingDate,
				)
				if err != nil {
					return err
				}
				logger.Info(
					"after-hours boundary outcomes captured",
					"trading_date", tradingDate.Format(time.DateOnly),
					"rows", rows,
				)
				return nil
			},
		},
		{
			name: "strategy_replay", environment: "SCHEDULE_STRATEGY_REPLAY",
			defaultClock: "13:25",
			run: func(jobContext context.Context) error {
				tradingDate, err := time.Parse(
					time.DateOnly,
					previousUSTradingDate(time.Now()),
				)
				if err != nil {
					return err
				}
				return runDailyStrategyReplays(
					jobContext,
					store,
					appConfig,
					tradingDate,
				)
			},
		},
		{
			name:         "spike_evaluation",
			environment:  "SCHEDULE_SPIKE_EVALUATION",
			defaultClock: "14:50",
			run: func(jobContext context.Context) error {
				tradingDate, err := time.Parse(
					time.DateOnly,
					previousUSTradingDate(time.Now()),
				)
				if err != nil {
					return err
				}
				_, err = store.EvaluateAndSaveSpikeReport(
					jobContext,
					tradingDate,
					environmentOr(
						"CONTINUOUS_LEARNING_SPIKE_MODEL",
						"spike-discovery-v1",
					),
					[]float64{.30, .50, 1},
					[]int{20, 100},
				)
				return err
			},
		},
		{
			name:         "spike_model_inference",
			environment:  "SCHEDULE_SPIKE_MODEL_INFERENCE",
			defaultClock: "14:58:30",
			run: func(jobContext context.Context) error {
				return runScheduledCommand(
					jobContext, logger,
					"rank",
					"--name="+environmentOr(
						"CONTINUOUS_LEARNING_SPIKE_MODEL",
						"spike-discovery-v1",
					),
					"--date="+previousUSTradingDate(time.Now()),
					"--common-stocks-only",
				)
			},
		},
		{
			name: "model_inference", environment: "SCHEDULE_MODEL_INFERENCE",
			defaultClock: "14:59",
			run: func(jobContext context.Context) error {
				return runScheduledCommand(
					jobContext, logger,
					"rank",
					"--name="+environmentOr(
						"CONTINUOUS_LEARNING_MODEL", "runner-baseline",
					),
					"--date="+previousUSTradingDate(time.Now()),
				)
			},
		},
		{
			name: "premarket_scan", environment: "SCHEDULE_PREMARKET_SCAN",
			defaultClock: "15:00",
			run: func(jobContext context.Context) error {
				tickers, err := store.RealtimeTickers(jobContext)
				if err != nil {
					return err
				}
				universe, err := store.PremarketUniverse(jobContext, 100)
				if err != nil {
					return err
				}
				tickers = scheduledScannerTickers(
					100,
					tickers,
					normalizedTickers(os.Getenv("SCHEDULER_TICKERS")),
					normalizedTickers(os.Getenv("QUOTE_TICKERS")),
					universe,
				)
				if len(tickers) == 0 {
					return errors.New("scheduler has no scanner symbols")
				}
				return runScheduledCommand(
					jobContext, logger,
					"scan",
					"--once",
					"--tickers="+strings.Join(tickers, ","),
					"--min-change="+environmentOr(
						"SCANNER_MIN_CHANGE", "0",
					),
					"--min-volume="+environmentOr(
						"SCANNER_MIN_VOLUME", "0",
					),
				)
			},
		},
		{
			name: "research", environment: "SCHEDULE_RESEARCH",
			defaultClock: "15:00:30",
			run: func(jobContext context.Context) error {
				tickers, err := store.RealtimeTickers(jobContext)
				if err != nil {
					return err
				}
				tickers = mergeTickers(
					tickers,
					normalizedTickers(os.Getenv("SCHEDULER_TICKERS")),
				)
				if len(tickers) == 0 {
					return errors.New("scheduler has no research symbols")
				}
				return runScheduledCommand(
					jobContext, logger,
					"features", "--tickers="+strings.Join(tickers, ","),
					"--date="+previousUSTradingDate(time.Now()),
				)
			},
		},
		{
			name: "ranking", environment: "SCHEDULE_RANKING",
			defaultClock: "15:01",
			run: func(jobContext context.Context) error {
				date, err := time.Parse(time.DateOnly, usTradingDate(time.Now()))
				if err != nil {
					return err
				}
				return store.SavePremarketRanking(jobContext, date, 5)
			},
		},
		{
			name: "dashboard_ready", environment: "SCHEDULE_DASHBOARD_READY",
			defaultClock: "15:02",
			run:          func(context.Context) error { return nil },
		},
	}
	if continuousLearning {
		timedJobs = append(timedJobs, struct {
			name         string
			environment  string
			defaultClock string
			run          func(context.Context) error
		}{
			name: "model_learning", environment: "SCHEDULE_MODEL_LEARNING",
			defaultClock: "13:30",
			run: func(jobContext context.Context) error {
				trainingTo, err := time.Parse(
					time.DateOnly,
					previousUSTradingDate(time.Now()),
				)
				if err != nil {
					return err
				}
				lookbackDays, err := intEnvironment(
					"CONTINUOUS_LEARNING_LOOKBACK_DAYS", 730,
				)
				if err != nil || lookbackDays < 60 {
					return errors.New(
						"CONTINUOUS_LEARNING_LOOKBACK_DAYS must be at least 60",
					)
				}
				trainingFrom := trainingTo.AddDate(0, 0, -lookbackDays)
				if err := runScheduledCommand(
					jobContext, logger,
					"features-backfill",
					"--from="+trainingFrom.Format(time.DateOnly),
					"--to="+trainingTo.Format(time.DateOnly),
				); err != nil {
					return err
				}
				return runScheduledCommand(
					jobContext, logger,
					"train",
					"--name="+environmentOr(
						"CONTINUOUS_LEARNING_MODEL", "runner-baseline",
					),
					"--algorithm="+environmentOr(
						"CONTINUOUS_LEARNING_ALGORITHM",
						ranking.AlgorithmLogistic,
					),
					"--iterations="+environmentOr(
						"CONTINUOUS_LEARNING_ITERATIONS", "100",
					),
					"--from="+trainingFrom.Format(time.DateOnly),
					"--to="+trainingTo.Format(time.DateOnly),
					"--validation-fraction="+environmentOr(
						"CONTINUOUS_LEARNING_VALIDATION_FRACTION", "0.20",
					),
					"--min-validation-samples="+environmentOr(
						"CONTINUOUS_LEARNING_MIN_VALIDATION_SAMPLES", "50",
					),
					"--min-logloss-improvement="+environmentOr(
						"CONTINUOUS_LEARNING_MIN_LOGLOSS_IMPROVEMENT", "0.01",
					),
					"--max-precision-regression="+environmentOr(
						"CONTINUOUS_LEARNING_MAX_PRECISION_REGRESSION", "0.03",
					),
				)
			},
		}, struct {
			name         string
			environment  string
			defaultClock string
			run          func(context.Context) error
		}{
			name:         "spike_model_learning",
			environment:  "SCHEDULE_SPIKE_MODEL_LEARNING",
			defaultClock: "13:40",
			run: func(jobContext context.Context) error {
				trainingTo, err := time.Parse(
					time.DateOnly,
					previousUSTradingDate(time.Now()),
				)
				if err != nil {
					return err
				}
				lookbackDays, err := intEnvironment(
					"CONTINUOUS_LEARNING_SPIKE_LOOKBACK_DAYS",
					730,
				)
				if err != nil || lookbackDays < 60 {
					return errors.New(
						"CONTINUOUS_LEARNING_SPIKE_LOOKBACK_DAYS " +
							"must be at least 60",
					)
				}
				trainingFrom := trainingTo.AddDate(0, 0, -lookbackDays)
				return runScheduledCommand(
					jobContext, logger,
					"train",
					"--name="+environmentOr(
						"CONTINUOUS_LEARNING_SPIKE_MODEL",
						"spike-discovery-v1",
					),
					"--target="+environmentOr(
						"CONTINUOUS_LEARNING_SPIKE_TARGET",
						"spike50",
					),
					"--algorithm="+environmentOr(
						"CONTINUOUS_LEARNING_SPIKE_ALGORITHM",
						ranking.AlgorithmLightGBM,
					),
					"--boost-rounds="+environmentOr(
						"CONTINUOUS_LEARNING_SPIKE_BOOST_ROUNDS",
						"64",
					),
					"--from="+trainingFrom.Format(time.DateOnly),
					"--to="+trainingTo.Format(time.DateOnly),
					"--validation-fraction="+environmentOr(
						"CONTINUOUS_LEARNING_VALIDATION_FRACTION",
						"0.20",
					),
					"--min-validation-samples="+environmentOr(
						"CONTINUOUS_LEARNING_MIN_VALIDATION_SAMPLES",
						"50",
					),
					"--min-logloss-improvement="+environmentOr(
						"CONTINUOUS_LEARNING_MIN_LOGLOSS_IMPROVEMENT",
						"0.01",
					),
					"--max-precision-regression="+environmentOr(
						"CONTINUOUS_LEARNING_MAX_PRECISION_REGRESSION",
						"0.03",
					),
					"--min-recall-at-20="+environmentOr(
						"CONTINUOUS_LEARNING_SPIKE_MIN_RECALL_AT_20",
						"0.10",
					),
					"--min-recall-at-100="+environmentOr(
						"CONTINUOUS_LEARNING_SPIKE_MIN_RECALL_AT_100",
						"0.40",
					),
				)
			},
		})
	}
	jobs := make([]automation.Job, 0, len(timedJobs))
	for _, scheduled := range timedJobs {
		hour, minute, second, err := automation.ParseTime(
			environmentOr(scheduled.environment, scheduled.defaultClock),
		)
		if err != nil {
			return fmt.Errorf("configuring %s: %w", scheduled.name, err)
		}
		jobs = append(jobs, automation.Job{
			Name: scheduled.name, Hour: hour, Minute: minute, Second: second,
			Run: scheduled.run,
		})
	}
	dailyScheduler, err := automation.New(automation.Config{
		Location: location, Jobs: jobs, Store: store,
		IsTradingDay: opening.IsTradingDay,
		Retry: automation.RetryPolicy{
			MaximumAttempts: retryAttempts, InitialDelay: retryDelay,
		},
	})
	if err != nil {
		return fmt.Errorf("configuring daily scheduler: %w", err)
	}
	go func() {
		if err := dailyScheduler.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("daily scheduler stopped", "error", err)
		}
	}()
	logger.Info("daily scheduler started", "timezone", locationName)
	return nil
}

func runDailyStrategyReplays(
	ctx context.Context,
	store *postgres.Store,
	appConfig config.Config,
	tradingDate time.Time,
) error {
	candidateLimit, err := intEnvironment("STRATEGY_REPLAY_CANDIDATES", 20)
	if err != nil || candidateLimit < 1 || candidateLimit > 100 {
		return errors.New("STRATEGY_REPLAY_CANDIDATES must be between 1 and 100")
	}
	maxEvents, err := intEnvironment("STRATEGY_REPLAY_MAX_EVENTS", 500_000)
	if err != nil || maxEvents < 1_000 || maxEvents > 5_000_000 {
		return errors.New(
			"STRATEGY_REPLAY_MAX_EVENTS must be between 1000 and 5000000",
		)
	}
	candidates, err := store.StrategyReplayCandidates(
		ctx,
		tradingDate,
		candidateLimit,
	)
	if err != nil {
		return err
	}
	engineConfig := strategyEngineConfig(appConfig)
	engine, err := strategy.NewReplayEngine(
		engineConfig,
		appConfig.StrategyFlowWindow,
		1,
	)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		events, err := store.StrategyReplayEvents(
			ctx,
			candidate.Ticker,
			tradingDate,
			maxEvents,
		)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			continue
		}
		result, err := engine.Run(
			candidate.Ticker,
			tradingDate,
			candidate.Score,
			events,
		)
		if err != nil {
			return fmt.Errorf(
				"replaying %s for %s: %w",
				candidate.Ticker,
				tradingDate.Format(time.DateOnly),
				err,
			)
		}
		if err := store.SaveStrategyReplay(
			ctx,
			tradingDate,
			candidate.Ticker,
			engineConfig,
			result,
		); err != nil {
			return err
		}
	}
	return runActualFillExitReplays(
		ctx,
		store,
		appConfig,
		tradingDate,
		maxEvents,
	)
}

func runActualFillExitReplays(
	ctx context.Context,
	store *postgres.Store,
	appConfig config.Config,
	tradingDate time.Time,
	maxEvents int,
) error {
	cycles, err := store.ActualTradeCycles(ctx, "live", tradingDate)
	if err != nil {
		return err
	}
	if len(cycles) == 0 {
		return nil
	}
	engineConfig := strategyEngineConfig(appConfig)
	grouped := make(map[string][]strategy.ActualFillExitComparison)
	for _, cycle := range cycles {
		events, err := store.StrategyReplayEventsBetween(
			ctx,
			cycle.Ticker,
			cycle.EntryAt.Add(-appConfig.StrategyFlowWindow),
			cycle.ActualExitAt,
			maxEvents,
		)
		if err != nil {
			return fmt.Errorf(
				"loading actual-fill events for %s at %s: %w",
				cycle.Ticker,
				cycle.EntryAt.Format(time.RFC3339Nano),
				err,
			)
		}
		comparison, err := strategy.CompareActualFillExitReplay(
			engineConfig,
			appConfig.StrategyFlowWindow,
			cycle,
			events,
		)
		if err != nil {
			return fmt.Errorf(
				"replaying actual fill for %s at %s: %w",
				cycle.Ticker,
				cycle.EntryAt.Format(time.RFC3339Nano),
				err,
			)
		}
		grouped[cycle.Ticker] = append(
			grouped[cycle.Ticker],
			comparison,
		)
	}
	tickers := make([]string, 0, len(grouped))
	for ticker := range grouped {
		tickers = append(tickers, ticker)
	}
	sort.Strings(tickers)
	generatedAt := time.Now().UTC()
	for _, ticker := range tickers {
		report, err := strategy.BuildActualFillReplayReport(
			tradingDate,
			ticker,
			grouped[ticker],
			generatedAt,
		)
		if err != nil {
			return err
		}
		if err := store.SaveActualFillReplay(ctx, report); err != nil {
			return err
		}
	}
	return nil
}

func runScheduledCommand(
	ctx context.Context,
	logger interface{ Info(string, ...any) },
	arguments ...string,
) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving mip executable: %w", err)
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf(
			"scheduled %s failed: %w: %s",
			arguments[0], err, strings.TrimSpace(output.String()),
		)
	}
	logger.Info(
		"scheduled command completed", "command", arguments[0],
		"output", strings.TrimSpace(output.String()),
	)
	return nil
}

func usTradingDate(now time.Time) string {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return now.UTC().Format(time.DateOnly)
	}
	return now.In(location).Format(time.DateOnly)
}

func previousUSTradingDate(now time.Time) string {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return now.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	}
	date := now.In(location).AddDate(0, 0, -1)
	for !opening.IsTradingDay(date) {
		date = date.AddDate(0, 0, -1)
	}
	return date.Format(time.DateOnly)
}

func environmentOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func boolEnvironment(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parsing %s: %w", name, err)
	}
	return value, nil
}

func intEnvironment(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func floatEnvironment(name string, fallback float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", name, err)
	}
	return value, nil
}

func runBook(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip book", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var tickersRaw string
	var timeout time.Duration
	flags.StringVar(&tickersRaw, "tickers", "", "comma-separated ticker symbols")
	flags.DurationVar(&timeout, "timeout", 20*time.Second, "maximum quote wait")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing book flags: %w", err)
	}
	tickers := normalizedTickers(tickersRaw)
	if len(tickers) == 0 {
		return errors.New("at least one quote ticker is required")
	}
	if timeout <= 0 {
		return errors.New("book timeout must be positive")
	}
	appConfig, err := config.LoadWebull()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	parentContext, stop := commandContext()
	defer stop()
	ctx, cancel := context.WithTimeout(parentContext, timeout)
	defer cancel()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	client, err := dashboardWebullClient(appConfig)
	if err != nil {
		return err
	}
	store := postgres.NewStore(pool)
	remaining := make(map[string]struct{}, len(tickers))
	for _, ticker := range tickers {
		remaining[ticker] = struct{}{}
	}
	streamContext, stopStream := context.WithCancel(ctx)
	defer stopStream()
	err = client.StreamQuotes(
		streamContext, appConfig.WebullMQTTURL, tickers,
		func(ctx context.Context, quote webull.BookQuote) error {
			if err := store.SaveBookQuote(ctx, quote); err != nil {
				return err
			}
			if _, ok := remaining[quote.Symbol]; !ok {
				return nil
			}
			bestBid, bestAsk := quote.Bids[0], quote.Asks[0]
			output := map[string]any{
				"ticker": quote.Symbol, "observed_at": quote.ObservedAt,
				"bid": bestBid, "ask": bestAsk,
			}
			if err := json.NewEncoder(stdout).Encode(output); err != nil {
				return fmt.Errorf("writing quote: %w", err)
			}
			delete(remaining, quote.Symbol)
			if len(remaining) == 0 {
				stopStream()
			}
			return nil
		},
	)
	if err == nil && len(remaining) == 0 {
		return nil
	}
	if errors.Is(err, context.Canceled) && len(remaining) == 0 {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		return fmt.Errorf("timed out waiting for quotes: %v", mapKeys(remaining))
	}
	return err
}

func startBookStream(
	ctx context.Context,
	store *postgres.Store,
	tickers []string,
	strategyHandlers autonomousStrategyHandlers,
	overnightDataEnabled bool,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) error {
	webullConfig, err := config.LoadWebull()
	if err != nil {
		return fmt.Errorf("loading Webull quote configuration: %w", err)
	}
	client, err := dashboardWebullClient(webullConfig)
	if err != nil {
		return err
	}
	streamContext, cancelStream := context.WithCancelCause(ctx)
	if err := startWebullTokenMaintenance(
		streamContext, webullConfig, client, cancelStream,
	); err != nil {
		cancelStream(err)
		return err
	}
	if err := startContinuousDiscovery(
		streamContext,
		store,
		client,
		overnightDataEnabled,
		logger,
	); err != nil {
		cancelStream(err)
		return err
	}
	symbolCache := newWebullStreamSymbolCache()
	go func() {
		defer cancelStream(nil)
		type streamResult struct {
			key string
			err error
		}
		results := make(chan streamResult, 1)
		refresh := time.NewTicker(5 * time.Second)
		defer refresh.Stop()
		var activeCancel context.CancelFunc
		currentKey := ""
		reconcile := true
		for {
			if reconcile {
				dynamic, err := store.RealtimeTickers(streamContext)
				if err != nil {
					logger.Error("loading realtime quote symbols", "error", err)
				} else {
					validationContext, validationCancel := context.WithTimeout(
						streamContext,
						webullConfig.HTTPTimeout,
					)
					symbols, validationErr := symbolCache.Filter(
						validationContext,
						client,
						mergeTickers(tickers, dynamic),
						false,
						100,
					)
					validationCancel()
					if validationErr != nil {
						logger.Error(
							"validating realtime quote symbols",
							"error",
							validationErr,
						)
						reconcile = false
						continue
					}
					key := strings.Join(symbols, ",")
					if key != currentKey {
						if activeCancel != nil {
							activeCancel()
						}
						currentKey = key
						activeCancel = nil
						if len(symbols) > 0 {
							subscriptionContext, cancel := context.WithCancel(
								streamContext,
							)
							activeCancel = cancel
							go func(streamKey string, streamSymbols []string) {
								err := client.StreamOrderFlow(
									subscriptionContext,
									webullConfig.WebullMQTTURL,
									streamSymbols,
									func(_ context.Context, quote webull.BookQuote) error {
										if strategyHandlers.quote != nil {
											if err := strategyHandlers.quote(
												streamContext,
												quote,
											); err != nil {
												return err
											}
										}
										persistCtx, cancel := context.WithTimeout(
											streamContext,
											2*time.Second,
										)
										defer cancel()
										if err := store.SaveBookQuote(
											persistCtx,
											quote,
										); err != nil && streamContext.Err() == nil {
											logger.Error(
												"persisting Webull quote failed",
												"ticker", quote.Symbol, "error", err,
											)
										}
										return nil
									},
									func(_ context.Context, tick webull.Tick) error {
										if strategyHandlers.tick != nil {
											if err := strategyHandlers.tick(
												streamContext,
												tick,
											); err != nil {
												return err
											}
										}
										persistCtx, cancel := context.WithTimeout(
											streamContext,
											2*time.Second,
										)
										defer cancel()
										if err := store.SaveTradeTick(
											persistCtx,
											tick,
										); err != nil && streamContext.Err() == nil {
											logger.Error(
												"persisting Webull tick failed",
												"ticker", tick.Symbol, "error", err,
											)
										}
										return nil
									},
								)
								select {
								case results <- streamResult{key: streamKey, err: err}:
								case <-streamContext.Done():
								}
							}(key, symbols)
							logger.Info(
								"Webull book subscriptions updated",
								"symbols", key,
							)
						}
					}
				}
				reconcile = false
			}
			select {
			case <-streamContext.Done():
				if activeCancel != nil {
					activeCancel()
				}
				if cause := context.Cause(streamContext); cause != nil &&
					!errors.Is(cause, context.Canceled) {
					logger.Error("Webull book stream stopped", "error", cause)
				} else {
					logger.Info("Webull book stream stopped")
				}
				return
			case <-refresh.C:
				reconcile = true
			case result := <-results:
				if result.key == currentKey && streamContext.Err() == nil {
					logger.Error(
						"Webull book stream stopped; reconnecting",
						"error", result.err,
					)
					if result.err != nil &&
						!errors.Is(result.err, context.Canceled) {
						now := time.Now().UTC()
						if err := store.RecordAlert(
							streamContext,
							"CONNECTION_LOST", "CRITICAL", nil,
							"Webull connection lost",
							"The realtime Webull book stream stopped and is reconnecting.",
							"webull-connection:"+now.Format("2006-01-02T15:04"),
						); err != nil {
							logger.Error(
								"recording Webull connection alert", "error", err,
							)
						}
					}
					currentKey = ""
					reconcile = true
				}
			}
		}
	}()
	return nil
}

type webullSnapshotSource interface {
	SnapshotsBestEffort(
		context.Context,
		[]string,
		bool,
	) ([]webull.Snapshot, error)
}

type webullStreamSymbolCache struct {
	supported   map[string]struct{}
	unsupported map[string]struct{}
}

func newWebullStreamSymbolCache() *webullStreamSymbolCache {
	return &webullStreamSymbolCache{
		supported:   make(map[string]struct{}),
		unsupported: make(map[string]struct{}),
	}
}

func (cache *webullStreamSymbolCache) Filter(
	ctx context.Context,
	source webullSnapshotSource,
	symbols []string,
	overnight bool,
	maximum int,
) ([]string, error) {
	if cache == nil || source == nil || maximum <= 0 {
		return []string{}, nil
	}
	symbols = market.NormalizeUSStockSymbols(symbols)
	result := make([]string, 0, min(len(symbols), maximum))
	pending := make([]string, 0, min(maximum, 100))
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		snapshots, err := source.SnapshotsBestEffort(
			ctx,
			pending,
			overnight,
		)
		if err != nil {
			return fmt.Errorf(
				"checking Webull stream symbols: %w",
				err,
			)
		}
		valid := make(map[string]struct{}, len(snapshots))
		for _, snapshot := range snapshots {
			valid[strings.ToUpper(strings.TrimSpace(snapshot.Symbol))] =
				struct{}{}
		}
		for _, symbol := range pending {
			if _, ok := valid[symbol]; ok {
				cache.supported[symbol] = struct{}{}
				if len(result) < maximum {
					result = append(result, symbol)
				}
				continue
			}
			cache.unsupported[symbol] = struct{}{}
		}
		pending = pending[:0]
		return nil
	}
	for _, symbol := range symbols {
		if len(result) >= maximum {
			break
		}
		if _, ok := cache.unsupported[symbol]; ok {
			continue
		}
		if _, ok := cache.supported[symbol]; ok {
			if err := flush(); err != nil {
				return nil, err
			}
			if len(result) < maximum {
				result = append(result, symbol)
			}
			continue
		}
		pending = append(pending, symbol)
		batchLimit := min(100, maximum-len(result))
		if len(pending) >= batchLimit {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return result, nil
}

func dashboardWebullClient(appConfig config.Config) (*webull.Client, error) {
	return webull.NewClient(
		appConfig.WebullAppKey, appConfig.WebullSecret,
		webull.WithBaseURL(appConfig.WebullBaseURL),
		webull.WithAlgorithm(appConfig.WebullAlgorithm),
		webull.WithAccessToken(appConfig.WebullAccessToken),
		webull.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
}

func startContinuousDiscovery(
	ctx context.Context,
	store *postgres.Store,
	client *webull.Client,
	overnightDataEnabled bool,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) error {
	interval, err := time.ParseDuration(
		environmentOr("CONTINUOUS_SCAN_INTERVAL", "15s"),
	)
	if err != nil {
		return fmt.Errorf("parsing CONTINUOUS_SCAN_INTERVAL: %w", err)
	}
	pageSize, err := intEnvironment("CONTINUOUS_SCAN_PAGE_SIZE", 50)
	if err != nil {
		return err
	}
	universeSize, err := intEnvironment("CONTINUOUS_SCAN_UNIVERSE_SIZE", 100)
	if err != nil {
		return err
	}
	selectionLimit, err := intEnvironment(
		"CONTINUOUS_SCAN_SELECTION_LIMIT",
		5,
	)
	if err != nil {
		return err
	}
	spikeUniverseLimit, err := intEnvironment(
		"CONTINUOUS_SCAN_SPIKE_UNIVERSE_LIMIT",
		20,
	)
	if err != nil {
		return err
	}
	minPrice, err := floatEnvironment("SCANNER_MIN_PRICE", .05)
	if err != nil {
		return err
	}
	maxPrice, err := floatEnvironment("SCANNER_MAX_PRICE", 100)
	if err != nil {
		return err
	}
	minChange, err := floatEnvironment("SCANNER_MIN_CHANGE", 0)
	if err != nil {
		return err
	}
	minVolume, err := floatEnvironment("SCANNER_MIN_VOLUME", 0)
	if err != nil {
		return err
	}
	service, err := discovery.NewService(client, store, discovery.Config{
		Interval: interval, PageSize: pageSize, UniverseSize: universeSize,
		SelectionLimit:   selectionLimit,
		OvernightEnabled: overnightDataEnabled,
		ModelName: environmentOr(
			"CONTINUOUS_LEARNING_MODEL",
			"runner-baseline",
		),
		SpikeModelName: environmentOr(
			"CONTINUOUS_LEARNING_SPIKE_MODEL",
			"spike-discovery-v1",
		),
		SpikeUniverseLimit: spikeUniverseLimit,
		Criteria: scanner.Criteria{
			MinPrice: minPrice, MaxPrice: maxPrice,
			MinChangeRatio: minChange, MinVolume: minVolume,
		},
	})
	if err != nil {
		return fmt.Errorf("configuring continuous discovery: %w", err)
	}
	go service.Run(
		ctx,
		func(report discovery.Report) {
			if report.State == "PAUSED" {
				return
			}
			logger.Info(
				"continuous discovery completed",
				"session", report.Session,
				"trading_date", report.TradingDate.Format(time.DateOnly),
				"universe", report.UniverseSize,
				"spike_seeds", report.SpikeSeeds,
				"signals", report.Signals,
				"pattern_flags", report.PatternFlags,
				"pattern_errors", report.PatternErrors,
			)
		},
		func(err error) {
			logger.Error("continuous discovery failed", "error", err)
		},
	)
	logger.Info(
		"continuous discovery started",
		"interval", interval,
		"overnight_enabled", overnightDataEnabled,
	)
	return nil
}

func startCandidateEnrichment(
	ctx context.Context,
	appConfig config.Config,
	store *postgres.Store,
	logger interface {
		Error(string, ...any)
		Info(string, ...any)
	},
) error {
	enabled, err := boolEnvironment(
		"CANDIDATE_ENRICHMENT_ENABLED",
		appConfig.MassiveAPIKey != "",
	)
	if err != nil {
		return err
	}
	if !enabled || appConfig.MassiveAPIKey == "" {
		logger.Info(
			"candidate catalyst enrichment disabled",
			"massive_key_configured",
			appConfig.MassiveAPIKey != "",
		)
		return nil
	}
	interval, err := time.ParseDuration(
		environmentOr("CANDIDATE_ENRICHMENT_INTERVAL", "30s"),
	)
	if err != nil {
		return fmt.Errorf(
			"parsing CANDIDATE_ENRICHMENT_INTERVAL: %w",
			err,
		)
	}
	cooldown, err := time.ParseDuration(
		environmentOr("CANDIDATE_ENRICHMENT_COOLDOWN", "15m"),
	)
	if err != nil {
		return fmt.Errorf(
			"parsing CANDIDATE_ENRICHMENT_COOLDOWN: %w",
			err,
		)
	}
	lookback, err := time.ParseDuration(
		environmentOr("CANDIDATE_NEWS_LOOKBACK", "72h"),
	)
	if err != nil {
		return fmt.Errorf("parsing CANDIDATE_NEWS_LOOKBACK: %w", err)
	}
	candidateLimit, err := intEnvironment(
		"CANDIDATE_ENRICHMENT_CANDIDATE_LIMIT",
		20,
	)
	if err != nil {
		return err
	}
	newsLimit, err := intEnvironment("CANDIDATE_NEWS_LIMIT", 25)
	if err != nil {
		return err
	}
	client, err := massive.NewClient(
		appConfig.MassiveAPIKey,
		massive.WithBaseURL(appConfig.MassiveBaseURL),
		massive.WithHTTPClient(
			&http.Client{Timeout: appConfig.HTTPTimeout},
		),
		// A single enrichment uses news plus float. Thirteen seconds between
		// requests keeps this background path below the free 5 req/min tier.
		massive.WithMinRequestInterval(13*time.Second),
	)
	if err != nil {
		return fmt.Errorf(
			"creating candidate enrichment client: %w",
			err,
		)
	}
	service, err := discovery.NewCandidateEnricher(
		client,
		store,
		discovery.EnrichmentConfig{
			Interval: interval, Cooldown: cooldown,
			NewsLookback: lookback, CandidateLimit: candidateLimit,
			NewsLimit: newsLimit,
		},
	)
	if err != nil {
		return fmt.Errorf("configuring candidate enrichment: %w", err)
	}
	go service.Run(
		ctx,
		func(report discovery.EnrichmentReport) {
			logger.Info(
				"candidate catalyst enrichment completed",
				"ticker", report.Ticker,
				"news_items", report.NewsItems,
				"float_available", report.FloatAvailable,
			)
		},
		func(runErr error) {
			logger.Error(
				"candidate catalyst enrichment failed",
				"error",
				runErr,
			)
		},
	)
	logger.Info(
		"candidate catalyst enrichment started",
		"interval", interval,
		"cooldown", cooldown,
		"maximum_requests_per_minute", 4,
	)
	return nil
}

func tradingSessionConfigured(
	allowed []string,
	session market.Session,
) bool {
	for _, configured := range allowed {
		if strings.EqualFold(strings.TrimSpace(configured), string(session)) {
			return true
		}
	}
	return false
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func mergeTickers(groups ...[]string) []string {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, group := range groups {
		for _, ticker := range group {
			if _, exists := seen[ticker]; exists {
				continue
			}
			seen[ticker] = struct{}{}
			merged = append(merged, ticker)
		}
	}
	return merged
}

func scheduledScannerTickers(maximum int, groups ...[]string) []string {
	return limitTickers(market.NormalizeUSStockSymbols(groups...), maximum)
}

func limitTickers(tickers []string, maximum int) []string {
	if maximum <= 0 || len(tickers) <= maximum {
		return tickers
	}
	return tickers[:maximum]
}
