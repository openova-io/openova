// Package natspub — compliance_rollup_publisher.go: concrete NATS JetStream
// KV binding for the compliance aggregator's `policy-rollup` history per
// ADR-0001 §3.
//
// Provenance: Wave 5.44 (#2251) follow-up to the audit finding that
// `main.go:1884-1900` `newCompliancePolicyRollupPublisherFromEnv` returned
// nil unconditionally even when `CATALYST_NATS_URL` was set, with the
// comment "actual NATS client is wired by a follow-up slice that imports
// nats.go". This is that follow-up slice. nats.go landed in catalyst-api's
// go.mod via TBD-D35c (PR #1918 chain → sandbox_publisher.go); the same
// dependency now backs this KV publisher.
//
// Why JetStream KV (not core publish like sandbox_publisher.go):
//
//   - The aggregator needs REPLAYABLE history — operators reopening
//     /compliance/scorecard after a catalyst-api restart expect the most
//     recent score, not a recompute lag from k8scache rehydration. KV's
//     latest-value-per-key semantics map directly.
//   - SSE consumers can replay by ListKeys + Get on startup; core
//     publish + at-most-once would lose this.
//   - The aggregator's Put rate is low (one per scope-change event, not
//     per K8s event) so JetStream's per-message overhead is negligible.
//
// Bucket name `policy-rollup` is fixed per ADR-0001 §3 + EPIC #1096 step 6.
// Provisioning of the bucket itself lives in bp-nats-jetstream's chart
// (slice H4); this publisher is consumer-side and auto-creates the bucket
// on first Put if missing (idempotent — re-create on existing returns
// success).
package natspub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
)

// jsKV is the minimal `nats.KeyValue` surface the publisher uses. The
// interface lets unit tests substitute a fake without bringing up an
// embedded NATS+JetStream server (mirrors the sandbox_publisher.go
// fake-only test style — see compliance_rollup_publisher_test.go).
type jsKV interface {
	// Put writes value at key. Mirrors `nats.KeyValue`.Put — returns
	// the revision on success, error on broker / auth failure.
	Put(key string, value []byte) (uint64, error)
}

// ComplianceRollupPublisher implements handler.PolicyRollupPublisher
// against a NATS JetStream KV bucket. The zero value is NOT useable —
// construct via NewComplianceRollupPublisher.
type ComplianceRollupPublisher struct {
	kv     jsKV
	log    *slog.Logger
	bucket string

	// publish-failure counter for the catalyst_compliance_nats_publish_
	// failures_total metric (audit finding #5). Wired by the handler
	// package's metric registration in compliance.go; we just bump it
	// on every Put error so an operator gets a /metrics signal beyond
	// the warn-log.
	onFailure func()

	// Read at startup; never mutated. Used in log lines to disambiguate
	// multi-Sovereign mothership operation.
	source string

	// connClose is invoked by Close. Set when this publisher owns the
	// underlying connection (production wiring); left nil when an
	// already-open connection is injected.
	closeMu sync.Mutex
	connClose func()
}

// Compile-time assertion that the impl satisfies the handler interface.
var _ handler.PolicyRollupPublisher = (*ComplianceRollupPublisher)(nil)

// NewComplianceRollupPublisher dials NATS at url, opens (or creates) the
// JetStream KV bucket, and returns a ready publisher. Caller owns Close.
//
// onPublishFailure is a callback invoked on every Put error so the caller
// can bump a Prometheus counter without coupling natspub to the metrics
// registry. Pass nil to skip.
//
// Returns (nil, err) on dial / JetStream / bucket-open failure; the
// caller should log + fall back to a nil PolicyRollupPublisher (the
// aggregator runs in best-effort mode without replayable history).
func NewComplianceRollupPublisher(
	url string,
	bucket string,
	source string,
	log *slog.Logger,
	onPublishFailure func(),
) (*ComplianceRollupPublisher, error) {
	if url == "" {
		return nil, errors.New("natspub: CATALYST_NATS_URL is empty")
	}
	if bucket == "" {
		bucket = "policy-rollup"
	}
	nc, err := nats.Connect(url,
		nats.Name(fmt.Sprintf("catalyst-api/compliance-rollup/%s", source)),
		nats.Timeout(10*time.Second),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("natspub: nats.Connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("natspub: nc.JetStream: %w", err)
	}
	kv, err := js.KeyValue(bucket)
	if err != nil {
		// Bucket not provisioned yet — try create. Idempotent on retry
		// (CreateKeyValue returns the existing handle if it already
		// exists with matching config).
		kv, err = js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket:      bucket,
			Description: "catalyst-api compliance score rollup history (ADR-0001 §3)",
			History:     5, // keep last 5 revisions per key for short-window replay
			Storage:     nats.FileStorage,
		})
		if err != nil {
			nc.Close()
			return nil, fmt.Errorf("natspub: open/create KV %q: %w", bucket, err)
		}
	}
	p := &ComplianceRollupPublisher{
		kv:        kv,
		log:       log.With("component", "compliance-rollup-publisher", "bucket", bucket),
		bucket:    bucket,
		onFailure: onPublishFailure,
		source:    source,
		connClose: nc.Close,
	}
	p.log.Info("NATS KV publisher ready", "url", url, "source", source)
	return p, nil
}

// Put writes value at key. Errors are logged + counted but never
// propagated to the aggregator — a transient NATS outage must NEVER
// wedge the in-process scorecard hot path (per compliance.go:944-959
// best-effort contract).
//
// Returning the error here would surface it to the handler's
// recomputeAndPublish which logs+continues; this method does the same
// in-line so the failure-counter increments at the source.
func (p *ComplianceRollupPublisher) Put(ctx context.Context, key string, value []byte) error {
	if p == nil || p.kv == nil {
		return errors.New("natspub: publisher closed")
	}
	if _, err := p.kv.Put(key, value); err != nil {
		p.log.Warn("KV Put failed", "key", key, "err", err)
		if p.onFailure != nil {
			p.onFailure()
		}
		return fmt.Errorf("natspub: kv.Put %q: %w", key, err)
	}
	return nil
}

// Close terminates the underlying NATS connection. Safe to call
// multiple times.
func (p *ComplianceRollupPublisher) Close() {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.connClose != nil {
		p.connClose()
		p.connClose = nil
	}
}
