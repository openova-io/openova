package suite

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check FND-G02, Zero touch: nobody changed a tracked object by hand.
//
// The tracked objects of a region are every HelmRelease of every namespace,
// and the Deployments, DaemonSets and StatefulSets of the namespaces in
// zeroTouchRestartNamespaces. Two rules:
//
//   - no HelmRelease, and no Deployment or DaemonSet of a namespace in
//     zeroTouchManagerNamespaces, has a field manager named kubectl or
//     kubectl-<anything>;
//   - no tracked Deployment, DaemonSet or StatefulSet has the annotation
//     kubectl.kubernetes.io/restartedAt on its pod template.
//
// There is no exemption by time and none by name. The clusters are looked at
// twice: zeroTouchStart looks before the journeys (the runner calls it), and
// Run looks again when the checks run. Run fails when either look failed.
// The steps of the first look are not seen by the journeys.
//
// kubectl prints field managers only with --show-managed-fields. An object
// that comes back without any field manager was not measured, and fails.
//
// The third clause of the check, that the deployment record shows no retry
// call, is recorded as SKIP: the suite has no source for it yet. A SKIP is
// never a PASS, so this check cannot pass before that source exists.
const zeroTouchID = "FND-G02"

const restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

var (
	zeroTouchRestartNamespaces = []string{"catalyst-system", "cnpg-system", "kube-system", "flux-system"}
	zeroTouchManagerNamespaces = []string{"catalyst-system", "cnpg-system", "kube-system"}
)

type zeroTouchCheck struct{}

func init() { RegisterCheck(zeroTouchCheck{}) }

func (zeroTouchCheck) ID() string { return zeroTouchID }

func (zeroTouchCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	next := func() string { n++; return zeroTouchID + "." + strconv.Itoa(n) }
	for _, region := range cc.Regions {
		rec.Step(next(), "at the start of the run no tracked object of region "+region.ID+" was changed by hand", func(ev Evidence) error {
			ev["region"] = region.ID
			for _, s := range cc.Prior {
				if s.Journey != zeroTouchID || s.Evidence["region"] != region.ID {
					continue
				}
				ev["start_step"], ev["inspected"], ev["offenders"] = s.ID, s.Evidence["inspected"], s.Evidence["offenders"]
				if s.Status != Pass {
					return errors.New(s.Error)
				}
				return nil
			}
			return fmt.Errorf("no look was taken at region %s at the start of the run", region.ID)
		})
		rec.Step(next(), "at the end of the run no tracked object of region "+region.ID+" was changed by hand", func(ev Evidence) error {
			return zeroTouchLook(ctx, region, ev)
		})
	}
	rec.Skip(next(), "the deployment record shows no retry call",
		"not measured: the suite has no source for the retry calls of the deployment")
	return nil
}

// zeroTouchStart takes the first look, before the journeys. It returns one
// step per region under the identifier of the check. The runner adds these
// steps to the record after the journeys, and Run finds them in
// CheckContext.Prior. It returns nothing when the check is not selected.
func zeroTouchStart(ctx context.Context, r *runner, checks []Check) []*Step {
	if !slices.ContainsFunc(checks, func(c Check) bool { return c.ID() == zeroTouchID }) {
		return nil
	}
	look := &runner{out: r.out, run: &Run{}}
	for i, region := range []kube.Region{r.A, r.B} {
		look.step(zeroTouchID, zeroTouchID+".start."+strconv.Itoa(i+1),
			"first look, before the journeys: no tracked object of region "+region.ID+" was changed by hand",
			func(ev Evidence) error { return zeroTouchLook(ctx, region, ev) })
	}
	return look.run.Steps
}

// zeroTouchLook inspects the tracked objects of one region once.
func zeroTouchLook(ctx context.Context, region kube.Region, ev Evidence) error {
	ev["region"] = region.ID
	inspected, offenders, unmeasured := 0, []string{}, []string{}
	judge := func(o kube.Object, managers, restart bool) {
		inspected++
		name := o.Str("kind") + " " + o.Namespace() + "/" + o.Name()
		if managers {
			fields := o.Objects("metadata", "managedFields")
			if len(fields) == 0 {
				unmeasured = append(unmeasured, name)
			}
			for _, f := range fields {
				if m := f.Str("manager"); m == "kubectl" || strings.HasPrefix(m, "kubectl-") {
					offenders = append(offenders, name+": field manager "+m+" ("+f.Str("operation")+")")
				}
			}
		}
		if at := o.Str("spec", "template", "metadata", "annotations", restartedAtAnnotation); restart && at != "" {
			offenders = append(offenders, name+": annotation "+restartedAtAnnotation+"="+at)
		}
	}
	releases, err := region.GetList(ctx, hrResource, "", "--show-managed-fields")
	if err != nil {
		return err
	}
	for _, hr := range releases {
		judge(hr, true, false)
	}
	for _, ns := range zeroTouchRestartNamespaces {
		workloads, err := region.GetList(ctx, "deployments,daemonsets,statefulsets", ns, "--show-managed-fields")
		if err != nil {
			return err
		}
		for _, w := range workloads {
			judge(w, slices.Contains(zeroTouchManagerNamespaces, ns) && w.Str("kind") != "StatefulSet", true)
		}
	}
	sort.Strings(offenders)
	sort.Strings(unmeasured)
	ev["inspected"], ev["offenders"] = inspected, offenders
	switch {
	case inspected == 0:
		return errors.New("no tracked object was found: nothing was measured")
	case len(unmeasured) > 0:
		return fmt.Errorf("%d objects came back without field managers, so they were not measured: %s", len(unmeasured), strings.Join(unmeasured, "; "))
	case len(offenders) > 0:
		return fmt.Errorf("%d findings: %s", len(offenders), strings.Join(offenders, "; "))
	}
	return nil
}
