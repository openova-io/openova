// application_jobs_projection.go — ADR-0015 D7: every reconciling object
// feeds Jobs through the existing jobs.ActivityBridge.
//
// Two projections, both LEVEL-TRIGGERED off the objects' own status (they run
// on every /jobs read and write only on change — ActivityBridge.
// ProjectObservation):
//
//  1. APPLICATIONS. One leaf per Application CR under the "applications"
//     group, Kind "application". Its status is statusOf(cr) — the CR's own
//     Ready condition — mapped Ready→succeeded, NotReady→failed,
//     Unknown→running; the condition reason + message ride verbatim on the
//     row (Job.Reason / Job.Message). An Application the controller cannot
//     reconcile (sovereign/test: Ready=False EnvironmentMissing
//     'Environment "sovereign-prod" not found') is therefore ONE Jobs row that
//     says "blocked: EnvironmentMissing …" — before this it appeared nowhere
//     on /jobs and as a bare "Pending" on the console.
//
//  2. TOPOLOGY TRANSITIONS. A Continuum or Application CR carrying
//     status.activity[] (the D8 transition engine populates it; shape below)
//     projects as one group per object — "topology-transition-<ns>-<name>" —
//     with one leaf per entry, Kind "topology-transition".
//
//     status.activity[]: [{id, phase, member, state, startedAt, finishedAt, message}]
//     id         stable entry id (leaf slug)
//     phase      transition phase (e.g. traffic-off, data-primary, traffic-on)
//     member     the member the phase acts on (e.g. wordpress-db, region b)
//     state      pending | running | succeeded | failed
//     startedAt  RFC3339, finishedAt RFC3339, message verbatim
//
// Per-region HelmReleases for org apps are projected by
// chrootSeedApplicationInstalls (#6045, widened by D7 to the funnel's
// org-tenant HRs), across every registered region cluster.
package handler

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// activityEntry is one element of status.activity[] (see file comment).
type activityEntry struct {
	ID         string
	Phase      string
	Member     string
	State      string
	StartedAt  string
	FinishedAt string
	Message    string
}

// activityEntriesOf reads status.activity[] verbatim. Entries without an id
// are skipped (they cannot be addressed as a leaf).
func activityEntriesOf(obj *unstructured.Unstructured) []activityEntry {
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "activity")
	out := make([]activityEntry, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		e := activityEntry{}
		e.ID, _ = m["id"].(string)
		e.Phase, _ = m["phase"].(string)
		e.Member, _ = m["member"].(string)
		e.State, _ = m["state"].(string)
		e.StartedAt, _ = m["startedAt"].(string)
		e.FinishedAt, _ = m["finishedAt"].(string)
		e.Message, _ = m["message"].(string)
		if strings.TrimSpace(e.ID) == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// activityResultFromState maps a status.activity[].state onto the
// ActivityBridge result vocabulary.
func activityResultFromState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running":
		return jobs.ActivityResultRunning
	case "succeeded", "success":
		return jobs.ActivityResultSuccess
	case "failed":
		return jobs.ActivityResultFailed
	default:
		return jobs.ActivityResultPending
	}
}

// applicationResultFromStatus maps statusOf onto the bridge result.
func applicationResultFromStatus(st ObjectStatus) string {
	switch st.State {
	case StateReady:
		return jobs.ActivityResultSuccess
	case StateNotReady:
		return jobs.ActivityResultFailed
	default:
		return jobs.ActivityResultRunning
	}
}

// slugFor makes a jobs-safe slug from namespace/name ("<ns>:<name>" keeps the
// same separator ApplicationComponentID uses so TanStack Router never splits
// the id into path segments).
func slugFor(ns, name string) string {
	return ns + ":" + name
}

func parseRFC3339(s string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s)); err == nil {
		return t
	}
	return fallback
}

// projectApplicationObjects projects Application CRs + topology-transition
// activity into store under depID. Pure over its inputs (the lists), so the
// fixture tests drive it without a live client.
func projectApplicationObjects(store *jobs.Store, depID string, apps, continuums []unstructured.Unstructured, now time.Time) error {
	if store == nil || strings.TrimSpace(depID) == "" {
		return nil
	}
	if len(apps) > 0 {
		bridge := jobs.NewActivityBridge(store, depID, jobs.GroupApplications, jobs.GroupApplicationsDisplay)
		steps := make([]jobs.ActivityStep, 0, len(apps))
		for i := range apps {
			a := &apps[i]
			steps = append(steps, jobs.ActivityStep{
				Slug:        slugFor(a.GetNamespace(), a.GetName()),
				DisplayName: "Application " + a.GetNamespace() + "/" + a.GetName(),
				Order:       i + 1,
				DependsOn:   []string{}, // independent objects: every leaf is a root
				Kind:        jobs.KindApplication,
			})
		}
		bridge.RegisterSteps(steps)
		for i := range apps {
			st := statusOf(&apps[i])
			since := parseRFC3339(st.Since, now)
			if err := bridge.ProjectObservation(steps[i], applicationResultFromStatus(st), st.Reason, st.Message, since); err != nil {
				return err
			}
		}
	}
	all := make([]unstructured.Unstructured, 0, len(apps)+len(continuums))
	all = append(all, apps...)
	all = append(all, continuums...)
	for i := range all {
		obj := &all[i]
		entries := activityEntriesOf(obj)
		if len(entries) == 0 {
			continue
		}
		group := jobs.TopologyTransitionGroupPrefix + obj.GetNamespace() + "-" + obj.GetName()
		display := "Topology transition " + obj.GetKind() + " " + obj.GetNamespace() + "/" + obj.GetName()
		bridge := jobs.NewActivityBridge(store, depID, group, display)
		steps := make([]jobs.ActivityStep, 0, len(entries))
		for j, e := range entries {
			name := strings.TrimSpace(e.Phase)
			if m := strings.TrimSpace(e.Member); m != "" {
				name += " · " + m
			}
			deps := []string{}
			if j > 0 {
				deps = []string{entries[j-1].ID}
			}
			steps = append(steps, jobs.ActivityStep{
				Slug:        e.ID,
				DisplayName: name,
				Order:       j + 1,
				DependsOn:   deps, // the declared order of the transition's phases
				Kind:        jobs.KindTopologyTransition,
			})
		}
		bridge.RegisterSteps(steps)
		for j, e := range entries {
			result := activityResultFromState(e.State)
			at := parseRFC3339(e.FinishedAt, parseRFC3339(e.StartedAt, now))
			if result == jobs.ActivityResultRunning {
				at = parseRFC3339(e.StartedAt, now)
			}
			if err := bridge.ProjectObservation(steps[j], result, "", e.Message, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// chrootSeedApplicationObjects lists Application + Continuum CRs on the
// Sovereign and projects them (see file comment). Best-effort: a list
// failure is logged and leaves the rest of /jobs untouched.
func (h *Handler) chrootSeedApplicationObjects(ctx context.Context, dep *Deployment) {
	if h.jobs == nil || dep == nil {
		return
	}
	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil || dyn == nil {
		return
	}
	h.seedApplicationObjectsFrom(ctx, dyn, dep.ID)
}

func (h *Handler) seedApplicationObjectsFrom(ctx context.Context, dyn dynamic.Interface, depID string) {
	var apps, conts []unstructured.Unstructured
	if l, err := dyn.Resource(ApplicationGVR()).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		apps = l.Items
	} else {
		h.log.Warn("jobs seed: Application CR list failed — Application rows not projected (ADR-0015 D7)",
			"depId", depID, "err", err)
	}
	if l, err := dyn.Resource(ContinuumGVR()).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		conts = l.Items
	}
	if err := projectApplicationObjects(h.jobs, depID, apps, conts, time.Now().UTC()); err != nil {
		h.log.Warn("jobs seed: Application/topology-transition projection failed",
			"depId", depID, "err", err)
	}
}
