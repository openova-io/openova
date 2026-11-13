package suite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SVC-G06, Blueprint proofs: every HelmRelease of the foundation ring
// has a passing function test, in every region.
//
// The result of one HelmRelease is read from the newest revision in its
// status, status.history[0].testHooks, and is one of:
//
//   - pass: every test hook of that revision has phase Succeeded;
//   - fail: a test hook of that revision has phase Failed;
//   - none: no hook ran. Either spec.test.enable is not true, or the
//     condition TestSuccess is True with the message "no test hooks";
//   - pending: anything else. The release is not settled (its
//     status.observedGeneration is not metadata.generation, or it has a
//     Reconciling condition), it has no revision, or its test has not
//     finished. An empty testHooks while TestSuccess is not True with
//     "no test hooks" is pending, never none.
//
// The Ready condition and status.failures are never read: with
// spec.test.ignoreFailures they do not show the result of the test.
//
// A HelmRelease is of the foundation ring when its label
// catalyst.openova.io/ring is "foundation". When it has no such label, it is
// of the foundation ring when the candidate declares it (by namespace and
// name) in a slot file of clusters/_template/bootstrap-kit whose number is
// one of foundationSlots. The charts of proofExempt have no function test
// yet: their releases are recorded and not judged.
//
// The check hands the result of every HelmRelease whose result is not
// pending to the record (ProofsKey). Finalize writes it, with the Blueprint
// name, onto the entry of artifacts.charts[] of the same release. The check
// only runs kubectl get.
const proofsID = "SVC-G06"

// ringLabel is the label that names the ring of a kit slot HelmRelease.
const ringLabel = "catalyst.openova.io/ring"

// foundationSlots are the numbers of the slot files of the foundation ring.
const foundationSlots = "01 01a 02 02a 03 04 05 05a 06 06a 07 08 09 10 11 12 12a 13 13b 13c 13e 14 15 15a " +
	"16 16a 16b 16c 16d 17 18 19 26 26b 27 27a 28 49 60 62"

// proofExempt are the Blueprints of the foundation ring whose chart has no
// function test yet.
var proofExempt = map[string]bool{
	"bp-cnpg-pair": true, "bp-continuum": true, "bp-reflector": true,
	"bp-sealed-secrets": true, "bp-self-sovereign-cutover": true,
}

// The results of one HelmRelease.
const (
	proofPass    = "pass"
	proofFail    = "fail"
	proofNone    = "none"
	proofPending = "pending"
)

type proofsCheck struct{}

func init() { RegisterCheck(proofsCheck{}) }

func (proofsCheck) ID() string { return proofsID }

func (proofsCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	next := func() string { n++; return proofsID + "." + strconv.Itoa(n) }

	var declared map[string]bool
	rec.Step(next(), "the candidate declares the HelmReleases of the foundation ring in "+pinsDir, func(ev Evidence) error {
		var err error
		if declared, err = proofsFoundation(cc.RepoRoot); err != nil {
			return err
		}
		ev["declared"] = len(declared)
		if len(declared) == 0 {
			declared = nil
			return fmt.Errorf("no HelmRelease was found in a foundation slot file of %s under --repo-root", pinsDir)
		}
		return nil
	})

	for _, region := range cc.Regions {
		rec.Step(next(), "every foundation HelmRelease that is not exempt has a passing function test in region "+region.ID, func(ev Evidence) error {
			if declared == nil {
				return errors.New("the foundation HelmReleases were not read")
			}
			releases, err := region.GetList(ctx, hrResource, "")
			if err != nil {
				return err
			}
			proofs, counts := map[string]ChartProof{}, map[string]int{}
			foundation, judged, notPass, exempt := 0, 0, []string{}, []string{}
			for _, hr := range releases {
				key := hr.Namespace() + "/" + hr.Name()
				blueprint := proofsBlueprint(hr)
				result, why := proofOf(hr)
				counts[result]++
				if result != proofPending {
					proofs[region.ID+"/"+key] = ChartProof{Blueprint: blueprint, FunctionTest: result}
				}
				ring := hr.Str("metadata", "labels", ringLabel)
				if ring != "foundation" && (ring != "" || !declared[key]) {
					continue
				}
				foundation++
				line := key + " (" + nonEmpty(blueprint, "no chart") + ") " + result
				if why != "" {
					line += ": " + why
				}
				if proofExempt[blueprint] {
					exempt = append(exempt, line)
					continue
				}
				judged++
				if result != proofPass {
					notPass = append(notPass, line)
				}
			}
			sort.Strings(notPass)
			sort.Strings(exempt)
			ev["releases"], ev["results"], ev["foundation"] = len(releases), counts, foundation
			ev["judged"], ev["not_pass"], ev["exempt"] = judged, notPass, exempt
			ev[ProofsKey] = proofs
			switch {
			case foundation == 0:
				return errors.New("no HelmRelease of the foundation ring was found: nothing was measured")
			case len(notPass) > 0:
				return fmt.Errorf("%d of %d foundation HelmReleases have no passing function test: %s", len(notPass), judged, strings.Join(notPass, "; "))
			}
			return nil
		})
	}
	return nil
}

// proofOf returns the result of the function test of the newest revision of
// one HelmRelease, and why when the result is not pass.
func proofOf(hr kube.Object) (result, why string) {
	if enabled, _ := hr.Get("spec", "test", "enable").(bool); !enabled {
		return proofNone, "spec.test.enable is not true"
	}
	if gen, seen := hr.Int("metadata", "generation"), hr.Int("status", "observedGeneration"); gen != seen {
		return proofPending, fmt.Sprintf("status.observedGeneration %d is not metadata.generation %d", seen, gen)
	}
	if _, _, _, reconciling := hr.Condition("Reconciling"); reconciling {
		return proofPending, "the release has a Reconciling condition"
	}
	history := hr.Objects("status", "history")
	if len(history) == 0 {
		return proofPending, "status.history holds no revision"
	}
	hooks, _ := history[0].Get("testHooks").(map[string]any)
	if len(hooks) == 0 {
		status, reason, message, found := hr.Condition("TestSuccess")
		if found && status == "True" && message == "no test hooks" {
			return proofNone, "the chart has no test hook"
		}
		if !found {
			return proofPending, "no test hook is recorded and there is no TestSuccess condition"
		}
		return proofPending, "no test hook is recorded and TestSuccess is " + status + " (" + reason + ")"
	}
	names := make([]string, 0, len(hooks))
	for name := range hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	failed, unfinished := []string{}, []string{}
	for _, name := range names {
		hook, _ := hooks[name].(map[string]any)
		switch phase := kube.Object(hook).Str("phase"); phase {
		case "Succeeded":
		case "Failed":
			failed = append(failed, name)
		default:
			unfinished = append(unfinished, name+" has phase "+nonEmpty(phase, "none"))
		}
	}
	switch {
	case len(failed) > 0:
		return proofFail, "test hook " + strings.Join(failed, ", ") + " failed"
	case len(unfinished) > 0:
		return proofPending, "test hook " + strings.Join(unfinished, ", ")
	}
	return proofPass, ""
}

// proofsBlueprint is the Blueprint name of a HelmRelease: the name of its
// chart, from spec.chart.spec.chart, or else from the newest revision.
func proofsBlueprint(hr kube.Object) string {
	if chart := hr.Str("spec", "chart", "spec", "chart"); chart != "" {
		return chart
	}
	if history := hr.Objects("status", "history"); len(history) > 0 {
		return history[0].Str("chartName")
	}
	return ""
}

// proofsFoundation reads <root>/clusters/_template/bootstrap-kit/*.yaml and
// returns "<namespace>/<name>" of every HelmRelease in a slot file whose
// number (the text of the file name before the first "-") is one of
// foundationSlots.
func proofsFoundation(root string) (map[string]bool, error) {
	if root == "" {
		return nil, errors.New("the run has no --repo-root")
	}
	files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pinsDir), "*.yaml"))
	if err != nil {
		return nil, err
	}
	slots := map[string]bool{}
	for _, s := range strings.Fields(foundationSlots) {
		slots[s] = true
	}
	sort.Strings(files)
	out := map[string]bool{}
	for _, file := range files {
		number, _, _ := strings.Cut(filepath.Base(file), "-")
		if !slots[number] {
			continue
		}
		text, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return nil, err
		}
		for _, doc := range strings.Split("\n"+string(text), "\n---") {
			at := pinValues(doc)
			if at["kind"] == "HelmRelease" && at["metadata/name"] != "" {
				out[at["metadata/namespace"]+"/"+at["metadata/name"]] = true
			}
		}
	}
	return out, nil
}
