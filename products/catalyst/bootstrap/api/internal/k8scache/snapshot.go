// snapshot.go — disk-snapshot the in-process Indexer to mitigate
// cold-start latency.
//
// The catalyst-api Pod can restart 6+ times in a 30-minute window
// (image rolls, OOM, node maintenance). Each cold start would
// otherwise re-LIST every (cluster × kind) which on a tier-1
// Sovereign with thousands of objects can take 30s+. The snapshot
// path closes that gap to <1s.
//
// Per ADR-0001 §5.1 cold-start budget: 1–30s without snapshot;
// <1s with snapshot.
//
// Persistence shape: one JSON file per (cluster, kind) at
//
//	<SnapshotDir>/<cluster>-<kind>.json
//
// Atomicity: temp-file (.tmp suffix) + os.Rename. ext4/xfs guarantee
// rename is atomic on the same filesystem, so a half-written snapshot
// is impossible — either the consumer reads the old snapshot or the
// fully-written new one.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene): Secret
// .data + .stringData are stripped via redactObject BEFORE the
// snapshot file is written. ConfigMap .data is also stripped. The
// snapshot is operator-readable on the PVC; it must not contain a
// credential.
package k8scache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// snapshotEnvelope wraps the per-(cluster, kind) Indexer dump on
// disk. The version field is read by the hydrate path; an
// incompatible version yields a "missing" hydrate result and a full
// LIST.
type snapshotEnvelope struct {
	Version int                            `json:"version"`
	Cluster string                         `json:"cluster"`
	Kind    string                         `json:"kind"`
	Wrote   time.Time                      `json:"wrote"`
	// ResourceVersion is the maximum metadata.resourceVersion observed
	// in the Indexer at write time. Hydrate uses it as the watch's
	// resume point so the apiserver delivers only the diff since the
	// snapshot — consistent with the informer contract.
	ResourceVersion string                       `json:"resourceVersion"`
	Items           []*unstructured.Unstructured `json:"items"`
}

// snapshotEnvelopeVersion — bump when the on-disk shape changes in a
// non-backward-compatible way.
const snapshotEnvelopeVersion = 1

// runSnapshotLoop fires every Config.SnapshotInterval and walks every
// (cluster × kind), serialising the Indexer to disk via temp+rename.
//
// The loop also implements TTL: snapshots older than
// Config.SnapshotMaxAge are deleted on each pass to bound disk
// usage. A typical Sovereign produces a 1–10MB snapshot per kind so
// the 5Gi PVC has plenty of headroom.
func (f *Factory) runSnapshotLoop(ctx context.Context) {
	t := time.NewTicker(f.cfg.SnapshotInterval)
	defer t.Stop()

	// Single eager pass on start so the first snapshot lands within
	// SnapshotInterval rather than after — closes the case where the
	// catalyst-api crashes 30s after start before any snapshot was
	// written.
	f.snapshotOnce(ctx)
	f.purgeStaleSnapshots()

	for {
		select {
		case <-ctx.Done():
			return
		case <-f.stop:
			return
		case <-t.C:
			f.snapshotOnce(ctx)
			f.purgeStaleSnapshots()
		}
	}
}

// snapshotOnce — single pass; serialise every (cluster, kind) Indexer.
func (f *Factory) snapshotOnce(_ context.Context) {
	if f.cfg.SnapshotDir == "" {
		return
	}
	f.mu.RLock()
	clusters := make([]*clusterState, 0, len(f.clusters))
	for _, cs := range f.clusters {
		clusters = append(clusters, cs)
	}
	f.mu.RUnlock()
	for _, cs := range clusters {
		for kindName, idx := range cs.indexers {
			items := idx.List()
			us := make([]*unstructured.Unstructured, 0, len(items))
			maxRV := ""
			for _, it := range items {
				u, ok := it.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				k := mustKind(f.registry, kindName)
				us = append(us, redactObject(k, u))
				if rv := u.GetResourceVersion(); rv > maxRV {
					maxRV = rv
				}
			}
			env := snapshotEnvelope{
				Version:         snapshotEnvelopeVersion,
				Cluster:         cs.id,
				Kind:            kindName,
				Wrote:           time.Now(),
				ResourceVersion: maxRV,
				Items:           us,
			}
			path := snapshotPath(f.cfg.SnapshotDir, cs.id, kindName)
			if err := writeSnapshotAtomic(path, env); err != nil {
				f.log.Warn("k8scache: snapshot write failed",
					"cluster", cs.id, "kind", kindName, "path", path, "err", err)
				continue
			}
			metricSnapshotWrites.WithLabelValues(cs.id, kindName).Inc()
		}
	}
}

// writeSnapshotAtomic writes the envelope to <path>.tmp and renames
// to <path>. The temp file is created with mode 0600 — the snapshot
// MAY contain redacted-but-still-internal cluster state (e.g. node
// IPs); operator-readable, not world-readable.
func writeSnapshotAtomic(path string, env snapshotEnvelope) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// O_CREATE|O_TRUNC|O_WRONLY at 0o600 — the snapshot file is
	// readable by the catalyst-api UID only.
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// purgeStaleSnapshots — drop any *.json older than SnapshotMaxAge.
// The hydrate path already rejects stale snapshots, but cleaning them
// up on the snapshot loop keeps the PVC bounded.
func (f *Factory) purgeStaleSnapshots() {
	if f.cfg.SnapshotDir == "" {
		return
	}
	entries, err := os.ReadDir(f.cfg.SnapshotDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-f.cfg.SnapshotMaxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") &&
			!strings.HasSuffix(e.Name(), ".json.tmp") {
			continue
		}
		full := filepath.Join(f.cfg.SnapshotDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(full); err == nil {
				f.log.Info("k8scache: purged stale snapshot",
					"path", full, "age", time.Since(info.ModTime()).String())
			}
		}
	}
}

// snapshotPath — derive on-disk path for a (cluster, kind) snapshot.
// Cluster id and kind are sanitised so a malicious id can't escape
// the snapshot directory.
func snapshotPath(dir, cluster, kind string) string {
	return filepath.Join(dir, sanitiseSegment(cluster)+"-"+sanitiseSegment(kind)+".json")
}

// sanitiseSegment strips path separators and other unsafe runes from
// a single path component. Non-alphanumeric/-./_ characters become
// '_' so the snapshot file stays inside the SnapshotDir even when an
// operator names a Sovereign with whitespace.
func sanitiseSegment(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}
