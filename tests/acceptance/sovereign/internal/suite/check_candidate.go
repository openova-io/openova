package suite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check FND-G01, Candidate identity: what runs is what the candidate names.
//
//   - The suite was built from the candidate commit.
//   - Every HelmRelease of every region runs the chart version that the
//     candidate pins in clusters/_template/bootstrap-kit/*.yaml. A HelmRelease
//     that the candidate does not pin is listed and not compared. So is a
//     HelmRelease with spec.suspend true.
//   - Every container of the platform namespaces has an image digest, and one
//     image reference has one digest, in each region and across the regions.
//
// The check also observes the artifacts of the record: one entry per
// HelmRelease per region and one per image reference and digest per region.
// It hands them to the record only when every region was read; a record
// without artifacts is never half of a valid pair.
const candidateID = "FND-G01"

// pinsDir is where the candidate pins the chart versions, under RepoRoot.
const pinsDir = "clusters/_template/bootstrap-kit"

type candidateCheck struct{}

func init() { RegisterCheck(candidateCheck{}) }

func (candidateCheck) ID() string { return candidateID }

// chartPin is the chart and the version that one HelmRelease of the
// candidate names.
type chartPin struct{ Chart, Version string }

func (candidateCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	next := func() string { n++; return candidateID + "." + strconv.Itoa(n) }

	rec.Step(next(), "the suite was built from the candidate commit", func(ev Evidence) error {
		built := gitSHA()
		ev["suite_git_sha"], ev["candidate_commit"] = built, cc.Candidate.Commit
		switch {
		case cc.Candidate.Commit == "":
			return errors.New("the run names no candidate commit (--repo-root is not a checkout)")
		case built != cc.Candidate.Commit:
			return fmt.Errorf("the suite was built from %s and the candidate commit is %s", nonEmpty(built, "an unknown commit"), cc.Candidate.Commit)
		}
		return nil
	})

	var pins map[string]chartPin
	rec.Step(next(), "the candidate pins a chart version for each HelmRelease of "+pinsDir, func(ev Evidence) error {
		var err error
		if pins, err = readPins(cc.RepoRoot); err != nil {
			return err
		}
		ev["pins"] = len(pins)
		if len(pins) == 0 {
			pins = nil
			return fmt.Errorf("no HelmRelease with a chart version was found in %s under --repo-root", pinsDir)
		}
		return nil
	})

	art := &Artifacts{Charts: []ChartArtifact{}, Images: []ImageArtifact{}}
	complete := len(cc.Regions) > 0
	for _, region := range cc.Regions {
		rec.Step(next(), "every HelmRelease runs its pinned chart version in region "+region.ID, func(ev Evidence) error {
			if pins == nil {
				complete = false
				return errors.New("the pins were not read")
			}
			releases, err := region.GetList(ctx, hrResource, "")
			if err != nil {
				complete = false
				return err
			}
			found, compared, differences := map[string]bool{}, 0, []string{}
			for _, hr := range releases {
				key := hr.Namespace() + "/" + hr.Name()
				found[key] = true
				c := ChartArtifact{Region: region.ID, Namespace: hr.Namespace(), Release: hr.Name(),
					Chart: hr.Str("spec", "chart", "spec", "chart"), Version: deployedChartVersion(hr), Pinned: pins[key].Version}
				art.Charts = append(art.Charts, c)
				if suspend, _ := hr.Get("spec", "suspend").(bool); suspend || c.Pinned == "" {
					continue
				}
				compared++
				if c.Version != c.Pinned {
					differences = append(differences, key+" runs "+nonEmpty(c.Version, "no deployed version")+" and the pin is "+c.Pinned)
				}
			}
			absent := []string{}
			for key := range pins {
				if !found[key] {
					absent = append(absent, key)
				}
			}
			sort.Strings(absent)
			sort.Strings(differences)
			ev["releases"], ev["compared"], ev["differences"], ev["pinned_and_absent"] = len(releases), compared, differences, absent
			if len(releases) == 0 {
				complete = false
				return errors.New("no HelmRelease was found: nothing was measured")
			}
			if len(differences) > 0 {
				return fmt.Errorf("%d of %d HelmReleases do not run their pinned version: %s", len(differences), compared, strings.Join(differences, "; "))
			}
			return nil
		})
		rec.Step(next(), "every container has a digest and one image reference has one digest in region "+region.ID, func(ev Evidence) error {
			pods, err := region.PlatformPods(ctx)
			if err != nil {
				complete = false
				return err
			}
			count, noDigest := map[[2]string]int{}, []string{}
			containers := 0
			for _, pod := range pods {
				digests := map[string]string{}
				for _, s := range containerStatuses(pod) {
					digests[s.Str("name")] = imageDigest(s.Str("imageID"))
				}
				for _, c := range append(pod.Objects("spec", "initContainers"), pod.Objects("spec", "containers")...) {
					containers++
					ref, digest := c.Str("image"), digests[c.Str("name")]
					if digest == "" {
						noDigest = append(noDigest, pod.Namespace()+"/"+pod.Name()+"/"+c.Str("name"))
						continue
					}
					count[[2]string{ref, digest}]++
				}
			}
			for k, v := range count {
				art.Images = append(art.Images, ImageArtifact{Region: region.ID, Ref: k[0], Digest: k[1], Containers: v})
			}
			two := twoDigests(art.Images, region.ID)
			sort.Strings(noDigest)
			ev["pods"], ev["containers"], ev["references"] = len(pods), containers, len(count)
			ev["no_digest"], ev["two_digests"] = noDigest, two
			switch {
			case containers == 0:
				complete = false
				return errors.New("no container was found in the platform namespaces: nothing was measured")
			case len(noDigest) > 0:
				return fmt.Errorf("%d containers have no image digest: %s", len(noDigest), strings.Join(noDigest, "; "))
			case len(two) > 0:
				return fmt.Errorf("%d image references have more than one digest: %s", len(two), strings.Join(two, "; "))
			}
			return nil
		})
	}

	rec.Step(next(), "one image reference has one digest across the regions", func(ev Evidence) error {
		sort.Slice(art.Charts, func(i, j int) bool {
			a, b := art.Charts[i], art.Charts[j]
			return a.Region+"\x00"+a.Namespace+"\x00"+a.Release < b.Region+"\x00"+b.Namespace+"\x00"+b.Release
		})
		sort.Slice(art.Images, func(i, j int) bool {
			a, b := art.Images[i], art.Images[j]
			return a.Region+"\x00"+a.Ref+"\x00"+a.Digest < b.Region+"\x00"+b.Ref+"\x00"+b.Digest
		})
		two := twoDigests(art.Images, "")
		ev["charts"], ev["images"], ev["two_digests"], ev["complete"] = len(art.Charts), len(art.Images), two, complete
		if complete {
			ev[ArtifactsKey] = art
		}
		switch {
		case !complete:
			return errors.New("a region was not read in full: the record gets no artifacts")
		case len(two) > 0:
			return fmt.Errorf("%d image references have more than one digest: %s", len(two), strings.Join(two, "; "))
		}
		return nil
	})
	return nil
}

// deployedChartVersion is the chart version of the newest deployed release
// in the history of a HelmRelease, or "" when nothing is deployed.
func deployedChartVersion(hr kube.Object) string {
	for _, snapshot := range hr.Objects("status", "history") {
		if snapshot.Str("status") == "deployed" {
			return snapshot.Str("chartVersion")
		}
	}
	return ""
}

// imageDigest is the digest in a container's imageID: the text after the
// last "@". An imageID without "@" has no digest.
func imageDigest(imageID string) string {
	i := strings.LastIndex(imageID, "@")
	if i < 0 {
		return ""
	}
	return imageID[i+1:]
}

// twoDigests lists the image references that have more than one digest, in
// one region, or across every region when region is "".
func twoDigests(images []ImageArtifact, region string) []string {
	seen := map[string]map[string]bool{}
	for _, im := range images {
		if region != "" && im.Region != region {
			continue
		}
		if seen[im.Ref] == nil {
			seen[im.Ref] = map[string]bool{}
		}
		seen[im.Ref][im.Digest] = true
	}
	out := []string{}
	for ref, digests := range seen {
		if len(digests) > 1 {
			list := make([]string, 0, len(digests))
			for d := range digests {
				list = append(list, d)
			}
			sort.Strings(list)
			out = append(out, ref+" has "+strings.Join(list, " and "))
		}
	}
	sort.Strings(out)
	return out
}

var pinKey = regexp.MustCompile(`^( *)([A-Za-z0-9_.-]+):(?: +(.*))?$`)

// readPins reads <root>/clusters/_template/bootstrap-kit/*.yaml and returns,
// for each HelmRelease in it, the chart and the version it names, by
// "<namespace>/<name>". The suite has no YAML library, so the files are read
// by indentation: a value counts only at the exact path of its key.
func readPins(root string) (map[string]chartPin, error) {
	if root == "" {
		return nil, errors.New("the run has no --repo-root")
	}
	files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pinsDir), "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	pins := map[string]chartPin{}
	for _, file := range files {
		text, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return nil, err
		}
		for _, doc := range strings.Split("\n"+string(text), "\n---") {
			at := pinValues(doc)
			if at["kind"] != "HelmRelease" {
				continue
			}
			pin := chartPin{Chart: at["spec/chart/spec/chart"], Version: at["spec/chart/spec/version"]}
			if at["metadata/name"] == "" || pin.Version == "" {
				return nil, fmt.Errorf("%s: a HelmRelease without a name or without spec.chart.spec.version", filepath.Base(file))
			}
			pins[at["metadata/namespace"]+"/"+at["metadata/name"]] = pin
		}
	}
	return pins, nil
}

// pinValues returns the scalar values of one YAML document by the path of
// their keys, for example "spec/chart/spec/version". Comment lines and list
// items are passed over.
func pinValues(doc string) map[string]string {
	type level struct {
		indent int
		key    string
	}
	stack, out := []level{}, map[string]string{}
	for _, line := range strings.Split(doc, "\n") {
		m := pinKey.FindStringSubmatch(strings.TrimRight(line, " \r"))
		if m == nil {
			continue
		}
		indent := len(m[1])
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, level{indent, m[2]})
		value := m[3]
		if strings.HasPrefix(value, "#") {
			value = ""
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = value[:i]
		}
		if value = strings.Trim(strings.TrimSpace(value), `"'`); value == "" {
			continue
		}
		keys := make([]string, len(stack))
		for i, l := range stack {
			keys[i] = l.key
		}
		if path := strings.Join(keys, "/"); out[path] == "" {
			out[path] = value
		}
	}
	return out
}
