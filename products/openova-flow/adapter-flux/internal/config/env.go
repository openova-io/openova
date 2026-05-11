// Package config — runtime knobs from env vars. Per
// docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) every parameter
// the adapter needs at boot is operator-driven.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config — resolved at process start. Immutable thereafter.
type Config struct {
	// FlowServerURL — base URL of the openova-flow-server, e.g.
	// "https://openova-flow.<sov-fqdn>". The adapter appends
	// /v1/flows/<FlowID>/events to POST events.
	FlowServerURL string

	// FlowID — runtime FlowInstance id this adapter sidecar binds to.
	// Typically the Sovereign deployment id ("dep-abc123") on
	// post-handover chroots, or the cluster id on the mother.
	FlowID string

	// RegionKey — region the adapter operates on, e.g. "fsn1".
	// Region-aware so multi-region renders correctly (one bubble per
	// region per HR-named family).
	RegionKey string

	// NamespaceFilter — informer scope. Empty == all namespaces.
	// Defaults to "flux-system" (canonical Flux namespace).
	NamespaceFilter string

	// EmitInterval — minimum delay between successive POSTs for the
	// SAME (node ID, status) tuple. The informer fires per event;
	// the emitter coalesces consecutive identical states into one
	// POST.
	EmitInterval time.Duration

	// PostTimeout — max wall-clock per HTTP POST to the flow server.
	PostTimeout time.Duration
}

// FromEnv parses Config out of process env. Returns an error when a
// required value is missing.
func FromEnv() (Config, error) {
	c := Config{
		FlowServerURL:   strings.TrimRight(os.Getenv("FLOW_SERVER_URL"), "/"),
		FlowID:          os.Getenv("FLOW_ID"),
		RegionKey:       os.Getenv("REGION_KEY"),
		NamespaceFilter: envDefault("NAMESPACE_FILTER", "flux-system"),
	}
	if c.FlowServerURL == "" {
		return c, errors.New("config: FLOW_SERVER_URL is required")
	}
	if c.FlowID == "" {
		return c, errors.New("config: FLOW_ID is required")
	}
	if c.RegionKey == "" {
		return c, errors.New("config: REGION_KEY is required")
	}
	emit, err := time.ParseDuration(envDefault("EMIT_INTERVAL", "200ms"))
	if err != nil {
		return c, fmt.Errorf("config: bad EMIT_INTERVAL: %w", err)
	}
	c.EmitInterval = emit
	post, err := time.ParseDuration(envDefault("POST_TIMEOUT", "10s"))
	if err != nil {
		return c, fmt.Errorf("config: bad POST_TIMEOUT: %w", err)
	}
	c.PostTimeout = post
	return c, nil
}

func envDefault(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}
