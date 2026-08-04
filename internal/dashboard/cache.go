package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const dashboardCacheSchemaVersion = "v3"

type CachedRepository struct {
	next  Repository
	redis *redis.Client
}

func NewCachedRepository(next Repository, client *redis.Client) *CachedRepository {
	return &CachedRepository{next: next, redis: client}
}

// Invalidate removes only the cache entries affected by a database event.
// Unknown scopes are intentionally ignored so new event producers cannot
// accidentally flush the whole dashboard cache.
func (cache *CachedRepository) Invalidate(ctx context.Context, scope string) error {
	keys := cacheKeysForScope(scope)
	if len(keys) == 0 {
		return nil
	}
	return cache.redis.Del(ctx, keys...).Err()
}

func dashboardCacheKey(name string) string {
	return "dashboard:" + dashboardCacheSchemaVersion + ":" + name
}

func cacheKeysForScope(scope string) []string {
	scopeNames := map[string][]string{
		"candidates": {"candidates"},
		"scan":       {"scan"},
		"watchlist":  {"watchlist"},
		"positions":  {"positions"},
		"trades":     {"trades"},
		"alerts":     {"alerts"},
		"execution":  {"learning-report"},
		"learning":   {"learning-report"},
	}
	unique := make(map[string]struct{})
	keys := make([]string, 0)
	for _, part := range strings.Split(scope, ",") {
		for _, name := range scopeNames[strings.TrimSpace(part)] {
			key := dashboardCacheKey(name)
			if _, exists := unique[key]; exists {
				continue
			}
			unique[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

func (cache *CachedRepository) Candidates(ctx context.Context) ([]Candidate, error) {
	var result []Candidate
	err := cache.load(ctx, dashboardCacheKey("candidates"), 30*time.Second, &result, func() error {
		var err error
		result, err = cache.next.Candidates(ctx)
		return err
	})
	return result, err
}

func (cache *CachedRepository) Scan(ctx context.Context) ([]ScanSignal, error) {
	var result []ScanSignal
	err := cache.load(ctx, dashboardCacheKey("scan"), 30*time.Second, &result, func() error {
		var err error
		result, err = cache.next.Scan(ctx)
		return err
	})
	return result, err
}

func (cache *CachedRepository) Watchlist(
	ctx context.Context,
) ([]WatchlistItem, error) {
	var result []WatchlistItem
	err := cache.load(ctx, dashboardCacheKey("watchlist"), 5*time.Second, &result, func() error {
		var err error
		result, err = cache.next.Watchlist(ctx)
		return err
	})
	return result, err
}

func (cache *CachedRepository) Positions(ctx context.Context) ([]Position, error) {
	var result []Position
	err := cache.load(ctx, dashboardCacheKey("positions"), time.Second, &result, func() error {
		var err error
		result, err = cache.next.Positions(ctx)
		return err
	})
	return result, err
}

func (cache *CachedRepository) Trades(ctx context.Context) ([]Trade, error) {
	var result []Trade
	err := cache.load(ctx, dashboardCacheKey("trades"), 5*time.Second, &result, func() error {
		var err error
		result, err = cache.next.Trades(ctx)
		return err
	})
	return result, err
}

func (cache *CachedRepository) UpsertWatchlist(
	ctx context.Context, input WatchlistInput,
) (WatchlistItem, error) {
	item, err := cache.next.UpsertWatchlist(ctx, input)
	if err == nil {
		_ = cache.redis.Del(ctx, dashboardCacheKey("watchlist")).Err()
	}
	return item, err
}

func (cache *CachedRepository) DeleteWatchlist(
	ctx context.Context, ticker string,
) error {
	err := cache.next.DeleteWatchlist(ctx, ticker)
	if err == nil {
		_ = cache.redis.Del(ctx, dashboardCacheKey("watchlist")).Err()
	}
	return err
}

func (cache *CachedRepository) CreateTrade(
	ctx context.Context, input TradeInput,
) (Trade, error) {
	trade, err := cache.next.CreateTrade(ctx, input)
	if err == nil {
		_ = cache.redis.Del(
			ctx,
			dashboardCacheKey("positions"),
			dashboardCacheKey("trades"),
		).Err()
	}
	return trade, err
}

func (cache *CachedRepository) ApplyTradeEvent(
	ctx context.Context, id int64, input TradeEventInput,
) (Trade, error) {
	trade, err := cache.next.ApplyTradeEvent(ctx, id, input)
	if err == nil {
		_ = cache.redis.Del(
			ctx,
			dashboardCacheKey("positions"),
			dashboardCacheKey("trades"),
		).Err()
	}
	return trade, err
}

func (cache *CachedRepository) ScoreHistory(
	ctx context.Context, ticker string,
) ([]ScorePoint, error) {
	return cache.next.ScoreHistory(ctx, ticker)
}

func (cache *CachedRepository) Alerts(ctx context.Context) ([]Alert, error) {
	var result []Alert
	err := cache.load(ctx, dashboardCacheKey("alerts"), 3*time.Second, &result, func() error {
		var err error
		result, err = cache.next.Alerts(ctx)
		return err
	})
	return result, err
}

func (cache *CachedRepository) AcknowledgeAlert(
	ctx context.Context, id int64,
) error {
	err := cache.next.AcknowledgeAlert(ctx, id)
	if err == nil {
		_ = cache.redis.Del(ctx, dashboardCacheKey("alerts")).Err()
	}
	return err
}

func (cache *CachedRepository) SystemHealth(
	ctx context.Context,
) (SystemHealth, error) {
	return cache.next.SystemHealth(ctx)
}

func (cache *CachedRepository) BrokerPositions(
	ctx context.Context,
) ([]BrokerPosition, error) {
	return cache.next.BrokerPositions(ctx)
}

func (cache *CachedRepository) BrokerOrders(
	ctx context.Context,
) ([]BrokerOrder, error) {
	return cache.next.BrokerOrders(ctx)
}

func (cache *CachedRepository) LearningReport(
	ctx context.Context,
) (LearningReport, error) {
	var result LearningReport
	err := cache.load(
		ctx,
		dashboardCacheKey("learning-report"),
		5*time.Minute,
		&result,
		func() error {
			var err error
			result, err = cache.next.LearningReport(ctx)
			return err
		},
	)
	return result, err
}

func (cache *CachedRepository) load(
	ctx context.Context,
	key string,
	ttl time.Duration,
	target any,
	fallback func() error,
) error {
	encoded, err := cache.redis.Get(ctx, key).Bytes()
	if err == nil && json.Unmarshal(encoded, target) == nil {
		return nil
	}
	if err := fallback(); err != nil {
		return err
	}
	encoded, err = json.Marshal(target)
	if err == nil {
		_ = cache.redis.Set(ctx, key, encoded, ttl).Err()
	}
	return nil
}
