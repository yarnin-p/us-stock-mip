package main

import (
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
)

func TestRuntimeHealthRegistryReturnsSortedSnapshot(t *testing.T) {
	t.Parallel()

	registry := newRuntimeHealthRegistry()
	registry.Set(dashboard.ComponentHealth{
		Name: "Massive News", Status: "CONNECTED",
	})
	registry.Set(dashboard.ComponentHealth{
		Name: "Alpaca News", Status: "WAITING",
	})
	registry.Set(dashboard.ComponentHealth{
		Name: "Alpaca News", Status: "CONNECTED",
	})

	got := registry.Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot() = %#v", got)
	}
	if got[0].Name != "Alpaca News" || got[0].Status != "CONNECTED" ||
		got[1].Name != "Massive News" {
		t.Fatalf("Snapshot() = %#v", got)
	}
}
