package suite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Check SVC-G01, Catalogue honest: the Blueprint objects of the catalogue
// seed on the Sovereign are exactly the ones the candidate's seed holds, and
// each of them says its class and its lifecycle.
//
// The seed is products/catalyst/chart/templates/catalog-seed/blueprints.yaml
// under --repo-root, which scripts/generate-catalog.py writes from the
// Blueprint files (SVC-160). Its objects are named by the first line
// "  name: bp-<name>" after each line "kind: Blueprint". A seed that names no
// object fails: nothing was measured.
//
// The chart bp-catalyst-platform renders the seed, and it runs in the primary
// region only (a follower region suspends it), so only the primary region is
// judged. Its seeded objects are the Blueprint objects with the label
// catalyst.openova.io/managed-by=catalog-seed. The label is read from each
// object, not passed to kubectl as a selector. A seeded object the seed does
// not name, and a name of the seed without an object, are each a failure.
//
// Every seeded object must carry a non-empty spec.class and spec.lifecycle,
// the class and lifecycle contract that SVC-170 delivers. Until it does, the
// third step fails and the check is expected-fail. The check only runs
// kubectl get.
const catalogueID = "SVC-G01"

// catalogueSeed is the seed file of the candidate, relative to --repo-root.
const catalogueSeed = "products/catalyst/chart/templates/catalog-seed/blueprints.yaml"

// catalogueResource is the resource of the Blueprint objects.
const catalogueResource = "blueprints.catalyst.openova.io"

// catalogueLabel and catalogueLabelValue mark an object of the seed.
const (
	catalogueLabel      = "catalyst.openova.io/managed-by"
	catalogueLabelValue = "catalog-seed"
)

var (
	catalogueKind = regexp.MustCompile(`^kind:\s*Blueprint\s*$`)
	catalogueName = regexp.MustCompile(`^  name:\s*"?(bp-[A-Za-z0-9._-]+)"?\s*$`)
)

type catalogueCheck struct{}

func init() { RegisterCheck(catalogueCheck{}) }

func (catalogueCheck) ID() string { return catalogueID }

// catalogueNames returns the sorted names of the objects of the seed file.
func catalogueNames(repoRoot string) ([]string, error) {
	b, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot, catalogueSeed)))
	if err != nil {
		return nil, err
	}
	seen, names, open := map[string]bool{}, []string{}, false
	for _, line := range strings.Split(string(b), "\n") {
		if catalogueKind.MatchString(line) {
			open = true
			continue
		}
		if m := catalogueName.FindStringSubmatch(line); open && m != nil {
			open = false
			if !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

func (catalogueCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	primary := cc.Regions[0]
	var names []string
	rec.Step(catalogueID+".1", "the candidate's seed "+catalogueSeed+" names its Blueprint objects", func(ev Evidence) error {
		var err error
		if names, err = catalogueNames(cc.RepoRoot); err != nil {
			return fmt.Errorf("the seed of the candidate cannot be read under --repo-root: %v", err)
		}
		ev["seeded"] = len(names)
		if len(names) == 0 {
			return errors.New("the seed of the candidate names no Blueprint object: nothing was measured")
		}
		return nil
	})

	var seeded []string
	byName := map[string]string{}
	rec.Step(catalogueID+".2", "the seeded Blueprint objects of region "+primary.ID+" are the ones the seed names", func(ev Evidence) error {
		if len(names) == 0 {
			return errors.New("the seed of the candidate was not read")
		}
		objects, err := primary.GetList(ctx, catalogueResource, "")
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		extra, missing := []string{}, []string{}
		for _, o := range objects {
			if o.Str("metadata", "labels", catalogueLabel) != catalogueLabelValue {
				continue
			}
			seeded = append(seeded, o.Name())
			byName[o.Name()] = o.Str("spec", "class") + "\x00" + o.Str("spec", "lifecycle")
			if !want[o.Name()] {
				extra = append(extra, o.Name())
			}
		}
		for _, n := range names {
			if _, ok := byName[n]; !ok {
				missing = append(missing, n)
			}
		}
		sort.Strings(seeded)
		sort.Strings(extra)
		ev["objects"], ev["seeded"], ev["missing"], ev["extra"] = len(objects), len(seeded), missing, extra
		switch {
		case len(missing) > 0 && len(extra) > 0:
			return fmt.Errorf("region %s lacks %d seeded objects (%s) and holds %d the seed does not name (%s)",
				primary.ID, len(missing), strings.Join(missing, ", "), len(extra), strings.Join(extra, ", "))
		case len(missing) > 0:
			return fmt.Errorf("region %s lacks %d seeded objects: %s", primary.ID, len(missing), strings.Join(missing, ", "))
		case len(extra) > 0:
			return fmt.Errorf("region %s holds %d seeded objects the seed does not name: %s", primary.ID, len(extra), strings.Join(extra, ", "))
		}
		return nil
	})

	rec.Step(catalogueID+".3", "every seeded Blueprint object of region "+primary.ID+" says its class and lifecycle", func(ev Evidence) error {
		if len(seeded) == 0 {
			return fmt.Errorf("no seeded Blueprint object of region %s was read", primary.ID)
		}
		without := []string{}
		for _, n := range seeded {
			v := strings.SplitN(byName[n], "\x00", 2)
			if v[0] == "" || v[1] == "" {
				without = append(without, n)
			}
		}
		ev["without"] = without
		if len(without) > 0 {
			return fmt.Errorf("%d of %d seeded Blueprint objects of region %s carry no spec.class or no spec.lifecycle: %s",
				len(without), len(seeded), primary.ID, strings.Join(without, ", "))
		}
		return nil
	})
	return nil
}
