package crossregion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// The every-region gate (#7902 defect 6): `ready` means every region.
//
// Before it, a multi-region deployment became `ready`, and its hand-over
// fired, the moment the PRIMARY region's releases were installed. The
// follower region could not be installed at that moment, by construction:
// what it needs from the parent (the mesh, the database Secrets, the admin
// Secrets, the database-pair switch) was started only AFTER `ready`. On the
// fresh installation of 15 November 2026 the record read `ready`, the
// console said "Sovereign is ready" and the hand-over had fired while the
// follower had releases installing and failed; `secondaryDegraded: true`
// sat in the same record.
//
// Now, when the primary region's watch concludes ready:
//
//  1. the parent's cross-region work starts at once, while the status is
//     still `phase1-watching`;
//  2. the gate waits for that work to land (regions meshed, database pair
//     switched on): the switch renders the shared engines again in both
//     regions, so nothing before it can be the final state;
//  3. it then watches EVERY region that reported in, the primary too, with
//     the Phase-1 watcher itself (a watch on the region's HelmReleases, not
//     a poll), after the region's kit Kustomization has applied its current
//     desired state;
//  4. when every watch concludes ready, one direct List of every region,
//     taken together, decides: every release installed and its current
//     generation observed. Only then `ready`, then the hand-over.
//
// When the wait ends without that, the deployment is NOT ready. The caller
// gives it the timeout classification (recoverable: nothing is torn down,
// the cross-region loop keeps running, the watch can be re-attached) with
// an error that names each region and the releases it is short of.
//
// Not covered, on purpose: a DECLARED region that never reported in (no
// kubeconfig). The record says standby-region-absent for it (#4486) and the
// primary is handed over; there is nothing here to watch.

const (
	// BudgetEnv — how long the gate waits after the primary region is
	// installed. A region's watch may run on while its count of installed
	// releases is still rising (the watcher's own progress guard); nothing
	// runs past twice this value.
	BudgetEnv = "CATALYST_PHASE1_REGION_GATE_BUDGET"
	// DefaultBudget — a follower's slowest release (Keycloak) has a
	// 30-minute install budget and can be in its second attempt when its
	// inputs arrive; the database-pair switch then renders the shared
	// engines again in both regions.
	DefaultBudget = 90 * time.Minute
	// SubstateEveryRegion — the substate of a deployment whose primary
	// region is installed and whose other regions are not yet.
	SubstateEveryRegion = "waiting-for-every-region"
	// kitKustomization is the Flux Kustomization the control-plane boot
	// template applies in every region and the cross-region switch patches.
	kitKustomization = "bootstrap-kit"
	// DefaultRespin is the first pause before the regions are looked at
	// again when a look ended without the wanted state (a closed watch, a
	// List taken in the instant between two controllers, a region that
	// cannot be reached). It doubles up to maxRespin while looks keep
	// ending at once. It is a guard against a busy loop, not the wait
	// itself: the wait is a watch.
	DefaultRespin = 2 * time.Second
	maxRespin     = time.Minute
)

var kustomizationGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}

// Budget resolves the gate's wait: the override (tests), else BudgetEnv,
// else DefaultBudget.
func Budget(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if raw := strings.TrimSpace(os.Getenv(BudgetEnv)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return DefaultBudget
}

// Signal is a one-way flag with a channel: zero value ready to use, Set any
// number of times, Done to wait on it. The cross-region loop sets it at its
// first convergence; the gate waits on it.
type Signal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (s *Signal) channel() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

// Set raises the flag.
func (s *Signal) Set() {
	ch := s.channel()
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// Done returns a channel that is closed once the flag is raised.
func (s *Signal) Done() <-chan struct{} { return s.channel() }

// IsSet reports whether the flag is raised.
func (s *Signal) IsSet() bool {
	select {
	case <-s.channel():
		return true
	default:
		return false
	}
}

// Verdict is what the gate found.
type Verdict struct {
	// Converged — the cross-region work landed and one List of every
	// region, taken together, read every release installed and settled.
	Converged bool
	// CrossRegionLanded — the parent's mesh + database-pair work converged.
	CrossRegionLanded bool
	// Pending — region label → what it is short of, e.g. "bp-keycloak
	// (failed)". The primary region is keyed by its own label.
	Pending map[string][]string
	// Unread — region label → why its state could not be read. An unread
	// region is never counted as installed.
	Unread map[string]string
	// States — FOLLOWER region key → component id → helmwatch state, from
	// the last List. The record's per-region census is built from it.
	States map[string]map[string]string
	// Waited — how long the gate ran.
	Waited time.Duration
}

// Summary renders the shortfall as one clause per cause.
func (v *Verdict) Summary() string {
	if v == nil {
		return ""
	}
	parts := []string{}
	if !v.CrossRegionLanded {
		parts = append(parts, "the regions are not meshed yet or the cross-region database pair is not switched on")
	}
	regions := make([]string, 0, len(v.Pending)+len(v.Unread))
	for r := range v.Pending {
		regions = append(regions, r)
	}
	for r := range v.Unread {
		if _, ok := v.Pending[r]; !ok {
			regions = append(regions, r)
		}
	}
	sort.Strings(regions)
	for _, r := range regions {
		if why, ok := v.Unread[r]; ok {
			parts = append(parts, fmt.Sprintf("region %s could not be read (%s)", r, why))
			continue
		}
		short := v.Pending[r]
		if len(short) == 0 {
			continue
		}
		shown, more := short, ""
		if len(shown) > 8 {
			more = fmt.Sprintf(" and %d more", len(shown)-8)
			shown = shown[:8]
		}
		parts = append(parts, fmt.Sprintf("region %s has %d not installed: %s%s", r, len(short), strings.Join(shown, ", "), more))
	}
	return strings.Join(parts, "; ")
}

func (v *Verdict) short() int {
	n := len(v.Unread)
	for _, p := range v.Pending {
		n += len(p)
	}
	if !v.CrossRegionLanded {
		n++
	}
	return n
}

// OverlayStates puts the verdict's follower states over the states read
// from watchers. The verdict's are from a direct List at the gate's end; a
// follower watcher that concluded early is no longer attached at all, and
// its region would drop out of the record's census.
func (v *Verdict) OverlayStates(fromWatchers map[string]map[string]string) map[string]map[string]string {
	if v == nil || len(v.States) == 0 {
		return fromWatchers
	}
	out := make(map[string]map[string]string, len(fromWatchers)+len(v.States))
	for region, states := range fromWatchers {
		out[region] = states
	}
	for region, states := range v.States {
		out[region] = states
	}
	return out
}

// FollowerKubeconfigs returns region key → kubeconfig path of every
// follower region that reported in: the recorded paths, and the
// conventional `<dir>/<id>-<region>.yaml` files (the recorded map is in
// memory only and is empty after a restart). A path whose file is gone is
// left out.
func FollowerKubeconfigs(dir, deploymentID string, recorded map[string]string) map[string]string {
	out := map[string]string{}
	for region, path := range recorded {
		if region != "" && path != "" {
			out[region] = path
		}
	}
	if dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			prefix := deploymentID + "-"
			for _, e := range entries {
				n := e.Name()
				if !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".yaml") {
					continue
				}
				region := strings.TrimSuffix(strings.TrimPrefix(n, prefix), ".yaml")
				if region == "" {
					continue
				}
				if _, ok := out[region]; !ok {
					out[region] = filepath.Join(dir, n)
				}
			}
		}
	}
	for region, path := range out {
		if _, err := os.Stat(path); err != nil {
			delete(out, region)
		}
	}
	return out
}

// Census is one region's state from one direct List.
type Census struct {
	Snapshots []helmwatch.ComponentSnapshot
	// Pending — what keeps the region from "settled with every release
	// installed", sorted.
	Pending []string
}

// ReadCensus lists one region and reports what keeps it from "settled with
// every release installed":
//
//   - the kit Kustomization has not applied its current desired state: its
//     generation is not observed, or a reconcile was requested (the
//     database-pair switch stamps the request) and is not handled yet;
//   - a Blueprint release that is not suspended and is not installed, or
//     whose current generation helm-controller has not observed.
//
// A release that can never install on this provider (the provisioner's
// explicit list) is left out, as in the record's own census.
func ReadCensus(ctx context.Context, dyn dynamic.Interface, provider string, callTimeout time.Duration) (Census, error) {
	var out Census
	getCtx, cancelGet := context.WithTimeout(ctx, callTimeout)
	ks, err := dyn.Resource(kustomizationGVR).Namespace(helmwatch.FluxNamespace).Get(getCtx, kitKustomization, metav1.GetOptions{})
	cancelGet()
	switch {
	case err == nil:
		if why := KustomizationUnsettled(ks); why != "" {
			out.Pending = append(out.Pending, fmt.Sprintf("kustomization/%s (%s)", kitKustomization, why))
		}
	case apierrors.IsNotFound(err):
		// No kit Kustomization to wait for; the releases below decide.
	default:
		return out, fmt.Errorf("get Kustomization %s/%s: %w", helmwatch.FluxNamespace, kitKustomization, err)
	}

	listCtx, cancelList := context.WithTimeout(ctx, callTimeout)
	list, err := dyn.Resource(helmwatch.HelmReleaseGVR).Namespace(helmwatch.FluxNamespace).List(listCtx, metav1.ListOptions{})
	cancelList()
	if err != nil {
		return out, fmt.Errorf("list HelmReleases in %s: %w", helmwatch.FluxNamespace, err)
	}
	for i := range list.Items {
		u := &list.Items[i]
		name := u.GetName()
		if !helmwatch.IsBlueprintRelease(name) {
			continue
		}
		snap := helmwatch.SnapshotHelmRelease(u)
		out.Snapshots = append(out.Snapshots, snap)
		if snap.Suspended || !provisioner.ComponentApplicable(provider, snap.AppID) {
			continue
		}
		switch {
		case snap.Status != helmwatch.StateInstalled:
			out.Pending = append(out.Pending, fmt.Sprintf("%s (%s)", name, snap.Status))
		case !helmwatch.ObservedCurrentGeneration(u):
			out.Pending = append(out.Pending, fmt.Sprintf("%s (new desired state not applied yet)", name))
		}
	}
	sort.Strings(out.Pending)
	return out, nil
}

// KustomizationUnsettled returns why a Flux Kustomization has not applied
// its current desired state, or "".
func KustomizationUnsettled(ks *unstructured.Unstructured) string {
	if suspended, ok, _ := unstructured.NestedBool(ks.Object, "spec", "suspend"); ok && suspended {
		return ""
	}
	if !helmwatch.ObservedCurrentGeneration(ks) {
		return "new desired state not applied yet"
	}
	if requested := ks.GetAnnotations()[helmwatch.AnnotationReconcileRequestedAt]; requested != "" {
		handled, _, _ := unstructured.NestedString(ks.Object, "status", "lastHandledReconcileAt")
		if handled != requested {
			return "requested reconcile not handled yet"
		}
	}
	return ""
}

// Gate is one run of the every-region gate.
type Gate struct {
	// PrimaryLabel is the record's name of the primary region; PrimaryPath
	// its kubeconfig file ("" when unknown: the primary is then judged by
	// its concluded watch alone).
	PrimaryLabel string
	PrimaryPath  string
	// Followers — region key → kubeconfig file (FollowerKubeconfigs).
	Followers map[string]string
	Provider  string
	// Budget — see BudgetEnv.
	Budget time.Duration
	// Landed is closed when the cross-region work has converged.
	Landed <-chan struct{}
	// Watching reports whether the deployment is still in its Phase-1
	// watch; the gate stops at its next step when it is not (a wipe).
	Watching func() bool
	// Dynamic builds the dynamic client of a kubeconfig file.
	Dynamic func(kubeconfigPath string) (dynamic.Interface, error)
	// WatchConfig builds the Phase-1 watcher configuration for a region's
	// kubeconfig (the file's contents). The gate clears the ready sentinel
	// for a follower (the console backend runs in the primary only) and
	// sets the time budget.
	WatchConfig func(kubeconfigYAML string) helmwatch.Config
	// Emit publishes one progress line to the deployment's event stream.
	Emit func(level, message string)
	// OnCensus receives every List the gate takes of a region ("" for the
	// primary), so the caller can write its job rows from the state the
	// gate judges from.
	OnCensus func(regionKey string, snapshots []helmwatch.ComponentSnapshot)
	// CallTimeout bounds each API call outside the watches.
	CallTimeout time.Duration
	// Respin overrides DefaultRespin (tests).
	Respin time.Duration
}

type gateRegion struct {
	label string
	key   string // "" for the primary
	path  string
}

func (g Gate) emit(level, message string) {
	if g.Emit != nil {
		g.Emit(level, message)
	}
}

func (g Gate) watching() bool { return g.Watching == nil || g.Watching() }

// Await runs the gate and returns its verdict. It returns when every region
// is installed, when the budget is used up, or when the deployment left its
// Phase-1 watch.
func (g Gate) Await(ctx context.Context) *Verdict {
	started := time.Now()
	regions := []gateRegion{}
	if g.PrimaryPath != "" {
		label := g.PrimaryLabel
		if label == "" {
			label = "primary"
		}
		regions = append(regions, gateRegion{label: label, path: g.PrimaryPath})
	}
	keys := make([]string, 0, len(g.Followers))
	for k := range g.Followers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		regions = append(regions, gateRegion{label: k, key: k, path: g.Followers[k]})
	}

	// Nothing runs past twice the budget, whatever a watch's own progress
	// guard would still allow.
	ctx, cancel := context.WithTimeout(ctx, 2*g.Budget)
	defer cancel()
	soft := time.NewTimer(g.Budget)
	defer soft.Stop()

	g.emit("info", fmt.Sprintf("The primary region is installed. The deployment is ready when every region is: the cross-region work (mesh, database Secrets, admin Secrets, database pair) starts now, then region(s) %s are watched, for up to %s.",
		strings.Join(keys, ", "), g.Budget))

	landed := false
	select {
	case <-g.Landed:
		landed = true
	case <-soft.C:
	case <-ctx.Done():
	}
	if !landed {
		v := g.census(ctx, regions, false)
		v.Waited = time.Since(started)
		return v
	}
	g.emit("info", "The regions are meshed and the cross-region database pair is switched on. Watching every region until its releases are installed.")

	respin := g.respin()
	lastSummary := ""
	for {
		// One watch per region, together. Each returns when its region's
		// releases are all terminal, or its time is up.
		looked := time.Now()
		remaining := g.Budget - time.Since(started)
		if remaining < g.respin() {
			remaining = g.respin()
		}
		var wg sync.WaitGroup
		for _, r := range regions {
			wg.Add(1)
			go func(r gateRegion) {
				defer wg.Done()
				g.watchRegion(ctx, r, remaining)
			}(r)
		}
		wg.Wait()

		v := g.census(ctx, regions, true)
		v.Waited = time.Since(started)
		if v.Converged {
			g.emit("info", fmt.Sprintf("Every region is installed (%d region(s), after %s). Sovereign ready for handover.", len(regions), v.Waited.Round(time.Second)))
			return v
		}
		if ctx.Err() != nil || time.Since(started) >= g.Budget || !g.watching() {
			g.emit("warn", fmt.Sprintf("The deployment is not ready: after %s not every region is installed — %s. Nothing is torn down; the regions keep converging and the watch can be re-attached.",
				v.Waited.Round(time.Second), v.Summary()))
			return v
		}
		if s := v.Summary(); s != lastSummary {
			lastSummary = s
			g.emit("info", "Not ready yet: "+s+".")
		}
		// A look that took its time was a real watch: start the pause over.
		if time.Since(looked) > maxRespin {
			respin = g.respin()
		}
		pause(ctx, respin)
		if respin *= 2; respin > maxRespin {
			respin = maxRespin
		}
	}
}

func (g Gate) respin() time.Duration {
	if g.Respin > 0 {
		return g.Respin
	}
	return DefaultRespin
}

// census lists every region once and builds the verdict from the lists
// taken together.
func (g Gate) census(ctx context.Context, regions []gateRegion, landed bool) *Verdict {
	v := &Verdict{
		CrossRegionLanded: landed,
		Pending:           map[string][]string{},
		Unread:            map[string]string{},
		States:            map[string]map[string]string{},
	}
	// The lists must be readable even when the gate's own time is up.
	listCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*g.CallTimeout)
	defer cancel()
	for _, r := range regions {
		dyn, err := g.Dynamic(r.path)
		if err != nil {
			v.Unread[r.label] = err.Error()
			continue
		}
		c, err := ReadCensus(listCtx, dyn, g.Provider, g.CallTimeout)
		if err != nil {
			v.Unread[r.label] = err.Error()
			continue
		}
		v.Pending[r.label] = c.Pending
		if r.key != "" {
			states := make(map[string]string, len(c.Snapshots))
			for _, s := range c.Snapshots {
				states[s.AppID] = s.Status
			}
			v.States[r.key] = states
		}
		if g.OnCensus != nil {
			g.OnCensus(r.key, c.Snapshots)
		}
	}
	v.Converged = v.short() == 0
	return v
}

// watchRegion waits until the region's kit Kustomization has applied its
// current desired state, then runs one Phase-1 watcher on the region until
// its releases are all terminal or the time is up. The watcher's result is
// not kept: the census that follows decides. The watcher is what makes the
// wait a watch.
func (g Gate) watchRegion(ctx context.Context, r gateRegion, budget time.Duration) {
	dyn, err := g.Dynamic(r.path)
	if err != nil {
		return
	}
	g.awaitKitApplied(ctx, dyn)
	if ctx.Err() != nil {
		return
	}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	cfg := g.WatchConfig(string(raw))
	cfg.WatchTimeout = budget
	cfg.OnHeartbeat = nil
	cfg.OnSubstate = nil
	if r.key != "" {
		cfg.ReadySentinelComponent = ""
	}
	w, err := helmwatch.NewWatcher(cfg, func(provisioner.Event) {})
	if err != nil {
		return
	}
	_, _ = w.Watch(ctx)
}

// awaitKitApplied returns when the region's kit Kustomization has applied
// its current desired state, by watching that one object. It returns at
// once when the region has no such Kustomization, and on ctx end.
func (g Gate) awaitKitApplied(ctx context.Context, dyn dynamic.Interface) {
	res := dyn.Resource(kustomizationGVR).Namespace(helmwatch.FluxNamespace)
	for ctx.Err() == nil {
		getCtx, cancel := context.WithTimeout(ctx, g.CallTimeout)
		ks, err := res.Get(getCtx, kitKustomization, metav1.GetOptions{})
		cancel()
		if apierrors.IsNotFound(err) || (err == nil && KustomizationUnsettled(ks) == "") {
			return
		}
		if err != nil {
			pause(ctx, g.respin())
			continue
		}
		w, err := res.Watch(ctx, metav1.ListOptions{
			FieldSelector:   "metadata.name=" + kitKustomization,
			ResourceVersion: ks.GetResourceVersion(),
		})
		if err != nil {
			pause(ctx, g.respin())
			continue
		}
		settled := false
		for ev := range w.ResultChan() {
			u, ok := ev.Object.(*unstructured.Unstructured)
			if ok && u.GetName() == kitKustomization && KustomizationUnsettled(u) == "" {
				settled = true
				break
			}
		}
		w.Stop()
		if settled {
			return
		}
		pause(ctx, g.respin())
	}
}

// pause waits d or until ctx ends.
func pause(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Shortfall lists every follower region once and returns what each is
// short of (empty map: nothing). For a caller about to call a record ready
// outside a live gate (the converged-late check on restart).
func Shortfall(ctx context.Context, followers map[string]string, provider string, dynamicFor func(string) (dynamic.Interface, error), callTimeout time.Duration) map[string]string {
	out := map[string]string{}
	for region, path := range followers {
		dyn, err := dynamicFor(path)
		if err != nil {
			out[region] = "could not be read: " + err.Error()
			continue
		}
		c, err := ReadCensus(ctx, dyn, provider, callTimeout)
		switch {
		case err != nil:
			out[region] = "could not be read: " + err.Error()
		case len(c.Pending) > 0:
			out[region] = strings.Join(c.Pending, ", ")
		}
	}
	return out
}
