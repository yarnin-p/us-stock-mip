package dashboard

import (
	"slices"
	"strings"
	"testing"
)

func TestCacheKeysForScopeVersionedAndExecutionRefreshesLearning(t *testing.T) {
	keys := cacheKeysForScope("execution")
	if !slices.Contains(keys, dashboardCacheKey("learning-report")) {
		t.Fatalf("execution invalidation keys = %v", keys)
	}
	for _, key := range append(
		keys,
		dashboardCacheKey("candidates"),
		dashboardCacheKey("learning-report"),
	) {
		if !strings.HasPrefix(key, "dashboard:v3:") {
			t.Fatalf("unversioned dashboard cache key %q", key)
		}
	}
}

func TestCacheKeysForScopeDeduplicatesCombinedScopes(t *testing.T) {
	keys := cacheKeysForScope("execution,learning,execution")
	if len(keys) != 1 || keys[0] != dashboardCacheKey("learning-report") {
		t.Fatalf("combined invalidation keys = %v", keys)
	}
}
