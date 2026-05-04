// Package proxy: NATS publisher with disk-spool fallback for the
// metering sidecar.
//
// Design contract:
//
//   1. Customer-facing latency MUST NOT include any NATS round-trip
//      synchronously. The proxy hands envelopes to the publisher AFTER
//      the response body has been restored — by then the customer's
//      bytes are already on the wire to the client. A slow publish
//      delays the next response from THIS sidecar but never the
//      current one.
//
//   2. NATS-unreachable >5s MUST NOT drop envelopes. Persisted to disk
//      in the spool directory, retried by DrainSpoolLoop until success.
//
//   3. Disk full MUST NOT crash the sidecar. We log + drop, but the
//      sidecar keeps serving customer traffic. Billing reconciliation
//      from NewAPI's own ledger (the "in-flight cache" per
//      [Q-mine-4]) is the safety net for this rare case.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openova-io/openova/core/services/shared/events"
)

// MeteringPublisher publishes usage envelopes to NATS with a disk-
// spool fallback.
type MeteringPublisher struct {
	NATS           *events.NATSConn
	PublishTimeout time.Duration
	SpoolDir       string

	// Counters surfaced via /metrics. atomic.Int64 so reads from the
	// metrics handler are race-free.
	publishedOK    atomic.Int64
	publishFailed  atomic.Int64
	spooled        atomic.Int64
	spoolDrained   atomic.Int64
	spoolDropFatal atomic.Int64
}

// PublishOrSpool attempts a synchronous publish; on failure (or if no
// NATS connection is configured) the envelope is persisted to disk
// for later retry by DrainSpoolLoop.
func (p *MeteringPublisher) PublishOrSpool(ctx context.Context, env events.UsageRecordedPayload) error {
	if p.NATS != nil {
		pubCtx, cancel := context.WithTimeout(ctx, p.PublishTimeout)
		defer cancel()
		if err := p.NATS.PublishUsage(pubCtx, env); err == nil {
			p.publishedOK.Add(1)
			return nil
		} else {
			p.publishFailed.Add(1)
			slog.Warn("metering: NATS publish failed — falling back to spool",
				"request_id", env.Metadata.RequestID, "error", err)
		}
	}
	return p.spoolEnvelope(env)
}

// spoolEnvelope writes the envelope as JSON to SpoolDir/<request_id>.json.
// Filename uses request_id so a re-spool of the same envelope (during a
// drain failure) overwrites instead of producing duplicates.
func (p *MeteringPublisher) spoolEnvelope(env events.UsageRecordedPayload) error {
	if p.SpoolDir == "" {
		p.spoolDropFatal.Add(1)
		return errors.New("publisher: no spool directory configured")
	}
	body, err := json.Marshal(env)
	if err != nil {
		p.spoolDropFatal.Add(1)
		return fmt.Errorf("publisher: marshal envelope: %w", err)
	}
	name := safeFilename(env.Metadata.RequestID)
	tmp := filepath.Join(p.SpoolDir, name+".tmp")
	final := filepath.Join(p.SpoolDir, name+".json")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		// Disk full / permission error — drop envelope but keep
		// serving customer traffic.
		p.spoolDropFatal.Add(1)
		return fmt.Errorf("publisher: spool write: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		p.spoolDropFatal.Add(1)
		return fmt.Errorf("publisher: spool rename: %w", err)
	}
	p.spooled.Add(1)
	return nil
}

// DrainSpoolLoop polls the spool directory at the given interval and
// republishes each envelope. Cancelled by ctx. The interval should be
// short enough to keep accumulated envelopes flowing once NATS recovers
// (default: 30s) but long enough that a NATS outage does not trigger a
// hot loop.
func (p *MeteringPublisher) DrainSpoolLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.DrainSpoolOnce(ctx)
		}
	}
}

// DrainSpoolOnce iterates the spool directory once and tries to
// publish each envelope. Successful publishes delete the file; failed
// ones leave it for the next pass.
func (p *MeteringPublisher) DrainSpoolOnce(ctx context.Context) {
	if p.NATS == nil {
		return
	}
	if p.SpoolDir == "" {
		return
	}
	entries, err := os.ReadDir(p.SpoolDir)
	if err != nil {
		slog.Warn("metering: spool dir read failed", "dir", p.SpoolDir, "error", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(p.SpoolDir, e.Name())
		body, err := os.ReadFile(full)
		if err != nil {
			slog.Warn("metering: spool file read failed", "file", full, "error", err)
			continue
		}
		var env events.UsageRecordedPayload
		if err := json.Unmarshal(body, &env); err != nil {
			// Corrupt envelope — remove so we don't retry forever.
			slog.Error("metering: corrupt spooled envelope — removing",
				"file", full, "error", err)
			os.Remove(full)
			p.spoolDropFatal.Add(1)
			continue
		}
		pubCtx, cancel := context.WithTimeout(ctx, p.PublishTimeout)
		err = p.NATS.PublishUsage(pubCtx, env)
		cancel()
		if err != nil {
			// Still down — leave for next pass.
			return
		}
		if rmErr := os.Remove(full); rmErr != nil {
			slog.Warn("metering: spool remove failed",
				"file", full, "error", rmErr)
		}
		p.spoolDrained.Add(1)
	}
}

// MetricsSnapshot returns the publisher's atomic counters as a
// human-readable map. Used by the /metrics handler.
func (p *MeteringPublisher) MetricsSnapshot() map[string]int64 {
	return map[string]int64{
		"published_ok":     p.publishedOK.Load(),
		"publish_failed":   p.publishFailed.Load(),
		"spooled":          p.spooled.Load(),
		"spool_drained":    p.spoolDrained.Load(),
		"spool_drop_fatal": p.spoolDropFatal.Load(),
	}
}

// safeFilename strips path-unsafe characters from a request_id so
// the spool filename cannot escape SpoolDir.
func safeFilename(s string) string {
	if s == "" {
		// Anonymous envelope — fall back to a per-process unique
		// name. Safe because we never receive multiple anonymous
		// envelopes within the same nanosecond.
		return fmt.Sprintf("anon-%d", time.Now().UnixNano())
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || out[0] == '.' {
		out = "x" + out
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}
