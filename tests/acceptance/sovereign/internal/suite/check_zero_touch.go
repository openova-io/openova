package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
// call, reads the record on the Sovereign: GET /api/v1/deployments/{id} with
// the console session of the run, field retryCalls. The engine appends one
// entry to that list for every retry call it accepts, on the parent (the list
// travels with the record the hand-over gives the Sovereign) and on the
// Sovereign (products/catalyst/bootstrap/api/internal/retrycall). The clause
// passes only when the record was read and the list holds no entry. It fails,
// naming the entries, when the list holds any; and it fails as not measured
// when the record cannot be read or carries no list (an engine too old to
// record retry calls).
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
	rec.Step(next(), "the deployment record shows no retry call", func(ev Evidence) error {
		return zeroTouchRetryCalls(ctx, cc, ev)
	})
	return nil
}

// zeroTouchRetryCall is one entry of the record's retryCalls list.
type zeroTouchRetryCall struct {
	At     string `json:"at"`
	Route  string `json:"route"`
	Target string `json:"target"`
	By     string `json:"by"`
}

// zeroTouchRetryCalls reads the retry calls of the deployment from the
// Sovereign's own API and fails unless the list was read and is empty.
func zeroTouchRetryCalls(ctx context.Context, cc *CheckContext, ev Evidence) error {
	if cc.API == nil {
		return errors.New("not measured: the run has no console session, so the deployment record cannot be read")
	}
	if cc.Deploy == nil || cc.Deploy.ID == "" {
		return errors.New("not measured: the run names no deployment")
	}
	path := "/api/v1/deployments/" + url.PathEscape(cc.Deploy.ID)
	ev["source"] = "GET " + path + " (retryCalls)"
	var record map[string]json.RawMessage
	if _, err := cc.API.Get(ctx, path, &record); err != nil {
		return fmt.Errorf("not measured: the deployment record could not be read: %w", err)
	}
	raw, ok := record["retryCalls"]
	if !ok || string(raw) == "null" {
		return errors.New("not measured: the deployment record carries no retryCalls list, so its engine does not record retry calls")
	}
	var calls []zeroTouchRetryCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return fmt.Errorf("not measured: retryCalls of the deployment record is not a list of retry calls: %w", err)
	}
	entries := make([]string, 0, len(calls))
	for _, c := range calls {
		entries = append(entries, c.At+" "+c.Route+" "+c.Target+" by "+c.By)
	}
	ev["retry_calls"] = entries
	if len(entries) > 0 {
		return fmt.Errorf("%d retry calls: %s", len(entries), strings.Join(entries, "; "))
	}
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
