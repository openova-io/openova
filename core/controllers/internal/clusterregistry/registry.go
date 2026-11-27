// Package clusterregistry reads the cluster identifiers of a Blueprint
// topology (spec.topology.perTopology.<bcp>.placement.clusters[]) and of a
// placement target as a REGION: the letter at the end of the identifier is
// the region's position in the placement (A = the first region, B = the
// second).
//
// An identifier is `<region>` or `<prefix>-<region>`. The prefix (`mgmt`,
// `dmz` or `rtz`) is the name of a retired tier. It carries no meaning: no
// identifier is resolved through it and no release is placed by it. It is
// still accepted because the stored identifiers carry it and the identifier
// pattern of the Blueprint kind (products/catalyst/chart/crds/blueprint.yaml)
// admits only the prefixed form until NET-227 strips both. The bare form is
// accepted already, so the data may lose its prefix without a change here.
//
// ADR-0015 D1: every per-region HelmRelease is committed to its OWN region's
// Git path and applied by that region's own Flux, so an identifier is always
// read in the region that applies it and never selects a kubeconfig.
//
// The package is pure data + pure functions: no K8s client, no I/O.
//
// Refs #3375 (DoD-4) · NET-223.
package clusterregistry

import (
	"fmt"
	"strings"
)

// legacyPrefixes are the retired tier names an identifier may still carry in
// front of its region letter (see the package comment).
var legacyPrefixes = []string{"mgmt", "dmz", "rtz"}

// wirePrefix is the prefix String writes. The identifier pattern of the
// Blueprint kind admits only a prefixed identifier until NET-227, and `rtz-`
// is the prefix every identifier the controller derives has carried, so the
// per-cluster HelmRelease names (`<app>-rtz-a`) stay as they are.
const wirePrefix = "rtz"

// Region is the region letter of a cluster identifier. "A" is the FIRST
// region of a placement, "B" is the SECOND region.
type Region string

const (
	// RegionA — the first region (primary in active-hot-standby rows).
	RegionA Region = "A"
	// RegionB — the second region (standby in active-hot-standby rows).
	RegionB Region = "B"
)

// KnownRegions is the LOCKED region set of a canonical identifier. A larger
// fleet would extend this with C/D… in a future ADR.
func KnownRegions() []Region { return []Region{RegionA, RegionB} }

// IsKnownRegion reports whether r is one of the canonical regions.
func IsKnownRegion(r Region) bool {
	switch r {
	case RegionA, RegionB:
		return true
	}
	return false
}

// ClusterID is a parsed cluster identifier: its region, nothing else.
type ClusterID struct {
	Region Region
}

// String renders the wire form `rtz-<region>` (e.g. "rtz-A"), the form the
// identifier pattern of the Blueprint kind admits today (see wirePrefix).
func (c ClusterID) String() string {
	return fmt.Sprintf("%s-%s", wirePrefix, c.Region)
}

// Parse reads the region of a cluster identifier: `<region>` or
// `<prefix>-<region>` with a retired tier name as the prefix. The region is
// upper-cased on parse, so "rtz-b", "rtz-B" and "b" resolve identically.
//
// Returns an error for an unknown prefix, an unknown region or a malformed
// string — the caller surfaces this as Ready=False reason=Invalid (same shape
// as the topology resolver).
func Parse(id string) (ClusterID, error) {
	region, err := regionPart(id)
	if err != nil {
		return ClusterID{}, err
	}
	r := Region(region)
	if !IsKnownRegion(r) {
		return ClusterID{}, fmt.Errorf("clusterregistry: %q has unknown region %q (want one of %v)", strings.TrimSpace(id), r, KnownRegions())
	}
	return ClusterID{Region: r}, nil
}

// IsCanonical reports whether id parses to a known region.
func IsCanonical(id string) bool {
	_, err := Parse(id)
	return err == nil
}

// Position reads the region letter of id as a zero-based position (`A` = 0,
// `B` = 1, … `Z` = 25) — the position in a region registry that may hold more
// regions than KnownRegions. false when id is not `<letter>` or
// `<prefix>-<letter>`.
func Position(id string) (int, bool) {
	region, err := regionPart(id)
	if err != nil || len(region) != 1 || region[0] < 'A' || region[0] > 'Z' {
		return 0, false
	}
	return int(region[0] - 'A'), true
}

// regionPart returns the upper-cased region part of `<region>` or
// `<prefix>-<region>`.
func regionPart(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("clusterregistry: empty cluster ID")
	}
	region := id
	if i := strings.LastIndex(id, "-"); i >= 0 {
		if i == len(id)-1 || !isLegacyPrefix(id[:i]) {
			return "", fmt.Errorf("clusterregistry: %q is not <region> or <prefix>-<region> (prefix one of %v)", id, legacyPrefixes)
		}
		region = id[i+1:]
	}
	return strings.ToUpper(region), nil
}

func isLegacyPrefix(p string) bool {
	for _, l := range legacyPrefixes {
		if p == l {
			return true
		}
	}
	return false
}
