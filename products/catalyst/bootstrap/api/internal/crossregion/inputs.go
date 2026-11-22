package crossregion

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// Follower-region inputs (#7902 defects 3, 4, 5).
//
// A follower region runs its own Keycloak, Gitea and Harbor against the
// PRIMARY region's database (one shared database per engine, reached over
// the mesh). Whatever the primary seeded in that database, or minted for a
// repository both regions serve, the follower must hold the same value of.
// Nothing inside a cluster can carry a Secret to another cluster, and the
// charts that write these Secrets either mint a different value per region
// or are suspended in a follower. The parent's ClusterMesh orchestrator is
// the one actor that reaches every region, so it copies them, primary to
// every follower, at the START of each pass: before the mesh is wired and
// before the database-pair switch, beside the database Secrets.
//
// Why at the start. The Keycloak admin copy used to sit in the last stage
// of the database-pair switch, behind two waits (the pair's and the shared
// engines' replication Secrets). On the fresh installation of 15 November
// 2026 it landed three minutes after the follower's bp-keycloak had used up
// its retries on `HTTP 401 Unauthorized`. None of these Secrets needs the
// mesh or the pair: they carry a password or a token, no host.

// CopiedSecret is one Secret the orchestrator copies from the primary
// region to every follower.
type CopiedSecret struct {
	Namespace string
	Name      string
}

// CopiedSecrets lists them. Each entry says why the follower cannot make
// the value itself.
var CopiedSecrets = []CopiedSecret{
	// Defect 3. Both regions' Keycloak use the primary's database, whose
	// master admin the primary seeded. The follower's own minted
	// `keycloak-admin` answers `invalid_user_credentials`; its config-cli
	// hook then fails with HTTP 401 until bp-keycloak has used up its
	// retries, and seven releases depend on it. The second Secret is the
	// derived master-realm credential the SSO bridge logs in with.
	{Namespace: "keycloak", Name: "keycloak-admin"},
	{Namespace: "keycloak", Name: "catalyst-kc-master-admin-credentials"},
	// Defect 4. clusters/_template/sovereign-region/base/gitrepository.yaml:
	// every region's `sovereign-region` source reads its own region's Gitea
	// with this basic-auth Secret (Gitea requires sign-in). It is written by
	// the platform chart
	// (products/catalyst/chart/templates/org-services/org-repo-gitrepo-auth-secret.yaml),
	// which is suspended in a follower, so the follower's source failed with
	// `failed to get secret 'flux-system/openova-org-tenants-git-auth'` and
	// no per-region object of any Organization reached that region. Both
	// regions' Gitea share one database, so the primary's token is valid in
	// the follower.
	{Namespace: helmwatch.FluxNamespace, Name: "openova-org-tenants-git-auth"},
	// Defect 5. platform/harbor/chart/templates/admin-secret.yaml mints a
	// random admin password per cluster. Harbor's `admin` row lives in the
	// shared database with the PRIMARY's password, so the follower's own
	// value answers 401: its setup Jobs (the pull login, the projects) fail
	// and the release, which does not wait for them, stays Ready. The chart
	// keeps an existing value (lookup), so the copy is stable. The failed
	// Jobs are run again by rerunFailedReaders.
	{Namespace: "harbor", Name: "harbor-admin"},
	// #7908. The key pair Harbor's core signs registry tokens with, and
	// verifies presented tokens with. The registry's public name sends the
	// requests of one push to both regions, so a token signed by one region's
	// key is dropped by the other (`token signature is invalid`), the request
	// is anonymous and the push is refused. One key for the Sovereign: the
	// primary's, which cert-manager issues there
	// (platform/harbor/chart/templates/token-key-certificate.yaml). A follower
	// issues none, and its core cannot start before this copy. The core reads
	// the key once, from a subPath mount; when the copy changes, bp-reloader
	// rolls it (its pod template names this Secret).
	{Namespace: "harbor", Name: "harbor-token-key"},
	// The same balancer, for a blob upload and a CSRF token. The registry
	// signs the state of a chunked upload with REGISTRY_HTTP_SECRET and the
	// core signs its CSRF cookie with CSRF_KEY; with a value per region an
	// upload begun in one region is refused in the other (BLOB_UPLOAD_INVALID)
	// and so is a write whose token the other region issued (403). The primary
	// writes both once and keeps them
	// (platform/harbor/chart/templates/shared-keys-secret.yaml); a follower
	// writes none, and its registry and core cannot start before this copy.
	{Namespace: "harbor", Name: "harbor-shared-keys"},
}

// Follower is one follower region as the orchestrator reaches it.
type Follower struct {
	// Key is the region key ("me-east-215-b-1").
	Key    string
	Client kubernetes.Interface
	// Dynamic builds the region's dynamic client; called once per pass.
	Dynamic func() (dynamic.Interface, error)
}

// Copier writes a copy of src into a destination cluster's namespace and
// reports whether it changed the destination's bytes. The handler passes
// its own cross-cluster copy, the one writer of these Secrets.
type Copier func(ctx context.Context, src *corev1.Secret, dst kubernetes.Interface, namespace string) (changed bool, err error)

// Deliveries remembers, per deployment and follower region, when a pass
// last CHANGED an input of that region, and per Secret when its current
// bytes arrived. In memory: after a restart of the process nothing is
// remembered until the next change, so a release or Job that failed before
// the restart is not restarted by the delivery rules until then.
type Deliveries struct{ m sync.Map }

func deliveryKey(deploymentID, region, secret string) string {
	return deploymentID + "|" + region + "|" + secret
}

// Note records a change of the region's inputs by a pass that started at
// `at`. secret is "<namespace>/<name>", or "" for inputs this package does
// not copy itself (the database Secrets). Only ever moves forward.
func (d *Deliveries) Note(deploymentID, region, secret string, at time.Time) {
	if d == nil || at.IsZero() {
		return
	}
	for _, key := range []string{deliveryKey(deploymentID, region, ""), deliveryKey(deploymentID, region, secret)} {
		if prev, ok := d.m.Load(key); ok {
			if pt, isTime := prev.(time.Time); isTime && !pt.Before(at) {
				continue
			}
		}
		d.m.Store(key, at)
	}
}

// At returns the time of the latest recorded change, or the zero time.
func (d *Deliveries) At(deploymentID, region, secret string) time.Time {
	if d == nil {
		return time.Time{}
	}
	if v, ok := d.m.Load(deliveryKey(deploymentID, region, secret)); ok {
		if t, isTime := v.(time.Time); isTime {
			return t
		}
	}
	return time.Time{}
}

// InputSync is one pass of the follower-input work for one deployment.
type InputSync struct {
	DeploymentID string
	// Primary is the primary region's client: the source of every copy.
	Primary   kubernetes.Interface
	Followers []Follower
	Copy      Copier
	// Deliveries is shared across passes (a field of the handler).
	Deliveries *Deliveries
	// CallTimeout bounds each API call.
	CallTimeout time.Duration
	Log         *slog.Logger
	// Emit publishes one progress line to the deployment's event stream.
	Emit func(level, message string)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s InputSync) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s InputSync) emit(level, message string) {
	if s.Emit != nil {
		s.Emit(level, message)
	}
}

// Run copies CopiedSecrets to every follower, then looks at each follower:
// failed Jobs that read a Secret whose bytes arrived after the Job began
// are run again, and stalled releases get a fresh attempt (HealReleases).
//
// Best-effort throughout, as every copy of the orchestrator is: a source
// the primary has not made yet, or a follower that cannot be written, is
// logged and left to the next pass. It never refuses or delays anything.
func (s InputSync) Run(ctx context.Context) {
	if s.Primary == nil || len(s.Followers) == 0 || s.Copy == nil {
		return
	}
	startedAt := s.now()

	for _, cs := range CopiedSecrets {
		getCtx, cancel := context.WithTimeout(ctx, s.CallTimeout)
		src, err := s.Primary.CoreV1().Secrets(cs.Namespace).Get(getCtx, cs.Name, metav1.GetOptions{})
		cancel()
		if err != nil {
			s.Log.Info("crossregion: follower input: source Secret not present on the primary region yet — skipping (best-effort, re-run converges)",
				"id", s.DeploymentID, "namespace", cs.Namespace, "secret", cs.Name)
			continue
		}
		changed := make([]string, 0, len(s.Followers))
		for _, f := range s.Followers {
			if f.Client == nil {
				continue
			}
			did, err := s.Copy(ctx, src, f.Client, cs.Namespace)
			if err != nil {
				s.Log.Warn("crossregion: follower input: copy to the follower region failed — skipping this Secret/region (best-effort, re-run converges)",
					"id", s.DeploymentID, "region", f.Key, "namespace", cs.Namespace, "secret", cs.Name, "err", err)
				continue
			}
			if did {
				s.Deliveries.Note(s.DeploymentID, f.Key, cs.Namespace+"/"+cs.Name, startedAt)
				changed = append(changed, f.Key)
			}
		}
		if len(changed) > 0 {
			s.Log.Info("crossregion: follower input Secret copied primary → follower region(s)",
				"id", s.DeploymentID, "namespace", cs.Namespace, "secret", cs.Name, "regions", changed)
			s.emit("info", fmt.Sprintf("ClusterMesh: copied Secret %s/%s from the primary region to %s — the follower shares the primary's database and cannot make this value itself",
				cs.Namespace, cs.Name, strings.Join(changed, ", ")))
		}
	}

	for _, f := range s.Followers {
		if f.Client == nil || f.Dynamic == nil {
			continue
		}
		dyn, err := f.Dynamic()
		if err != nil {
			s.Log.Warn("crossregion: follower heal: dynamic client build failed — skipping this pass (best-effort)",
				"id", s.DeploymentID, "region", f.Key, "err", err)
			continue
		}
		for _, cs := range CopiedSecrets {
			s.rerunFailedReaders(ctx, f, dyn, cs)
		}
		s.HealReleases(ctx, f.Key, dyn)
	}
}

// Origin labels helm-controller puts on every object of a release.
const (
	labelReleaseName      = "helm.toolkit.fluxcd.io/name"
	labelReleaseNamespace = "helm.toolkit.fluxcd.io/namespace"
	annotationHelmHook    = "helm.sh/hook"
)

// rerunFailedReaders runs again the failed Jobs of a follower that read a
// copied Secret and began BEFORE that Secret's current bytes arrived
// (#7902 defect 5).
//
// Such a Job failed on the old value and nothing runs it again: it is a
// plain Job of a release that is Ready, so Flux has no reason to touch it.
// The Job is deleted and the release that owns it (the origin labels) is
// asked for a fresh attempt, in which Helm creates the missing Job again.
// It then mounts the Secret as it is now.
//
// Left alone: a Job that began after the delivery (it failed with the right
// value: a real failure), a Helm hook (it belongs to an attempt of its
// release, which HealReleases restarts as a whole), and a Job with no
// release to create it again.
func (s InputSync) rerunFailedReaders(ctx context.Context, f Follower, dyn dynamic.Interface, cs CopiedSecret) {
	deliveredAt := s.Deliveries.At(s.DeploymentID, f.Key, cs.Namespace+"/"+cs.Name)
	if deliveredAt.IsZero() {
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, s.CallTimeout)
	jobs, err := f.Client.BatchV1().Jobs(cs.Namespace).List(listCtx, metav1.ListOptions{})
	cancel()
	if err != nil {
		s.Log.Warn("crossregion: follower Job re-run: list failed — skipping this pass (best-effort)",
			"id", s.DeploymentID, "region", f.Key, "namespace", cs.Namespace, "err", err)
		return
	}
	releases := map[string]bool{}
	rerun := []string{}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		release := job.Labels[labelReleaseName]
		if !jobFailed(job) || !jobReadsSecret(job, cs.Name) || release == "" ||
			job.Labels[labelReleaseNamespace] != helmwatch.FluxNamespace || job.Annotations[annotationHelmHook] != "" {
			continue
		}
		began := job.CreationTimestamp.Time
		if job.Status.StartTime != nil {
			began = job.Status.StartTime.Time
		}
		if !began.Before(deliveredAt) {
			continue
		}
		background := metav1.DeletePropagationBackground
		delCtx, cancelDel := context.WithTimeout(ctx, s.CallTimeout)
		err := f.Client.BatchV1().Jobs(cs.Namespace).Delete(delCtx, job.Name, metav1.DeleteOptions{PropagationPolicy: &background})
		cancelDel()
		if err != nil {
			s.Log.Warn("crossregion: follower Job re-run: delete failed — left for the next pass (best-effort)",
				"id", s.DeploymentID, "region", f.Key, "namespace", cs.Namespace, "job", job.Name, "err", err)
			continue
		}
		releases[release] = true
		rerun = append(rerun, job.Name)
	}
	if len(rerun) == 0 {
		return
	}
	names := make([]string, 0, len(releases))
	for release := range releases {
		names = append(names, release)
	}
	sort.Strings(names)
	for _, release := range names {
		patchCtx, cancelPatch := context.WithTimeout(ctx, s.CallTimeout)
		_, err := helmwatch.RequestRetry(patchCtx, dyn, release, s.now())
		cancelPatch()
		if err != nil {
			s.Log.Warn("crossregion: follower Job re-run: the release could not be asked for a fresh attempt; its deleted Job(s) come back with its next upgrade",
				"id", s.DeploymentID, "region", f.Key, "helmrelease", release, "err", err)
		}
	}
	sort.Strings(rerun)
	s.Log.Info("crossregion: follower Job(s) that failed before their Secret arrived are run again",
		"id", s.DeploymentID, "region", f.Key, "namespace", cs.Namespace, "secret", cs.Name, "jobs", rerun, "releases", names)
	s.emit("info", fmt.Sprintf("ClusterMesh: region %q: %d Job(s) (%s) failed before Secret %s/%s arrived — deleted, and release(s) %s asked to create them again",
		f.Key, len(rerun), strings.Join(rerun, ", "), cs.Namespace, cs.Name, strings.Join(names, ", ")))
}

func jobFailed(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func jobReadsSecret(job *batchv1.Job, name string) bool {
	spec := job.Spec.Template.Spec
	for _, v := range spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == name {
			return true
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.Secret != nil && src.Secret.Name == name {
					return true
				}
			}
		}
	}
	for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, c := range containers {
			for _, from := range c.EnvFrom {
				if from.SecretRef != nil && from.SecretRef.Name == name {
					return true
				}
			}
			for _, e := range c.Env {
				if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// HealReleases runs on EVERY pass for one follower region and gives a fresh
// attempt to the releases that cannot get one from Flux:
//
//  1. A release that is stalled (retries used up) on an attempt that began
//     before the follower's latest input delivery. That attempt ran, at
//     least in part, without the input. Once per delivery: a release
//     already restarted after that delivery is left alone, so a failure
//     that survives the restart stays visible as what it is.
//
//     The former rule compared the FAILURE time with the delivery of the
//     database Secrets only, on the delivery pass only. It missed a release
//     that failed on a Secret delivered later (the Keycloak admin Secret),
//     and a release whose attempt began before a delivery and failed after
//     it (a hook pod that still held the old value).
//
//  2. A release whose restart request helm-controller received while a
//     dependency was not ready. helm-controller consumes such a request
//     without acting (helmwatch.RetryRequestDropped). It is sent again as
//     soon as the dependency is ready.
//
// Best-effort and read-mostly: one List per follower per pass, a write only
// for a release in one of the two states.
func (s InputSync) HealReleases(ctx context.Context, region string, dyn dynamic.Interface) {
	listCtx, cancelList := context.WithTimeout(ctx, s.CallTimeout)
	list, err := dyn.Resource(helmwatch.HelmReleaseGVR).Namespace(helmwatch.FluxNamespace).List(listCtx, metav1.ListOptions{})
	cancelList()
	if err != nil {
		s.Log.Warn("crossregion: follower release heal: list HelmReleases failed — skipping this pass (best-effort)",
			"id", s.DeploymentID, "region", region, "err", err)
		return
	}
	deliveredAt := s.Deliveries.At(s.DeploymentID, region, "")

	restarted := make([]string, 0, 2)
	for i := range list.Items {
		hr := &list.Items[i]
		if suspended, ok, _ := unstructured.NestedBool(hr.Object, "spec", "suspend"); ok && suspended {
			continue
		}
		why := ""
		switch {
		case !deliveredAt.IsZero() && helmwatch.IsStalledHelmRelease(hr) && attemptPredates(hr, deliveredAt) && !restartedSince(hr, deliveredAt):
			why = "its attempt began before the region's inputs were delivered"
		case helmwatch.RetryRequestDropped(hr) && !helmwatch.WaitingOnDependency(hr) && !helmwatch.AttemptInProgress(hr):
			why = "the earlier restart request arrived while a dependency was not ready and was dropped"
		default:
			continue
		}
		patchCtx, cancelPatch := context.WithTimeout(ctx, s.CallTimeout)
		_, patchErr := helmwatch.RequestRetry(patchCtx, dyn, hr.GetName(), s.now())
		cancelPatch()
		if patchErr != nil {
			s.Log.Warn("crossregion: follower release heal: the restart request could not be written — continuing (best-effort)",
				"id", s.DeploymentID, "region", region, "helmrelease", hr.GetName(), "err", patchErr)
			continue
		}
		s.Log.Info("crossregion: follower release restarted",
			"id", s.DeploymentID, "region", region, "helmrelease", hr.GetName(), "why", why)
		restarted = append(restarted, hr.GetName())
	}
	if len(restarted) > 0 {
		s.emit("info", fmt.Sprintf("ClusterMesh: restarted %d HelmRelease(s) (%s) in region %q — Flux never retries a release that used up its retries, and these could not have succeeded before the region's inputs arrived",
			len(restarted), strings.Join(restarted, ", "), region))
	}
}

// attemptPredates reports whether the failed attempt of a stalled release
// began before `at`. The attempt's start is the newest history entry's
// lastDeployed (helm-controller stamps it when the install or upgrade
// starts); when the history is empty (an install whose remediation
// uninstalled the release) the latest time the release went not-Ready or
// stalled stands in. A release with neither time cannot be shown to predate
// anything and is left alone.
func attemptPredates(hr *unstructured.Unstructured, at time.Time) bool {
	history, _, _ := unstructured.NestedSlice(hr.Object, "status", "history")
	if len(history) > 0 {
		if entry, ok := history[0].(map[string]any); ok {
			for _, field := range []string{"lastDeployed", "firstDeployed"} {
				if raw, _ := entry[field].(string); raw != "" {
					if ts, err := time.Parse(time.RFC3339, raw); err == nil {
						return ts.Before(at)
					}
				}
			}
		}
	}
	var latest time.Time
	conds, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := c["type"].(string)
		status, _ := c["status"].(string)
		if (typ != "Ready" || status != "False") && (typ != "Stalled" || status != "True") {
			continue
		}
		if lt, _ := c["lastTransitionTime"].(string); lt != "" {
			if ts, err := time.Parse(time.RFC3339, lt); err == nil && ts.After(latest) {
				latest = ts
			}
		}
	}
	return !latest.IsZero() && latest.Before(at)
}

// restartedSince reports whether the release already carries a restart
// request written at or after `at` (the token is the request's time).
func restartedSince(hr *unstructured.Unstructured, at time.Time) bool {
	token := hr.GetAnnotations()[helmwatch.AnnotationReconcileForceAt]
	if token == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339Nano, token)
	if err != nil {
		return false
	}
	return !ts.Before(at)
}
