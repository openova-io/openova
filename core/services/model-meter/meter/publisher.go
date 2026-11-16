// The JetStream publisher of the usage event, with a disk spool.
//
// Design contract:
//
//  1. The event is published after the response has been written to the caller, so a slow
//     publish never delays the caller's bytes.
//
//  2. A publish that fails, or that is not acknowledged within PublishTimeout, does not lose
//     the event. The envelope bytes are written to the spool directory, in a file named by the
//     event identifier, and DrainSpoolLoop publishes them again with the same identifier until
//     the bus acknowledges them. The bus keeps one copy of a message published twice.
//
//  3. A full disk does not stop the meter. A spool write that fails is counted as dropped and
//     logged at error level. It is the only path that loses an event.

package meter

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
)

// Bus publishes one message on a JetStream subject with a message identifier and returns after
// the acknowledgement. *events.NATSConn of core/services/shared implements it.
type Bus interface {
	PublishRaw(ctx context.Context, subject, msgID string, body []byte) error
}

// Publisher publishes or spools one event; it never blocks the response path.
type Publisher interface {
	Publish(ctx context.Context, ev UsageEvent) error
}

// ErrSpooled is returned by Publish when the event was written to the spool directory for a
// later publish.
var ErrSpooled = errors.New("meter: the event was spooled for a later publish")

// SpoolPublisher publishes usage events to JetStream with a disk-spool fallback.
type SpoolPublisher struct {
	Bus            Bus // nil: every event is spooled
	Source         string
	PublishTimeout time.Duration
	SpoolDir       string

	// Counters, read with MetricsSnapshot. atomic.Int64 so reads are race-free.
	publishedOK    atomic.Int64
	publishFailed  atomic.Int64
	spooled        atomic.Int64
	spoolDrained   atomic.Int64
	spoolDropFatal atomic.Int64
}

// Publish publishes the event synchronously and returns nil after the acknowledgement. When no
// bus is configured, or the publish fails, the envelope is written to the spool directory and
// Publish returns ErrSpooled. Any other error means the event is lost.
func (p *SpoolPublisher) Publish(ctx context.Context, ev UsageEvent) error {
	body, err := ev.CloudEvent(p.Source)
	if err != nil {
		p.spoolDropFatal.Add(1)
		slog.Error("meter: the event cannot be encoded and is dropped", "id", ev.ID, "error", err)
		return err
	}
	if p.Bus != nil {
		pubCtx, cancel := context.WithTimeout(ctx, p.PublishTimeout)
		err := p.Bus.PublishRaw(pubCtx, Subject, ev.ID, body)
		cancel()
		if err == nil {
			p.publishedOK.Add(1)
			return nil
		}
		p.publishFailed.Add(1)
		slog.Warn("meter: publish failed, spooling the event", "id", ev.ID, "error", err)
	}
	if err := p.spoolEnvelope(ev.ID, body); err != nil {
		p.spoolDropFatal.Add(1)
		slog.Error("meter: the event could not be spooled and is dropped", "id", ev.ID, "error", err)
		return err
	}
	p.spooled.Add(1)
	return ErrSpooled
}

// spoolEnvelope writes the envelope bytes to SpoolDir/<id>.json. The file name is the event
// identifier, so a second spool of the same event overwrites the first.
func (p *SpoolPublisher) spoolEnvelope(id string, body []byte) error {
	if p.SpoolDir == "" {
		return errors.New("meter: no spool directory configured")
	}
	name := safeFilename(id)
	tmp := filepath.Join(p.SpoolDir, name+".tmp")
	final := filepath.Join(p.SpoolDir, name+".json")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("meter: spool write: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		if rmErr := os.Remove(tmp); rmErr != nil {
			slog.Warn("meter: spool temporary file remove failed", "file", tmp, "error", rmErr)
		}
		return fmt.Errorf("meter: spool rename: %w", err)
	}
	return nil
}

// DrainSpoolLoop drains the spool directory at the given interval until ctx is cancelled.
func (p *SpoolPublisher) DrainSpoolLoop(ctx context.Context, interval time.Duration) {
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

// DrainSpoolOnce reads the spool directory once and publishes each envelope again, with the
// event identifier of the envelope as the message identifier. A published envelope's file is
// removed. When a publish fails the drain stops and the files stay for the next pass. A file
// that is not an envelope with an identifier is removed and counted as dropped.
func (p *SpoolPublisher) DrainSpoolOnce(ctx context.Context) {
	if p.Bus == nil || p.SpoolDir == "" {
		return
	}
	entries, err := os.ReadDir(p.SpoolDir)
	if err != nil {
		slog.Warn("meter: spool directory read failed", "dir", p.SpoolDir, "error", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Clean(filepath.Join(p.SpoolDir, e.Name()))
		body, err := os.ReadFile(full)
		if err != nil {
			slog.Warn("meter: spool file read failed", "file", full, "error", err)
			continue
		}
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &head); err != nil || head.ID == "" {
			slog.Error("meter: a spooled file is not an envelope, removing it", "file", full)
			if rmErr := os.Remove(full); rmErr != nil {
				slog.Warn("meter: spool remove failed", "file", full, "error", rmErr)
			}
			p.spoolDropFatal.Add(1)
			continue
		}
		pubCtx, cancel := context.WithTimeout(ctx, p.PublishTimeout)
		err = p.Bus.PublishRaw(pubCtx, Subject, head.ID, body)
		cancel()
		if err != nil {
			return // still down: leave the files for the next pass
		}
		if rmErr := os.Remove(full); rmErr != nil {
			slog.Warn("meter: spool remove failed", "file", full, "error", rmErr)
		}
		p.spoolDrained.Add(1)
	}
}

// MetricsSnapshot returns the publisher's counters.
func (p *SpoolPublisher) MetricsSnapshot() map[string]int64 {
	return map[string]int64{
		"published_ok":     p.publishedOK.Load(),
		"publish_failed":   p.publishFailed.Load(),
		"spooled":          p.spooled.Load(),
		"spool_drained":    p.spoolDrained.Load(),
		"spool_drop_fatal": p.spoolDropFatal.Load(),
	}
}

// safeFilename strips path-unsafe characters from an identifier so that the spool file name
// cannot escape SpoolDir.
func safeFilename(s string) string {
	if s == "" {
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
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}
