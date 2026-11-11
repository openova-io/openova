// Package regionlocal is the ADR-0015 D6 admission rule for region-local
// file state: a Blueprint whose declared region-local volumes are not all
// replicated (or need no replication) may not be placed in more than one
// region.
//
// A Blueprint declares, in spec.regionLocalVolumes, every piece of file state
// it keeps per region and the mechanism that carries it across a primary
// move:
//
//	regionLocalVolumes:
//	  - name: uploads
//	    replication: s3-replicated     # bp-seaweedfs `replicated[-*]` bucket
//	  - name: wp-content
//	    replication: none-needed       # rebuilt from the image / Git
//
// `replication: none` (or no mechanism at all, or one this package does not
// know) means a primary move leaves the volume behind in the old region —
// WordPress media on a region-local PVC was the case that motivated D6. Such
// a Blueprint is refused for every multi-region placement, with a message
// that names the volume.
//
// Both admission points use this package so they can never disagree:
// catalyst-api's validateInstallRefs (every Application-create door, 422)
// and the application-controller's placement validation (Ready=False for a
// spec edited in Git or with kubectl, which bypasses the API).
package regionlocal

import (
	"fmt"
	"strings"
)

// Replication mechanisms a region-local volume may declare.
const (
	// ReplicationNone — nothing carries the volume to another region.
	ReplicationNone = "none"
	// ReplicationS3Replicated — the data lives in a bp-seaweedfs bucket named
	// `replicated` / `replicated-<suffix>`, synced between regions by the
	// native `weed filer.sync` (ADR-0015 D6a).
	ReplicationS3Replicated = "s3-replicated"
	// ReplicationVolumeReplication — a volume replication operator.
	ReplicationVolumeReplication = "volume-replication"
	// ReplicationNoneNeeded — the volume holds nothing that must survive a
	// move (rebuilt from the image or Git on every start).
	ReplicationNoneNeeded = "none-needed"
)

// Replicated reports whether a declared mechanism carries the volume across
// a primary move. Unknown and empty values are NOT replicated — the gate
// fails closed on a typo.
func Replicated(mechanism string) bool {
	switch strings.ToLower(strings.TrimSpace(mechanism)) {
	case ReplicationS3Replicated, ReplicationVolumeReplication, ReplicationNoneNeeded:
		return true
	}
	return false
}

// Volume is one spec.regionLocalVolumes[] entry.
type Volume struct {
	Name        string
	Replication string
}

// FromBlueprint reads spec.regionLocalVolumes from an unstructured Blueprint
// object (the map under unstructured.Unstructured.Object). Entries that are
// not objects are reported with an empty name so they still fail closed.
func FromBlueprint(obj map[string]any) []Volume {
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return nil
	}
	raw, _ := spec["regionLocalVolumes"].([]any)
	out := make([]Volume, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		name, _ := m["name"].(string)
		repl, _ := m["replication"].(string)
		out = append(out, Volume{Name: strings.TrimSpace(name), Replication: strings.TrimSpace(repl)})
	}
	return out
}

// Unreplicated returns the declared volumes a primary move would strand.
func Unreplicated(vols []Volume) []Volume {
	var out []Volume
	for _, v := range vols {
		if !Replicated(v.Replication) {
			out = append(out, v)
		}
	}
	return out
}

// MultiRegion reports whether a placement spans more than one region: any
// mode other than singleton, or more than one region named.
func MultiRegion(mode string, regionCount int) bool {
	if regionCount > 1 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "singleton", "single-region", "single_region":
		return false
	}
	return true
}

// Refusal is the D6 verdict for one Blueprint and placement: nil when the
// placement is admitted, otherwise an error naming every stranded volume.
func Refusal(blueprint string, bpObj map[string]any, mode string, regionCount int) error {
	if !MultiRegion(mode, regionCount) {
		return nil
	}
	bad := Unreplicated(FromBlueprint(bpObj))
	if len(bad) == 0 {
		return nil
	}
	parts := make([]string, 0, len(bad))
	for _, v := range bad {
		name := v.Name
		if name == "" {
			name = "<unnamed>"
		}
		repl := v.Replication
		if repl == "" {
			repl = "<unset>"
		}
		parts = append(parts, fmt.Sprintf("%q (replication: %s)", name, repl))
	}
	m := strings.TrimSpace(mode)
	if m == "" {
		m = fmt.Sprintf("%d regions", regionCount)
	}
	return fmt.Errorf(
		"Blueprint %q cannot be placed in more than one region (placement %s): region-local volume %s is not replicated, so a primary move would leave its data in the old region (ADR-0015 D6). Declare a replication mechanism (%s, %s or %s) in spec.regionLocalVolumes, or place it in a single region",
		blueprint, m, strings.Join(parts, ", "),
		ReplicationS3Replicated, ReplicationVolumeReplication, ReplicationNoneNeeded)
}
