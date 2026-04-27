package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ListenAddr     string
	UpstreamURL    string
	UpstreamAPIKey string

	DatabaseURL string
	RedisURL    string

	AdminPassword string
	SessionSecret string
	SessionTTL    time.Duration
	CookieSecure  bool

	KeyPrefix      string
	PricingPath    string
	StreamTimeout  time.Duration
	VerifyCacheTTL time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:     env("LISTEN_ADDR", ":8080"),
		UpstreamURL:    env("UPSTREAM_URL", "https://api.openai.com"),
		UpstreamAPIKey: os.Getenv("OPENAI_API_KEY"),

		DatabaseURL: os.Getenv("DATABASE_URL"),
		RedisURL:    os.Getenv("REDIS_URL"),

		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		SessionSecret: os.Getenv("SESSION_SECRET"),
		SessionTTL:    durationEnv("SESSION_TTL", 24*time.Hour),
		CookieSecure:  env("COOKIE_SECURE", "true") != "false",

		KeyPrefix:      env("KEY_PREFIX", "sk-pxy-"),
		PricingPath:    env("PRICING_PATH", "pricing.yaml"),
		StreamTimeout:  durationEnv("STREAM_TIMEOUT", 30*time.Minute),
		VerifyCacheTTL: durationEnv("VERIFY_CACHE_TTL", 60*time.Second),
	}

	required := map[string]string{
		"OPENAI_API_KEY":  cfg.UpstreamAPIKey,
		"DATABASE_URL":    cfg.DatabaseURL,
		"REDIS_URL":       cfg.RedisURL,
		"ADMIN_PASSWORD":  cfg.AdminPassword,
		"SESSION_SECRET":  cfg.SessionSecret,
	}
	for name, val := range required {
		if val == "" {
			return nil, fmt.Errorf("%s is required", name)
		}
	}
	if len(cfg.SessionSecret) < 16 {
		return nil, fmt.Errorf("SESSION_SECRET must be at least 16 characters")
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func durationEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}
