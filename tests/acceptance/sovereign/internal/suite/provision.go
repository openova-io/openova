package suite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// The object provision of the record: the installation under test. A run in
// the cloud measures it last, in step D0.11, because whether the installation
// is fresh depends on both looks of check FND-G02. Every value is read from
// the clusters; only the requested node counts come from a flag.
//
//   - provider: PROVIDER of the bootstrap variables of the first region (see
//     sovereignVariables). Every other region must report the same value.
//   - created_at: the earliest creation of the namespace kube-system in the
//     regions: the birth of the environment, also after updates in place.
//   - ready_at: when the hand-over chain ended, from the ConfigMap
//     catalyst/self-sovereign-cutover-status of the first region:
//     cutoverFinishedAt when cutoverComplete is "true", else
//     step.<failedStep>.finishedAt.
//   - regions: per region its id, the nodes --nodes-requested names (0 when it
//     names none) and the nodes that are Ready.
//   - fresh: true only when the run names a candidate, the environment was
//     created after that candidate was cut (the time that starts its id), and
//     check FND-G02 passed at the start and at the end in every region. An
//     environment updated in place was created before the candidate existed.
//
// A value that cannot be read fails step D0.11, and with it the run; the object
// is still whole (a time not read is 0001-01-01T00:00:00Z, a provider "").
const provisionStepID = "D0.11"

const kustomizationResource = "kustomizations.kustomize.toolkit.fluxcd.io"

// measureProvision records step D0.11 and writes the installation into the
// object provision of the record, next to the wipe values set before.
// requested is the value of --nodes-requested, nil when it was not given.
func (r *runner) measureProvision(ctx context.Context, requested map[string]int) {
	inst := &Installation{Regions: []ProvisionRegion{}}
	if r.run.Provision == nil {
		r.run.Provision = &Provision{}
	}
	r.run.Provision.Installation = inst
	prior := append([]*Step(nil), r.run.Steps...)
	r.step("D0", provisionStepID, "the installation under test: provider, creation, readiness, nodes, and whether it is fresh", func(ev Evidence) error {
		return measureInstallation(ctx, []kube.Region{r.A, r.B}, requested, r.run.Candidate.ID, prior, inst, ev)
	})
}

// measureInstallation fills inst from the regions, the first region first, and
// returns an error that names every value it could not read.
func measureInstallation(ctx context.Context, regions []kube.Region, requested map[string]int, candidateID string,
	prior []*Step, inst *Installation, ev Evidence) error {
	problems, sources := []string{}, []string{}
	var born time.Time
	bornRead := true
	for i, reg := range regions {
		entry := ProvisionRegion{ID: reg.ID, NodesRequested: requested[reg.ID]}
		vars, source, err := sovereignVariables(ctx, reg)
		switch {
		case err != nil:
			problems = append(problems, "provider: "+err.Error())
		case vars["PROVIDER"] == "":
			problems = append(problems, fmt.Sprintf("provider: the bootstrap variables of region %s (%s) have no PROVIDER", reg.ID, source))
		case i == 0:
			inst.Provider = vars["PROVIDER"]
		case inst.Provider != "" && vars["PROVIDER"] != inst.Provider:
			problems = append(problems, fmt.Sprintf("provider: region %s reports %s and region %s reports %s",
				reg.ID, vars["PROVIDER"], regions[0].ID, inst.Provider))
		}
		sources = append(sources, source)
		t, err := kubeSystemCreated(ctx, reg)
		if err != nil {
			bornRead = false
			problems = append(problems, "created_at: "+err.Error())
		} else if born.IsZero() || t.Before(born) {
			born = t
		}
		if nodes, err := reg.GetList(ctx, "nodes", ""); err != nil {
			problems = append(problems, "nodes_ready: "+err.Error())
		} else {
			for _, n := range nodes {
				if status, _, _, _ := n.Condition("Ready"); status == "True" {
					entry.NodesReady++
				}
			}
		}
		inst.Regions = append(inst.Regions, entry)
	}
	if bornRead {
		inst.CreatedAt = born
	}
	if at, source, err := handoverEnded(ctx, regions[0]); err != nil {
		problems = append(problems, "ready_at: "+err.Error())
	} else {
		inst.ReadyAt, ev["ready_source"] = at, source
	}
	inst.Fresh, ev["fresh_reason"] = freshness(candidateID, inst.CreatedAt, prior, regions)
	ev["variables"] = sources
	if len(problems) > 0 {
		return errors.New("not read: " + strings.Join(problems, "; "))
	}
	return nil
}

// getNamed returns the object of a kind with that name, and whether it exists.
// namespace "" is for a kind without a namespace.
func getNamed(ctx context.Context, reg kube.Region, resource, namespace, name string) (kube.Object, bool, error) {
	list, err := reg.GetList(ctx, resource, namespace, "--field-selector", "metadata.name="+name)
	if err != nil {
		return nil, false, err
	}
	for _, o := range list {
		if o.Name() == name {
			return o, true, nil
		}
	}
	return nil, false, nil
}

// sovereignVariables returns the values that the bootstrap of a region
// substitutes into its Flux Kustomizations, and where they were read. A region
// installed since SOV-130 holds them in the ConfigMap
// flux-system/sovereign-variables; an older region in spec.postBuild.substitute
// of the Kustomization flux-system/bootstrap-kit.
func sovereignVariables(ctx context.Context, reg kube.Region) (map[string]string, string, error) {
	cm, ok, err := getNamed(ctx, reg, "configmaps", "flux-system", "sovereign-variables")
	if err != nil {
		return nil, "", err
	}
	if ok {
		return stringValues(cm.Get("data")), "ConfigMap flux-system/sovereign-variables", nil
	}
	kit, ok, err := getNamed(ctx, reg, kustomizationResource, "flux-system", "bootstrap-kit")
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", fmt.Errorf("[%s] neither the ConfigMap flux-system/sovereign-variables nor the Kustomization flux-system/bootstrap-kit exists", reg.ID)
	}
	return stringValues(kit.Get("spec", "postBuild", "substitute")), "Kustomization flux-system/bootstrap-kit spec.postBuild.substitute", nil
}

// stringValues keeps the string values of a JSON object.
func stringValues(v any) map[string]string {
	out := map[string]string{}
	m, _ := v.(map[string]any)
	for k, x := range m {
		if s, ok := x.(string); ok {
			out[k] = s
		}
	}
	return out
}

// kubeSystemCreated is the creation time of the namespace kube-system.
func kubeSystemCreated(ctx context.Context, reg kube.Region) (time.Time, error) {
	ns, ok, err := getNamed(ctx, reg, "namespaces", "", "kube-system")
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		return time.Time{}, fmt.Errorf("[%s] the namespace kube-system does not exist", reg.ID)
	}
	s := ns.Str("metadata", "creationTimestamp")
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("[%s] the creation time of the namespace kube-system is not a time: %q", reg.ID, s)
	}
	return t.UTC(), nil
}

// handoverEnded is when the hand-over chain of the environment ended, and the
// key it was read from.
func handoverEnded(ctx context.Context, reg kube.Region) (time.Time, string, error) {
	const where = "catalyst/self-sovereign-cutover-status"
	cm, ok, err := getNamed(ctx, reg, "configmaps", "catalyst", "self-sovereign-cutover-status")
	if err != nil {
		return time.Time{}, "", err
	}
	if !ok {
		return time.Time{}, "", fmt.Errorf("[%s] the ConfigMap %s does not exist", reg.ID, where)
	}
	data, key := stringValues(cm.Get("data")), "cutoverFinishedAt"
	switch {
	case data["cutoverComplete"] == "true":
	case data["failedStep"] != "":
		key = "step." + data["failedStep"] + ".finishedAt"
	default:
		return time.Time{}, "", fmt.Errorf("[%s] the hand-over chain has not ended (current step: %q)", reg.ID, data["currentStep"])
	}
	t, err := time.Parse(time.RFC3339, data[key])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("[%s] %s of the ConfigMap %s is not a time: %q", reg.ID, key, where, data[key])
	}
	return t.UTC(), where + " " + key, nil
}

// freshness says whether the record is of a fresh installation, and why.
func freshness(candidateID string, created time.Time, prior []*Step, regions []kube.Region) (bool, string) {
	stamp, _, _ := strings.Cut(candidateID, "-")
	cut, err := time.Parse("20060102T150405Z", stamp)
	switch {
	case candidateID == "":
		return false, "not fresh: the run names no candidate"
	case err != nil:
		return false, "not fresh: the candidate id " + candidateID + " does not start with the time it was cut"
	case created.IsZero():
		return false, "not fresh: the creation time of the environment was not read"
	case !created.After(cut):
		return false, fmt.Sprintf("not fresh: the environment was created at %s, before candidate %s was cut at %s, so it was updated in place",
			created.Format(time.RFC3339), candidateID, cut.Format(time.RFC3339))
	}
	if held, why := zeroTouchHeld(prior, regions); !held {
		return false, "not fresh: " + why
	}
	return true, "fresh: created after the candidate was cut, and check " + zeroTouchID + " passed at the start and at the end in every region"
}

// zeroTouchHeld says whether check FND-G02 passed at the start and at the end
// of the run in every region. Its steps (check_zero_touch.go) are, per region,
// a first look FND-G02.start.<n>, a step that points at it (evidence
// start_step) and a look at the end; the clause it cannot measure is SKIP.
func zeroTouchHeld(prior []*Step, regions []kube.Region) (bool, string) {
	for _, s := range prior {
		if s.Journey == zeroTouchID && s.Status != Pass && s.Status != Skip {
			return false, fmt.Sprintf("check %s failed in step %s: %s", zeroTouchID, s.ID, s.Error)
		}
	}
	for _, reg := range regions {
		start, end := false, false
		for _, s := range prior {
			if s.Journey != zeroTouchID || s.Status != Pass || s.Evidence["region"] != reg.ID {
				continue
			}
			_, pointer := s.Evidence["start_step"]
			start = start || strings.HasPrefix(s.ID, zeroTouchID+".start.")
			end = end || (!pointer && !strings.HasPrefix(s.ID, zeroTouchID+".start."))
		}
		if !start {
			return false, fmt.Sprintf("check %s took no passing look at region %s at the start of the run", zeroTouchID, reg.ID)
		}
		if !end {
			return false, fmt.Sprintf("check %s took no passing look at region %s at the end of the run", zeroTouchID, reg.ID)
		}
	}
	return true, ""
}
