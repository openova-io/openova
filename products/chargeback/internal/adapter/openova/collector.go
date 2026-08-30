package openova

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/window"
)

// Platform SKUs (allocation-based, hourly — the request is the entitlement
// the plan quota enforces, so the request is what is billed).
const (
	SKUVCPU  = "k8s.vcpu"
	UnitVCPU = "vcpu-hour"
	SKUMem   = "k8s.mem_gb"
	UnitMem  = "gib-hour"
	SKUPVC   = "k8s.pvc_gb"
	UnitPVC  = "gb-hour"

	// orgLabel joins a host namespace to its Organization — the same key
	// the sovereign-admin dashboard's buildPodRows uses
	// (products/catalyst/bootstrap/api/internal/handler/dashboard.go).
	orgLabel       = "openova.io/organization"
	orgLabelLegacy = "catalyst.openova.io/organization"

	platformBackfill = 31 * 24 * time.Hour
	usageBatch       = 500
)

// PlatformCollector watches pods and PVCs across Organization-labelled
// namespaces and emits usage_records per Organization (ADR-0014 D3 case 1):
// event-driven through informers, with an hourly reconciliation pass (D3a).
// Windows are sliced by the same internal/window math the cloud collector
// uses, so a re-run over the same hour updates the same rows.
type PlatformCollector struct {
	Client    kubernetes.Interface
	Repo      Repository
	Metrics   *metrics.Registry
	Reconcile time.Duration // reconciliation pass interval; 0 = 1h (D3a)
	Debounce  time.Duration // event → emit delay; 0 = 30s
	Now       func() time.Time

	mu    sync.Mutex
	nsOrg map[string]string           // namespace → Organization slug
	res   map[string]*trackedResource // resource key → lifecycle + factors
	dirty map[string]bool             // Organization slugs touched by events
}

// trackedResource is the collector's in-memory record of one pod or PVC.
// Informer events set the boundaries; emission derives hour slices from
// them. Requests are captured at observation time.
type trackedResource struct {
	Org       string
	Namespace string
	Name      string
	Kind      string // "pod" | "pvc"
	VCPU      float64
	MemGiB    float64
	PVCGB     float64
	Created   time.Time
	Deleted   time.Time // zero = alive
}

func (c *PlatformCollector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *PlatformCollector) metricsReg() *metrics.Registry {
	if c.Metrics != nil {
		return c.Metrics
	}
	return metrics.Default
}

func (c *PlatformCollector) reconcile() time.Duration {
	if c.Reconcile > 0 {
		return c.Reconcile
	}
	return time.Hour
}

func (c *PlatformCollector) debounce() time.Duration {
	if c.Debounce > 0 {
		return c.Debounce
	}
	return 30 * time.Second
}

func (c *PlatformCollector) init() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nsOrg == nil {
		c.nsOrg = map[string]string{}
	}
	if c.res == nil {
		c.res = map[string]*trackedResource{}
	}
	if c.dirty == nil {
		c.dirty = map[string]bool{}
	}
}

func orgOfNamespace(ns *corev1.Namespace) string {
	if v := ns.Labels[orgLabel]; v != "" {
		return v
	}
	return ns.Labels[orgLabelLegacy]
}

// ObserveNamespace records (or clears) a namespace's Organization mapping.
func (c *PlatformCollector) ObserveNamespace(ns *corev1.Namespace) {
	c.init()
	org := orgOfNamespace(ns)
	c.mu.Lock()
	defer c.mu.Unlock()
	if org == "" {
		delete(c.nsOrg, ns.Name)
		return
	}
	c.nsOrg[ns.Name] = org
	c.dirty[org] = true
}

func resourceKey(kind, namespace, name, uid string) string {
	if uid != "" {
		return kind + "/" + uid
	}
	return kind + "/" + namespace + "/" + name
}

// ObservePod tracks a pod in an Organization namespace. A pod that ran to
// completion (Succeeded/Failed) stops billing at the moment it is observed
// finished — its requests are no longer scheduled entitlement.
func (c *PlatformCollector) ObservePod(pod *corev1.Pod) {
	c.init()
	c.mu.Lock()
	org, ok := c.nsOrg[pod.Namespace]
	c.mu.Unlock()
	if !ok {
		return
	}
	var cores, gib float64
	for _, ct := range pod.Spec.Containers {
		if q, ok := ct.Resources.Requests[corev1.ResourceCPU]; ok {
			cores += float64(q.MilliValue()) / 1000
		}
		if q, ok := ct.Resources.Requests[corev1.ResourceMemory]; ok {
			gib += float64(q.Value()) / (1 << 30)
		}
	}
	key := resourceKey("pod", pod.Namespace, pod.Name, string(pod.UID))
	c.mu.Lock()
	defer c.mu.Unlock()
	tr, ok := c.res[key]
	if !ok {
		tr = &trackedResource{Org: org, Namespace: pod.Namespace, Name: pod.Name, Kind: "pod", Created: pod.CreationTimestamp.Time.UTC()}
		if tr.Created.IsZero() {
			tr.Created = c.now()
		}
		c.res[key] = tr
	}
	tr.VCPU, tr.MemGiB = cores, gib
	if pod.DeletionTimestamp != nil {
		tr.Deleted = pod.DeletionTimestamp.Time.UTC()
	} else if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		if tr.Deleted.IsZero() {
			tr.Deleted = c.now()
		}
	} else {
		tr.Deleted = time.Time{}
	}
	c.dirty[org] = true
}

// ObservePodDeleted closes a pod's billing window.
func (c *PlatformCollector) ObservePodDeleted(pod *corev1.Pod) {
	c.closeResource(resourceKey("pod", pod.Namespace, pod.Name, string(pod.UID)), pod.DeletionTimestamp)
}

// ObservePVC tracks a PersistentVolumeClaim in an Organization namespace.
func (c *PlatformCollector) ObservePVC(pvc *corev1.PersistentVolumeClaim) {
	c.init()
	c.mu.Lock()
	org, ok := c.nsOrg[pvc.Namespace]
	c.mu.Unlock()
	if !ok {
		return
	}
	var gb float64
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		gb = float64(q.Value()) / 1e9
	} else if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		gb = float64(q.Value()) / 1e9
	}
	key := resourceKey("pvc", pvc.Namespace, pvc.Name, string(pvc.UID))
	c.mu.Lock()
	defer c.mu.Unlock()
	tr, ok := c.res[key]
	if !ok {
		tr = &trackedResource{Org: org, Namespace: pvc.Namespace, Name: pvc.Name, Kind: "pvc", Created: pvc.CreationTimestamp.Time.UTC()}
		if tr.Created.IsZero() {
			tr.Created = c.now()
		}
		c.res[key] = tr
	}
	tr.PVCGB = gb
	if pvc.DeletionTimestamp != nil {
		tr.Deleted = pvc.DeletionTimestamp.Time.UTC()
	} else {
		tr.Deleted = time.Time{}
	}
	c.dirty[org] = true
}

// ObservePVCDeleted closes a PVC's billing window.
func (c *PlatformCollector) ObservePVCDeleted(pvc *corev1.PersistentVolumeClaim) {
	c.closeResource(resourceKey("pvc", pvc.Namespace, pvc.Name, string(pvc.UID)), pvc.DeletionTimestamp)
}

func (c *PlatformCollector) closeResource(key string, ts *metav1.Time) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	tr, ok := c.res[key]
	if !ok {
		return
	}
	if tr.Deleted.IsZero() {
		if ts != nil && !ts.Time.IsZero() {
			tr.Deleted = ts.Time.UTC()
		} else {
			tr.Deleted = c.now()
		}
	}
	c.dirty[tr.Org] = true
}

// Run wires the informers and drives the two cadences: an immediate emit
// after cache sync, the debounced event-driven emits, and the hourly
// reconciliation pass. Blocks until ctx is done.
func (c *PlatformCollector) Run(ctx context.Context) {
	c.init()
	factory := informers.NewSharedInformerFactory(c.Client, c.reconcile())
	nsInf := factory.Core().V1().Namespaces().Informer()
	podInf := factory.Core().V1().Pods().Informer()
	pvcInf := factory.Core().V1().PersistentVolumeClaims().Informer()

	_, _ = nsInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if ns, ok := obj.(*corev1.Namespace); ok {
				c.ObserveNamespace(ns)
				c.rescanNamespace(podInf, pvcInf, ns.Name)
			}
		},
		UpdateFunc: func(_, obj any) {
			if ns, ok := obj.(*corev1.Namespace); ok {
				c.ObserveNamespace(ns)
				c.rescanNamespace(podInf, pvcInf, ns.Name)
			}
		},
	})
	_, _ = podInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.podEvent(obj) },
		UpdateFunc: func(_, obj any) { c.podEvent(obj) },
		DeleteFunc: func(obj any) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			if pod, ok := obj.(*corev1.Pod); ok {
				c.ObservePodDeleted(pod)
			}
		},
	})
	_, _ = pvcInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.pvcEvent(obj) },
		UpdateFunc: func(_, obj any) { c.pvcEvent(obj) },
		DeleteFunc: func(obj any) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			if pvc, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				c.ObservePVCDeleted(pvc)
			}
		},
	})

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), nsInf.HasSynced, podInf.HasSynced, pvcInf.HasSynced) {
		return
	}
	slog.Info("openova adapter: platform collector started", "reconcile", c.reconcile(), "debounce", c.debounce())

	c.EmitAll(ctx)
	tick := time.NewTicker(c.reconcile())
	defer tick.Stop()
	deb := time.NewTicker(c.debounce())
	defer deb.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.EmitAll(ctx)
		case <-deb.C:
			c.EmitDirty(ctx)
		}
	}
}

func (c *PlatformCollector) podEvent(obj any) {
	if pod, ok := obj.(*corev1.Pod); ok {
		c.ObservePod(pod)
	}
}

func (c *PlatformCollector) pvcEvent(obj any) {
	if pvc, ok := obj.(*corev1.PersistentVolumeClaim); ok {
		c.ObservePVC(pvc)
	}
}

// rescanNamespace replays a namespace's pods and PVCs after its
// Organization label is learned — informer ordering may deliver the pods
// before the namespace, and those adds were skipped.
func (c *PlatformCollector) rescanNamespace(podInf, pvcInf cache.SharedIndexInformer, namespace string) {
	for _, obj := range podInf.GetStore().List() {
		if pod, ok := obj.(*corev1.Pod); ok && pod.Namespace == namespace {
			c.ObservePod(pod)
		}
	}
	for _, obj := range pvcInf.GetStore().List() {
		if pvc, ok := obj.(*corev1.PersistentVolumeClaim); ok && pvc.Namespace == namespace {
			c.ObservePVC(pvc)
		}
	}
}

// EmitAll runs one reconciliation pass over every tracked Organization.
func (c *PlatformCollector) EmitAll(ctx context.Context) {
	c.init()
	c.mu.Lock()
	orgs := map[string]bool{}
	for _, tr := range c.res {
		orgs[tr.Org] = true
	}
	c.dirty = map[string]bool{}
	c.mu.Unlock()
	for org := range orgs {
		if ctx.Err() != nil {
			return
		}
		if _, err := c.EmitOrg(ctx, org); err != nil {
			slog.Warn("openova adapter: platform emit failed; continuing with the next Organization", "org", org, "error", err)
		}
	}
}

// EmitDirty emits only the Organizations touched by events since the last
// pass — the event-driven half of D3a.
func (c *PlatformCollector) EmitDirty(ctx context.Context) {
	c.init()
	c.mu.Lock()
	orgs := make([]string, 0, len(c.dirty))
	for org := range c.dirty {
		orgs = append(orgs, org)
	}
	c.dirty = map[string]bool{}
	c.mu.Unlock()
	sort.Strings(orgs)
	for _, org := range orgs {
		if ctx.Err() != nil {
			return
		}
		if _, err := c.EmitOrg(ctx, org); err != nil {
			slog.Warn("openova adapter: platform emit failed; continuing with the next Organization", "org", org, "error", err)
		}
	}
}

// EmitOrg recomputes one Organization's usage from its source's last
// collection stamp to now and upserts it (idempotent per hour slice).
func (c *PlatformCollector) EmitOrg(ctx context.Context, org string) (int, error) {
	c.init()
	now := c.now()
	cust, err := c.Repo.GetCustomerBySlug(ctx, org)
	if errors.Is(err, store.ErrNotFound) {
		// The Organization sync has not created the customer yet; the next
		// pass picks the records up — nothing is lost, the windows are
		// recomputed from the tracked lifecycles.
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	src, err := c.orgSource(ctx, cust)
	if err != nil {
		return 0, err
	}
	from := now.Add(-platformBackfill)
	if src.LastCollectedAt != nil && !src.LastCollectedAt.IsZero() && src.LastCollectedAt.After(from) {
		from = src.LastCollectedAt.UTC()
	}

	c.mu.Lock()
	var tracked []*trackedResource
	var keys []string
	for k, tr := range c.res {
		if tr.Org == org {
			cp := *tr
			tracked = append(tracked, &cp)
			keys = append(keys, k)
		}
	}
	c.mu.Unlock()

	var batch []store.UsageRecord
	written := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := c.Repo.UpsertUsage(ctx, batch)
		written += n
		batch = batch[:0]
		return err
	}
	for i, tr := range tracked {
		if !tr.Deleted.IsZero() && tr.Deleted.Before(from.Truncate(time.Hour)) {
			continue
		}
		lc := window.Lifecycle{Created: tr.Created, Deleted: tr.Deleted}
		for _, sl := range window.HourSlices(from, now, lc) {
			for _, line := range platformSKUs(tr) {
				qty := window.Quantity(sl.Hours(), line.factor)
				if qty <= 0 {
					continue
				}
				labels, _ := json.Marshal(map[string]any{"name": tr.Namespace + "/" + tr.Name, "namespace": tr.Namespace, "kind": tr.Kind})
				batch = append(batch, store.UsageRecord{
					CustomerID:   cust.ID,
					SourceID:     src.ID,
					ResourceID:   resourceIDOf(keys[i]),
					ResourceKind: "k8s-" + tr.Kind,
					SKU:          line.sku,
					Quantity:     store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)),
					Unit:         line.unit,
					WindowStart:  sl.Start,
					WindowEnd:    sl.End,
					Labels:       labels,
				})
				if len(batch) >= usageBatch {
					if err := flush(); err != nil {
						return written, err
					}
				}
			}
		}
	}
	if err := flush(); err != nil {
		return written, err
	}
	if err := c.Repo.SetSourceCollected(ctx, src.ID, now); err != nil {
		return written, err
	}
	c.prune(now)
	c.metricsReg().Inc("chargeback_platform_usage_records_written_total", "Platform usage records upserted", nil, float64(written))
	if written > 0 {
		slog.Info("openova adapter: platform usage emitted", "org", org, "records", written, "window_from", from, "window_to", now)
	}
	return written, nil
}

type skuLine struct {
	sku    string
	unit   string
	factor float64
}

func platformSKUs(tr *trackedResource) []skuLine {
	switch tr.Kind {
	case "pod":
		return []skuLine{
			{SKUVCPU, UnitVCPU, tr.VCPU},
			{SKUMem, UnitMem, tr.MemGiB},
		}
	case "pvc":
		return []skuLine{{SKUPVC, UnitPVC, tr.PVCGB}}
	}
	return nil
}

func resourceIDOf(key string) string { return key }

// orgSource finds — or auto-creates — the customer's per-Organization
// platform source (one cost_source per Organization).
func (c *PlatformCollector) orgSource(ctx context.Context, cust store.Customer) (store.CostSource, error) {
	srcs, err := c.Repo.ListSources(ctx, store.OperatorScope, cust.ID)
	if err != nil {
		return store.CostSource{}, err
	}
	for _, s := range srcs {
		if s.Kind == SourceKindOrg {
			return s, nil
		}
	}
	slug := cust.Slug
	if cust.OrgSlug != nil && *cust.OrgSlug != "" {
		slug = *cust.OrgSlug
	}
	src, _, err := c.Repo.UpsertSource(ctx, cust.ID, SourceKindOrg, "", slug)
	if err != nil {
		return store.CostSource{}, fmt.Errorf("auto-create platform source: %w", err)
	}
	if src.Status != "verified" {
		if err := c.Repo.SetSourceVerified(ctx, src.ID, ""); err != nil {
			return store.CostSource{}, err
		}
	}
	return src, nil
}

// prune drops resources whose whole life has been emitted: deleted more
// than two reconciliation passes ago, so the final partial hour was written.
func (c *PlatformCollector) prune(now time.Time) {
	cutoff := now.Add(-2 * c.reconcile())
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, tr := range c.res {
		if !tr.Deleted.IsZero() && tr.Deleted.Before(cutoff) {
			delete(c.res, k)
		}
	}
}
