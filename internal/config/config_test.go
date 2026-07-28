package config_test

import (
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
