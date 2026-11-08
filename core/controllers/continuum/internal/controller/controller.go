// Package controller is the ADR-0015 D8 Continuum: the cross-region lease,
// region-loss detection, and committing the next phase of a topology
// transition. Nothing else.
//
// It runs in EVERY region (bp-continuum renders per region, D1). Each tick it:
//
//  1. lists the Continuum CRs Flux applied in this region (one per governed
//     Application, rendered into the Application's region files by the
//     application-controller) and groups them by spec.transition.dependsOn;
//  2. reads every member's region files and the group's lease + record from
//     the writer Gitea;
//  3. reads native status — this region's directly (observe.Observer), every
//     other region's through its `continuum-<region-id>` ClusterMesh Service;
//  4. evaluates the pure phase machine (internal/transition);
//  5. if there is something to write AND this region holds (or may take) the
//     group's Git lease: compare-and-swaps the lease file, then makes the one
//     region-file commit the machine returned;
//  6. mirrors the transition record into the status of its own Continuum CRs
//     and Applications (status.activity[] in the exact D7 shape, which the
//     Jobs view projects as type `topology-transition`).
//
// Its only writes are Git commits (lease file + the two transition fields of a
// region file) and status subresources of Continuum / Application CRs. It
// never creates, patches or deletes a workload object — CNPG, Flux, the
// chart's own Services/routes and PowerDNS records act from Git.
// scripts/check-continuum-no-workload-mutation.sh enforces that.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/continuum/internal/observe"
	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// ContinuumGVR — the Continuum CRD (products/continuum/chart/crds/continuum.yaml).
var ContinuumGVR = schema.GroupVersionResource{Group: "dr.openova.io", Version: "v1", Resource: "continuums"}

// Defaults.
const (
	DefaultTick         = 10 * time.Second
	DefaultLeaseTTL     = 60 * time.Second
	DefaultRegionLoss   = 5 * time.Minute
	DefaultPhaseTimeout = 15 * time.Minute
)

// PeerFetcher reads another region's observations.
type PeerFetcher interface {
	Fetch(ctx context.Context, region string, targets []observe.Target) (map[string]transition.MemberStatus, error)
}

// LocalObserver reads this region's observations.
type LocalObserver interface {
	Observe(ctx context.Context, targets []observe.Target) map[string]transition.MemberStatus
}

// Controller is one region's Continuum.
type Controller struct {
	Dyn    dynamic.Interface
	Store  Store
	Local  LocalObserver
	Peers  PeerFetcher
	Region string
	// Regions is the Sovereign's region registry (pkg/regions), used when a
	// Continuum names no regions.
	Regions  []string
	Tick     time.Duration
	LeaseTTL time.Duration
	Log      *slog.Logger
	Now      func() time.Time

	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Controller) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// Start runs the tick loop until ctx is done (controller-runtime Runnable).
func (c *Controller) Start(ctx context.Context) error {
	tick := c.Tick
	if tick <= 0 {
		tick = DefaultTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil {
			c.log().Warn("continuum tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// member is a parsed Continuum CR.
type member struct {
	cr     *unstructured.Unstructured
	m      transition.Member
	repo   Repo
	policy transition.Policy
	gitOK  bool
	why    string
}

func parseMember(cr *unstructured.Unstructured, registry []string) member {
	out := member{cr: cr}
	ns := cr.GetNamespace()
	appRef, _, _ := unstructured.NestedString(cr.Object, "spec", "applicationRef")
	app := appRef
	if _, a, ok := strings.Cut(appRef, "/"); ok {
		app = a
	}
	if a, _, _ := unstructured.NestedString(cr.Object, "spec", "transition", "app"); a != "" {
		app = a
	}
	regions, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "regions")
	if len(regions) == 0 {
		regions = append([]string(nil), registry...)
	}
	deps, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "transition", "dependsOn")
	stateful, _, _ := unstructured.NestedBool(cr.Object, "spec", "transition", "stateful")
	out.m = transition.Member{Name: cr.GetName(), Namespace: ns, App: app, Stateful: stateful, DependsOn: deps, Regions: regions}
	owner, _, _ := unstructured.NestedString(cr.Object, "spec", "transition", "repo", "owner")
	name, _, _ := unstructured.NestedString(cr.Object, "spec", "transition", "repo", "name")
	branch, _, _ := unstructured.NestedString(cr.Object, "spec", "transition", "repo", "branch")
	if branch == "" {
		branch = "main"
	}
	out.repo = Repo{Owner: owner, Name: name, Branch: branch}
	out.gitOK = owner != "" && name != "" && app != ""
	if !out.gitOK {
		out.why = "this Continuum names no Git region files (spec.transition.repo); ADR-0015 D8 transitions only Git-delivered Applications — re-express it in the per-region layout"
	}
	auto, _, _ := unstructured.NestedBool(cr.Object, "spec", "transition", "autoPromoteOnRegionLoss")
	out.policy = transition.Policy{
		AutoPromoteOnRegionLoss: auto,
		RegionLoss:              seconds(cr, DefaultRegionLoss, "spec", "transition", "regionLossSeconds"),
		PhaseTimeout:            seconds(cr, DefaultPhaseTimeout, "spec", "transition", "phaseTimeoutSeconds"),
	}
	return out
}

func seconds(cr *unstructured.Unstructured, def time.Duration, path ...string) time.Duration {
	if v, ok, _ := unstructured.NestedInt64(cr.Object, path...); ok && v > 0 {
		return time.Duration(v) * time.Second
	}
	if v, ok, _ := unstructured.NestedFloat64(cr.Object, path...); ok && v > 0 {
		return time.Duration(v) * time.Second
	}
	return def
}

// Reconcile is one tick over every group.
func (c *Controller) Reconcile(ctx context.Context) error {
	list, err := c.Dyn.Resource(ContinuumGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // CRD not installed yet
		}
		return fmt.Errorf("list Continuums: %w", err)
	}
	byKey := map[string]member{}
	var governed []transition.Member
	for i := range list.Items {
		pm := parseMember(&list.Items[i], c.Regions)
		if !pm.gitOK {
			c.writeStatus(ctx, pm, groupStatus{outcome: "NotGitDelivered", message: pm.why})
			continue
		}
		byKey[pm.m.Namespace+"/"+pm.m.Name] = pm
		governed = append(governed, pm.m)
	}
	groups, gerrs := transition.Groups(governed)
	for _, ge := range gerrs {
		for _, m := range ge.Members {
			c.writeStatus(ctx, byKey[m.Namespace+"/"+m.Name], groupStatus{outcome: transition.OutcomeBlocked, message: ge.Error()})
		}
	}
	var errs []string
	for _, g := range groups {
		if err := c.reconcileGroup(ctx, g, byKey); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

type fileState struct {
	data []byte
	sha  string
}

type groupStatus struct {
	group     string
	outcome   string
	message   string
	committed string
	desired   string
	lease     transition.LeaseFile
	record    *transition.Record
}

func (c *Controller) reconcileGroup(ctx context.Context, g transition.Group, byKey map[string]member) error {
	now := c.now()
	members := make([]member, 0, len(g.Members))
	for _, m := range g.Members {
		members = append(members, byKey[m.Namespace+"/"+m.Name])
	}
	repo := members[0].repo
	policy := members[0].policy
	for _, pm := range members[1:] {
		if pm.repo != repo {
			return c.statusAll(ctx, members, groupStatus{group: g.ID, outcome: transition.OutcomeBlocked,
				message: fmt.Sprintf("group spans repositories (%s/%s and %s/%s); a group's region files and lease must live in one repository", repo.Owner, repo.Name, pm.repo.Owner, pm.repo.Name)})
		}
		// The strictest policy of the group applies.
		policy.AutoPromoteOnRegionLoss = policy.AutoPromoteOnRegionLoss && pm.policy.AutoPromoteOnRegionLoss
		if pm.policy.RegionLoss > policy.RegionLoss {
			policy.RegionLoss = pm.policy.RegionLoss
		}
		if pm.policy.PhaseTimeout > policy.PhaseTimeout {
			policy.PhaseTimeout = pm.policy.PhaseTimeout
		}
	}

	// Region files (from the writer Gitea).
	view := transition.View{Now: now, Files: map[string]map[string]transition.FileView{}, Regions: map[string]transition.RegionView{}}
	raw := map[string]map[string]fileState{}
	regionSet := map[string]bool{}
	for _, m := range g.Members {
		view.Files[m.Key()] = map[string]transition.FileView{}
		raw[m.Key()] = map[string]fileState{}
		for _, r := range m.Regions {
			regionSet[r] = true
			data, sha, found, err := c.Store.Read(ctx, repo, regionfile.AppPath(r, m.App))
			if err != nil {
				return c.statusAll(ctx, members, groupStatus{group: g.ID, outcome: transition.OutcomeBlocked,
					message: fmt.Sprintf("read %s from the writer Gitea: %v", regionfile.AppPath(r, m.App), err)})
			}
			fv := transition.FileView{Exists: found}
			if found {
				objs, perr := regionfile.Parse(data)
				if perr != nil {
					return c.statusAll(ctx, members, groupStatus{group: g.ID, outcome: transition.OutcomeBlocked, message: perr.Error()})
				}
				f, _, ferr := regionfile.ReadTransitionFields(objs)
				if ferr != nil {
					return c.statusAll(ctx, members, groupStatus{group: g.ID, outcome: transition.OutcomeBlocked, message: ferr.Error()})
				}
				fv.Fields = f
				fv.Desired = desiredIn(objs, m.Name)
			}
			view.Files[m.Key()][r] = fv
			raw[m.Key()][r] = fileState{data: data, sha: sha}
		}
	}

	// Native observations, per region.
	regions := make([]string, 0, len(regionSet))
	for r := range regionSet {
		regions = append(regions, r)
	}
	sort.Strings(regions)
	for _, r := range regions {
		var targets []observe.Target
		for _, m := range g.Members {
			for _, mr := range m.Regions {
				if mr == r {
					targets = append(targets, observe.Target{Namespace: m.Namespace, App: m.App, Stateful: m.Stateful})
				}
			}
		}
		view.Regions[r] = c.observeRegion(ctx, r, targets, now)
	}

	// Lease + record.
	leasePath := regionfile.LeasePath(g.ID)
	ldata, lsha, _, err := c.Store.Read(ctx, repo, leasePath)
	if err != nil {
		return c.statusAll(ctx, members, groupStatus{group: g.ID, outcome: transition.OutcomeBlocked,
			message: fmt.Sprintf("read lease %s: %v", leasePath, err)})
	}
	lease, err := transition.ParseLease(ldata)
	if err != nil {
		return c.statusAll(ctx, members, groupStatus{group: g.ID, outcome: transition.OutcomeBlocked, message: err.Error()})
	}

	res := transition.Plan(g, lease.Transition, view, policy)
	st := groupStatus{group: g.ID, outcome: res.Outcome, message: res.Message, committed: res.Committed, desired: res.Desired, lease: lease, record: lease.Transition}

	changed := res.Record != nil && !reflect.DeepEqual(normalize(res.Record), normalize(lease.Transition))
	transitioning := res.Outcome == transition.OutcomeTransitioning || res.Outcome == transition.OutcomeFailed
	ttl := c.LeaseTTL
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	renewDue := lease.Holder == c.Region && transitioning && leaseAge(lease, now) > ttl/3
	if res.Commit == nil && !changed && !renewDue {
		return c.statusAll(ctx, members, st)
	}
	if !lease.MayAct(c.Region, now) {
		// Another region holds the lease: it acts; this region mirrors.
		if lease.Transition != nil {
			st.outcome, st.message = lease.Transition.Outcome, lease.Transition.Message
		}
		if st.outcome == "" {
			st.outcome, st.message = res.Outcome, res.Message
		}
		return c.statusAll(ctx, members, st)
	}

	next := transition.LeaseFile{Group: g.ID, Holder: c.Region, RenewedAt: now.Format(time.RFC3339), TTLSeconds: int(ttl / time.Second), Transition: res.Record}
	if !transitioning && res.Commit == nil {
		next.Holder, next.RenewedAt = "", "" // release: the transition is over (or nothing is moving)
	}
	body, err := next.Marshal()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("continuum(%s): %s — %s", c.Region, g.ID, res.Message)
	if err := c.Store.Write(ctx, repo, leasePath, body, lsha, msg); err != nil {
		if err == ErrConflict {
			c.log().Info("lease compare-and-swap lost; another region acted", "group", g.ID)
			return c.statusAll(ctx, members, st)
		}
		return fmt.Errorf("write lease %s: %w", leasePath, err)
	}
	st.lease, st.record = next, res.Record

	if res.Commit != nil {
		if err := c.commitRegionFile(ctx, repo, *res.Commit, raw[res.Commit.Member][res.Commit.Region], res.Record); err != nil {
			st.message = err.Error()
			_ = c.statusAll(ctx, members, st)
			return err
		}
	}
	return c.statusAll(ctx, members, st)
}

func leaseAge(l transition.LeaseFile, now time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339, l.RenewedAt)
	if err != nil {
		return time.Duration(1<<62 - 1)
	}
	return now.Sub(t)
}

// desiredIn reads spec.desiredPrimaryRegion of the member's own Continuum
// document in a region file.
func desiredIn(objs []*unstructured.Unstructured, name string) string {
	for _, o := range objs {
		if o.GetKind() == "Continuum" && o.GetName() == name {
			d, _, _ := unstructured.NestedString(o.Object, "spec", "desiredPrimaryRegion")
			return d
		}
	}
	return ""
}

func (c *Controller) observeRegion(ctx context.Context, region string, targets []observe.Target, now time.Time) transition.RegionView {
	c.mu.Lock()
	if c.lastSeen == nil {
		c.lastSeen = map[string]time.Time{}
	}
	if _, ok := c.lastSeen[region]; !ok {
		// Loss is counted from when this Continuum started watching.
		c.lastSeen[region] = now
	}
	c.mu.Unlock()

	var obs map[string]transition.MemberStatus
	var err error
	if region == c.Region {
		obs = c.Local.Observe(ctx, targets)
	} else if c.Peers != nil {
		obs, err = c.Peers.Fetch(ctx, region, targets)
	} else {
		err = fmt.Errorf("no peer transport")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.log().Info("region not reachable", "region", region, "err", err)
		return transition.RegionView{Reachable: false, LastSeen: c.lastSeen[region]}
	}
	c.lastSeen[region] = now
	return transition.RegionView{Reachable: true, LastSeen: now, Members: obs}
}

func (c *Controller) commitRegionFile(ctx context.Context, repo Repo, cm transition.Commit, fs fileState, rec *transition.Record) error {
	objs, err := regionfile.Parse(fs.data)
	if err != nil {
		return err
	}
	n, err := regionfile.SetTransitionFields(objs, cm.Fields)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	data, err := regionfile.Marshal(objs)
	if err != nil {
		return err
	}
	path := regionfile.AppPath(cm.Region, cm.App)
	id := ""
	if rec != nil {
		id = rec.ID
	}
	msg := fmt.Sprintf("continuum(%s): topology transition %s — %s: primary=%s in %s", c.Region, id, cm.Reason, cm.Fields.Primary, cm.Region)
	if err := c.Store.Write(ctx, repo, path, data, fs.sha, msg); err != nil {
		if err == ErrConflict {
			return nil // the file moved (application-controller commit); next tick re-reads
		}
		return fmt.Errorf("commit %s: %w", path, err)
	}
	c.log().Info("committed transition phase", "path", path, "primary", cm.Fields.Primary, "reason", cm.Reason)
	return nil
}

// ── status ────────────────────────────────────────────────────────────────

func (c *Controller) statusAll(ctx context.Context, members []member, st groupStatus) error {
	for _, pm := range members {
		c.writeStatus(ctx, pm, st)
	}
	return nil
}

func phaseFor(outcome string) (phase, ready, reason string) {
	switch outcome {
	case transition.OutcomeConverged:
		return "Healthy", "True", "Converged"
	case transition.OutcomeTransitioning:
		return "Transitioning", "False", "Transitioning"
	case transition.OutcomeFailed:
		return "Failed", "False", "TransitionFailed"
	case transition.OutcomeBlocked:
		return "Degraded", "False", "TransitionBlocked"
	case transition.OutcomePending:
		return "Pending", "Unknown", "NoRegionFiles"
	}
	return "Unknown", "Unknown", outcome
}

// memberActivity is the slice of a record that acts on one member.
func memberActivity(rec *transition.Record, m transition.Member) []interface{} {
	out := []interface{}{}
	if rec == nil {
		return out
	}
	suffix := "-" + m.Namespace + "-" + m.App
	for _, a := range rec.Activity {
		if !strings.HasSuffix(a.ID, suffix) {
			continue
		}
		e := map[string]interface{}{"id": a.ID, "phase": a.Phase, "member": a.Member, "state": a.State}
		if a.StartedAt != "" {
			e["startedAt"] = a.StartedAt
		}
		if a.FinishedAt != "" {
			e["finishedAt"] = a.FinishedAt
		}
		if a.Message != "" {
			e["message"] = a.Message
		}
		out = append(out, e)
	}
	return out
}

func (c *Controller) writeStatus(ctx context.Context, pm member, st groupStatus) {
	if pm.cr == nil {
		return
	}
	phase, ready, reason := phaseFor(st.outcome)
	cur, _, _ := unstructured.NestedMap(pm.cr.Object, "status")
	if cur == nil {
		cur = map[string]interface{}{}
	}
	ltt := c.now().Format(time.RFC3339)
	if conds, ok := cur["conditions"].([]interface{}); ok {
		for _, x := range conds {
			if cm, ok := x.(map[string]interface{}); ok && cm["type"] == "Ready" && cm["status"] == ready && cm["reason"] == reason {
				if t, ok := cm["lastTransitionTime"].(string); ok {
					ltt = t
				}
			}
		}
	}
	status := map[string]interface{}{
		"phase":              phase,
		"observedGeneration": pm.cr.GetGeneration(),
		"conditions": []interface{}{map[string]interface{}{
			"type": "Ready", "status": ready, "reason": reason, "message": st.message, "lastTransitionTime": ltt,
		}},
		"activity": memberActivity(st.record, pm.m),
	}
	if st.committed != "" {
		status["primaryRegion"] = st.committed
	}
	if st.desired != "" {
		status["desiredPrimaryRegion"] = st.desired
	}
	status["converged"] = st.outcome == transition.OutcomeConverged && st.committed != "" && st.committed == st.desired
	status["leaseHolder"] = st.lease.Holder
	status["leaseExpiresAt"] = st.lease.ExpiresAt()
	tr := map[string]interface{}{"group": st.group, "outcome": st.outcome, "message": st.message}
	if st.record != nil {
		tr["id"], tr["from"], tr["to"], tr["startedAt"], tr["lossMode"] = st.record.ID, st.record.From, st.record.To, st.record.StartedAt, st.record.LossMode
	}
	status["transition"] = tr
	if st.lease.Holder == "" {
		delete(status, "leaseExpiresAt")
	}

	same := true
	for k, v := range status {
		if !reflect.DeepEqual(normalize(cur[k]), normalize(v)) {
			same = false
			break
		}
	}
	if !same {
		body, _ := json.Marshal(map[string]interface{}{"status": status})
		if _, err := c.Dyn.Resource(ContinuumGVR).Namespace(pm.cr.GetNamespace()).Patch(ctx, pm.cr.GetName(), types.MergePatchType, body, metav1.PatchOptions{}, "status"); err != nil {
			c.log().Warn("patch Continuum status", "name", pm.cr.GetName(), "err", err)
		}
	}
	c.writeApplicationActivity(ctx, pm, status["activity"].([]interface{}))
}

// writeApplicationActivity mirrors the member's phases onto its Application
// (when the Application lives in this region's API), so the Application's own
// resource carries its transition and Jobs projects it there too.
func (c *Controller) writeApplicationActivity(ctx context.Context, pm member, activity []interface{}) {
	app, err := c.Dyn.Resource(observe.ApplicationGVR).Namespace(pm.m.Namespace).Get(ctx, pm.m.App, metav1.GetOptions{})
	if err != nil {
		return
	}
	cur, _, _ := unstructured.NestedSlice(app.Object, "status", "activity")
	if reflect.DeepEqual(normalize(cur), normalize(activity)) || (len(cur) == 0 && len(activity) == 0) {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{"status": map[string]interface{}{"activity": activity}})
	if _, err := c.Dyn.Resource(observe.ApplicationGVR).Namespace(pm.m.Namespace).Patch(ctx, pm.m.App, types.MergePatchType, body, metav1.PatchOptions{}, "status"); err != nil {
		c.log().Warn("patch Application status.activity", "app", pm.m.Key(), "err", err)
	}
}

// normalize round-trips through JSON so int/int64/float64 compare equal.
func normalize(v interface{}) interface{} {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out interface{}
	_ = json.Unmarshal(b, &out)
	return out
}
