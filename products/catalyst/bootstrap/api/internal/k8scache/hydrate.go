// hydrate.go — pre-populate the Indexer from disk before the watch
// starts.
//
// Per ADR-0001 §5.1 the cold-start budget without snapshot is 1–30s;
// with snapshot it is <1s. The mechanism:
//
//  1. Factory.NewFactory builds dynamicinformer.SharedInformerFactory
//     per cluster, registers EventHandlers (no-op until Start).
//  2. Factory.Start() — BEFORE calling cs.factory.Start(stop) — calls
//     hydrateAll. For each (cluster, kind) snapshot file on disk:
//     a. If the file is older than SnapshotMaxAge, drop it; the
//        informer's normal LIST will populate the cache.
//     b. Decode the envelope. Push every item into the Indexer via
//        cache.Indexer.Add with metadata.resourceVersion preserved.
//        The informer's controller picks up the seeded objects on
//        the same LIST→WATCH path it would otherwise use.
//  3. cs.factory.Start(stop) — informers begin LIST+WATCH. The LIST
//     reconciles the seeded set against the apiserver (so deletes
//     since the snapshot show up via the cache.Replace path); the
//     WATCH then carries the live diff.
//
// Hydrate is best-effort. A failure to decode any snapshot file
// downgrades to "do a normal LIST" — the catalyst-api never wedges
// because of bad snapshot bytes.
package k8scache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// hydrateAll iterates every cluster's registered (kind → informer)
// pair and pre-seeds the Indexer from any matching snapshot file on
// disk.
func (f *Factory) hydrateAll(_ context.Context) {
	f.mu.RLock()
	clusters := make([]*clusterState, 0, len(f.clusters))
	for _, cs := range f.clusters {
		clusters = append(clusters, cs)
	}
	f.mu.RUnlock()

	for _, cs := range clusters {
		for kindName, inf := range cs.informers {
			path := snapshotPath(f.cfg.SnapshotDir, cs.id, kindName)
			outcome, count, err := f.hydrateOne(cs, kindName, inf.GetIndexer(), path)
			if err != nil {
				f.log.Warn("k8scache: hydrate failed",
					"cluster", cs.id, "kind", kindName, "path", path, "err", err)
			}
			metricSnapshotHydrate.WithLabelValues(cs.id, kindName, outcome).Add(1)
			if count > 0 {
				f.log.Info("k8scache: hydrated from snapshot",
					"cluster", cs.id, "kind", kindName,
					"items", count, "outcome", outcome)
			}
		}
	}
}

// hydrateOne — pre-seed a single Indexer from disk. Returns
// (outcome, items-loaded, err). Outcome:
//
//	"hydrated" — Indexer pre-populated with `count` items.
//	"missing"  — no snapshot file on disk.
//	"expired"  — snapshot found but older than SnapshotMaxAge.
//	"failed"   — snapshot found but decode/push failed.
//
// On any non-"hydrated" outcome the caller still proceeds with a
// normal LIST via factory.Start.
func (f *Factory) hydrateOne(cs *clusterState, kindName string, idx hydrateIndexer, path string) (outcome string, count int, err error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", 0, nil
		}
		return "failed", 0, fmt.Errorf("read snapshot: %w", err)
	}

	var env snapshotEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "failed", 0, fmt.Errorf("decode snapshot: %w", err)
	}
	if env.Version != snapshotEnvelopeVersion {
		return "expired", 0, fmt.Errorf("snapshot version %d != current %d", env.Version, snapshotEnvelopeVersion)
	}
	if env.Cluster != cs.id || env.Kind != kindName {
		return "failed", 0, fmt.Errorf("snapshot mismatch: file=%q/%q expected=%q/%q",
			env.Cluster, env.Kind, cs.id, kindName)
	}
	age := time.Since(env.Wrote)
	if age > f.cfg.SnapshotMaxAge {
		return "expired", 0, nil
	}

	for _, it := range env.Items {
		if it == nil {
			continue
		}
		// Drop empty objects defensively — a corrupt snapshot must
		// not crash the dynamic informer's resync logic.
		if len(it.Object) == 0 {
			continue
		}
		if err := idx.Add(it); err != nil {
			return "failed", count, fmt.Errorf("indexer.Add: %w", err)
		}
		count++

		// Update last-event metadata so /healthz reads "fresh-ish"
		// while the watch reconnect lands. The first WATCH event
		// will overwrite this with a precise timestamp.
		cs.lastEventLock.Lock()
		cs.lastEventAt[kindName] = env.Wrote
		cs.lastEventLock.Unlock()
		metricLastEvent.WithLabelValues(cs.id, kindName).Set(float64(env.Wrote.Unix()))
	}
	metricCacheSize.WithLabelValues(cs.id, kindName).Set(float64(count))
	return "hydrated", count, nil
}

// hydrateIndexer is the minimal interface hydrateOne needs. The real
// implementation is cache.Indexer; tests inject a fake.
type hydrateIndexer interface {
	Add(obj any) error
}

// LoadSnapshotForTest is a test helper that exposes the hydrate path
// without spinning up the rest of the Factory. Reads `path`, validates
// version + cluster/kind match, returns the items.
func LoadSnapshotForTest(path, cluster, kind string, maxAge time.Duration) ([]*unstructured.Unstructured, time.Time, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, time.Time{}, err
	}
	if env.Cluster != cluster || env.Kind != kind {
		return nil, time.Time{}, errors.New("snapshot cluster/kind mismatch")
	}
	if maxAge > 0 && time.Since(env.Wrote) > maxAge {
		return nil, env.Wrote, errors.New("snapshot expired")
	}
	return env.Items, env.Wrote, nil
}
