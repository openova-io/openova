// Package runtime loads + validates the projector's runtime configuration
// from environment variables (per docs/INVIOLABLE-PRINCIPLES.md #4 —
// never hardcode).
package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	NATSURL          string
	NATSStream       string
	NATSSubject      string
	NATSDurable      string
	NATSAckWait      time.Duration
	NATSMaxDeliver   int
	NATSBackoffMin   time.Duration
	NATSBackoffMax   time.Duration

	ValkeyAddr     string
	ValkeyUsername string
	ValkeyPassword string
	ValkeyTTL      time.Duration

	Cluster      string
	ColdStart    bool
	LogLevel     string
}

// LoadFromEnv reads the env-var contract and returns a validated Config.
//
// Env-var contract:
//
//	NATS_URL              — nats://<host>:<port>; default ""
//	NATS_STREAM           — default "catalyst.events"
//	NATS_SUBJECT          — default "catalyst.events.>"
//	NATS_DURABLE          — default "catalyst-projector-${HOSTNAME}"
//	NATS_ACK_WAIT         — default "30s"
//	NATS_MAX_DELIVER      — default 5
//	NATS_BACKOFF_MIN      — default "1s"
//	NATS_BACKOFF_MAX      — default "30s"
//	VALKEY_ADDR           — host:port; REQUIRED
//	VALKEY_USERNAME       — default ""
//	VALKEY_PASSWORD_FILE  — path to file with the Valkey password; optional
//	VALKEY_TTL            — per-key TTL; default "24h"
//	CLUSTER_ID            — Sovereign cluster id used in keys; REQUIRED
//	COLD_START            — "true"/"false"; default "true"
//	LOG_LEVEL             — debug/info/warn/error; default "info"
func LoadFromEnv() (Config, error) {
	cfg := Config{
		NATSURL:        getenvDefault("NATS_URL", ""),
		NATSStream:     getenvDefault("NATS_STREAM", "catalyst.events"),
		NATSSubject:    getenvDefault("NATS_SUBJECT", "catalyst.events.>"),
		NATSDurable:    getenvDefault("NATS_DURABLE", "catalyst-projector-"+os.Getenv("HOSTNAME")),
		ValkeyAddr:     strings.TrimSpace(os.Getenv("VALKEY_ADDR")),
		ValkeyUsername: os.Getenv("VALKEY_USERNAME"),
		Cluster:        strings.TrimSpace(os.Getenv("CLUSTER_ID")),
		LogLevel:       getenvDefault("LOG_LEVEL", "info"),
		ColdStart:      true,
	}
	if cfg.ValkeyAddr == "" {
		return cfg, errors.New("VALKEY_ADDR is required (host:port)")
	}
	if cfg.Cluster == "" {
		return cfg, errors.New("CLUSTER_ID is required (Sovereign cluster identifier)")
	}

	cfg.NATSAckWait = mustParseDur(getenvDefault("NATS_ACK_WAIT", "30s"), 30*time.Second)
	cfg.NATSBackoffMin = mustParseDur(getenvDefault("NATS_BACKOFF_MIN", "1s"), time.Second)
	cfg.NATSBackoffMax = mustParseDur(getenvDefault("NATS_BACKOFF_MAX", "30s"), 30*time.Second)
	cfg.ValkeyTTL = mustParseDur(getenvDefault("VALKEY_TTL", "24h"), 24*time.Hour)

	cfg.NATSMaxDeliver = 5
	if v := strings.TrimSpace(os.Getenv("NATS_MAX_DELIVER")); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			return cfg, fmt.Errorf("NATS_MAX_DELIVER=%q invalid", v)
		}
		cfg.NATSMaxDeliver = n
	}

	if v := strings.TrimSpace(os.Getenv("COLD_START")); v != "" {
		cfg.ColdStart = strings.EqualFold(v, "true")
	}

	if pwFile := strings.TrimSpace(os.Getenv("VALKEY_PASSWORD_FILE")); pwFile != "" {
		raw, err := os.ReadFile(pwFile)
		if err != nil {
			return cfg, fmt.Errorf("read VALKEY_PASSWORD_FILE %q: %w", pwFile, err)
		}
		cfg.ValkeyPassword = strings.TrimSpace(string(raw))
	}

	return cfg, nil
}

// ParseLogLevel maps the env LOG_LEVEL string to a slog.Level.
func ParseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func getenvDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func mustParseDur(raw string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
