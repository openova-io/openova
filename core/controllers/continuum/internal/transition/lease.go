package transition

// The cross-region lease is a FILE IN GIT, compare-and-swapped through the
// writer Gitea (gitea.PutFileIfMatch). Why Git and not a Kubernetes Lease:
//
//   - A coordination.k8s.io Lease lives in ONE cluster's etcd. Regions do not
//     share an API server (ADR-0015 D1 deleted the cross-region kubeconfig
//     fan-out), so a Lease in region A is invisible to region B — it cannot
//     fence the two against each other, and it vanishes with region A.
//   - Every phase of a transition is already a commit to the same repo. The
//     repo's writer Gitea is the one store every region already reaches
//     (gitea-<writer-id> over ClusterMesh) and the only one whose ordering
//     decides what Flux applies. Putting the lease next to the commits it
//     guards adds no new dependency and makes the fence exact: a holder
//     whose lease write loses the compare-and-swap commits nothing.
//   - The lease file also carries the transition RECORD (from, to, tokens,
//     per-phase activity), so whichever region holds the lease next resumes
//     the transition from Git, and every region mirrors the same activity
//     into its own Continuum status.
//
// The lease is held only while a transition runs; an idle group writes
// nothing (no heartbeat commits). Fencing between a holder that lost the
// lease and the new holder: both evaluate the same record against the same
// Git files, so the commits they could make are identical desired-state
// writes, each itself compare-and-swapped on the region file's SHA.

import (
	"fmt"
	"time"

	"sigs.k8s.io/yaml"
)

// LeaseFile is the content of continuum/transitions/<group>.yaml.
type LeaseFile struct {
	Group string `json:"group"`
	// Holder is the region id of the acting Continuum ("" = released).
	Holder string `json:"holder"`
	// RenewedAt is when the holder last wrote the file.
	RenewedAt string `json:"renewedAt,omitempty"`
	// TTLSeconds — the lease is expired TTLSeconds after RenewedAt.
	TTLSeconds int `json:"ttlSeconds"`
	// Transition is the current or last transition record.
	Transition *Record `json:"transition,omitempty"`
}

// ParseLease decodes a lease file (empty input = zero lease).
func ParseLease(data []byte) (LeaseFile, error) {
	var l LeaseFile
	if len(data) == 0 {
		return l, nil
	}
	if err := yaml.Unmarshal(data, &l); err != nil {
		return l, fmt.Errorf("lease file: %w", err)
	}
	return l, nil
}

// Marshal encodes the lease file.
func (l LeaseFile) Marshal() ([]byte, error) { return yaml.Marshal(l) }

// Expired reports whether the lease no longer fences anyone at now.
func (l LeaseFile) Expired(now time.Time) bool {
	if l.Holder == "" {
		return true
	}
	t, ok := parseTS(l.RenewedAt)
	if !ok {
		return true
	}
	return now.Sub(t) > time.Duration(l.TTLSeconds)*time.Second
}

// MayAct reports whether region may take or keep the lease at now.
func (l LeaseFile) MayAct(region string, now time.Time) bool {
	return l.Holder == region || l.Expired(now)
}

// ExpiresAt is RenewedAt + TTL ("" when released).
func (l LeaseFile) ExpiresAt() string {
	t, ok := parseTS(l.RenewedAt)
	if !ok || l.Holder == "" {
		return ""
	}
	return ts(t.Add(time.Duration(l.TTLSeconds) * time.Second))
}
