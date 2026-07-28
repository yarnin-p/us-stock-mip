package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultMassiveBaseURL = "https://api.massive.com"

type Config struct {
	DatabaseURL    string
	MassiveAPIKey  string
	MassiveBaseURL string
	HTTPTimeout    time.Duration
	DBMaxConns     int32
	DBMinConns     int32
	LogLevel       string
}

func Load() (Config, error) {
	config := Config{
		DatabaseURL:    strings.TrimSpace(os.Getenv("DATABASE_URL")),
		MassiveAPIKey:  strings.TrimSpace(os.Getenv("MASSIVE_API_KEY")),
		MassiveBaseURL: envOrDefault("MASSIVE_BASE_URL", defaultMassiveBaseURL),
		LogLevel:       strings.ToLower(envOrDefault("LOG_LEVEL", "info")),
	}

	var err error
	config.HTTPTimeout, err = durationEnv("HTTP_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	config.DBMaxConns, err = int32Env("DB_MAX_CONNS", 10)
	if err != nil {
		return Config{}, err
	}
	config.DBMinConns, err = int32Env("DB_MIN_CONNS", 2)
	if err != nil {
		return Config{}, err
	}

	if config.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if config.MassiveAPIKey == "" {
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
