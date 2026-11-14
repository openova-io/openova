package gitwrite

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// The region scopes of contract C4 (docs/contracts/C4-git-layout-and-commit-library.md,
// section 2.2). Intent that every region applies, and intent for one
// region's infrastructure, lives in the region folders of the fleet
// repository, below clusters/<fqdn>/regions/<region-id>/platform/<module>/.
// Each region's sovereign-region Kustomization applies its own folder.

// regionPlatformScope is the Name of a RegionPlatform scope.
const regionPlatformScope = "region-platform"

// RegionPlatform is the scope of intent in the region folders of the fleet
// repository: robot "catalyst-fleet-bot", and one prefix
// "clusters/<fqdn>/regions/<region-id>/platform/<module>/" for each listed
// region and each listed module, regions and modules each sorted.
//
// Every-region intent lists every region (modules "root", "policy",
// "machine-ops"); one region's infrastructure intent lists module
// "infrastructure" and that one region. An empty list of modules or of
// regions gives no prefix, so the scope refuses every commit. A Sovereign
// FQDN, region or module that is empty, ".", "..", or holds a "/" gives no
// prefix either: a name never widens the scope.
func RegionPlatform(sovereignFQDN string, modules, regionIDs []string) Scope {
	s := Scope{Name: regionPlatformScope, Robot: fleetRobot}
	if !validName(sovereignFQDN) {
		return s
	}
	for _, region := range sortedNames(regionIDs) {
		for _, module := range sortedNames(modules) {
			s.Prefixes = append(s.Prefixes, regionDir(sovereignFQDN, region)+"platform/"+module+"/")
		}
	}
	return s
}

// Regions returns the IDs of the region folders of one Sovereign in the
// fleet repository r: the directories directly under
// "clusters/<fqdn>/regions/", sorted. Files in that folder are not
// regions. A missing folder gives an empty slice. Regions reads with the
// robot of RegionPlatform; an FQDN that is not a valid name returns
// ErrInvalid before any network call.
func Regions(ctx context.Context, w Writer, r Repo, sovereignFQDN string) ([]string, error) {
	if !validName(sovereignFQDN) {
		return nil, fmt.Errorf("%w: %q is not a Sovereign FQDN", ErrInvalid, sovereignFQDN)
	}
	entries, err := w.List(ctx, RegionPlatform(sovereignFQDN, nil, nil), r, "clusters/"+sovereignFQDN+"/regions")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, e := range entries {
		if e.IsDir {
			ids = append(ids, path.Base(e.Path))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// WriteAllRegions commits the files of c into the folder of every region of
// regionIDs, in one commit made by w.Commit.
//
// The path of each file of c is relative to a region folder, for example
// "platform/policy/mode.yaml". It becomes
// "clusters/<fqdn>/regions/<region-id>/<path>" once for each region. The
// regions are sorted, and an ID listed twice counts once. The IfMatch of a
// file applies to its copy in the first region in that order; the copies
// in the other regions carry no IfMatch. So the caller reads the first
// region's file, and the commit fails with *ConflictError when that file
// changed since.
//
// c.Scope must allow every copy; normally it is
// RegionPlatform(fqdn, modules, regionIDs). A copy outside it is refused
// with ErrForbiddenPath, and nothing is committed. Result.Files lists the
// copies region by region in sorted order, and within a region in the
// order of c.Files. No region, or a Sovereign FQDN or region ID that is
// not a valid name, returns ErrInvalid before any network call. c itself
// is not changed.
func WriteAllRegions(ctx context.Context, w Writer, sovereignFQDN string, regionIDs []string, c Change) (Result, error) {
	if !validName(sovereignFQDN) {
		return Result{}, fmt.Errorf("%w: %q is not a Sovereign FQDN", ErrInvalid, sovereignFQDN)
	}
	for _, id := range regionIDs {
		if !validName(id) {
			return Result{}, fmt.Errorf("%w: %q is not a region ID", ErrInvalid, id)
		}
	}
	regions := sortedNames(regionIDs)
	if len(regions) == 0 {
		return Result{}, fmt.Errorf("%w: WriteAllRegions needs at least one region", ErrInvalid)
	}
	files := make([]FileChange, 0, len(regions)*len(c.Files))
	for i, region := range regions {
		for _, f := range c.Files {
			f.Path = regionDir(sovereignFQDN, region) + f.Path
			if i > 0 {
				f.IfMatch = ""
			}
			files = append(files, f)
		}
	}
	all := c
	all.Files = files
	return w.Commit(ctx, all)
}

// regionDir is the folder of one region in the fleet repository, with a
// trailing slash.
func regionDir(sovereignFQDN, region string) string {
	return "clusters/" + sovereignFQDN + "/regions/" + region + "/"
}

// validName reports whether s can be one element of a path: not empty,
// not "." or "..", and without a "/".
func validName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.Contains(s, "/")
}

// sortedNames returns the valid names of names, sorted, each once.
func sortedNames(names []string) []string {
	var out []string
	for _, n := range names {
		if validName(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	var uniq []string
	for _, n := range out {
		if len(uniq) == 0 || uniq[len(uniq)-1] != n {
			uniq = append(uniq, n)
		}
	}
	return uniq
}
