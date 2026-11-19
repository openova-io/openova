package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobauth"
)

// Release delivery after cutover (#7922).
//
// A release is a release candidate ref rc/<id> that passed the cut. A Sovereign
// that has cut over follows the branch main of its own Gitea: the release it was
// installed from plus its own commits. PUT /api/v1/sovereign/release, called by a
// sovereign-admin, starts one Job that pulls a later candidate:
//
//  1. stage, in every region: fetch rc/<id> from the upstream repository, merge
//     it into that region's main (every commit of the Sovereign stays), apply the
//     region's pivot to what the candidate added, push the result as
//     release/<id>. A conflicted file whose region side is only the region's
//     registry rewrite is taken from the candidate (deleted when it deletes it);
//     any other merge conflict in any region names the files and moves
//     nothing. A dry run stops here and pushes nothing.
//     Then the cutover chart of the staged commit, verified to be the one the
//     candidate pins, is rendered: the rest of the delivery runs the
//     candidate's mechanism, never the installed one (release_job.go).
//  2. prewarm: the candidate's cutover step 03, reading the pins of release/<id>, copies
//     every chart and image they declare into the Sovereign's registry and stops
//     by name on any it cannot copy. Its upstream credential is the request's
//     pullSecret, which the stage copies into every follower region (step 03's
//     follower leg reads it there); when the prewarm ends, or the stage fails,
//     the Job deletes it in every region, and a failed prewarm is recorded as
//     phase failed with the step and its cause.
//  3. promote: if no region's main moved since stage, push release/<id> as main
//     in every region, a fast-forward the server checks. Never a force push.
//
// One region at a time (SOV-521). A request may name a region: primary, or a
// follower's name, as the state ConfigMap names them. Stage and prewarm run in
// every region as above, so a conflict anywhere moves nothing; promote checks
// and pushes that region only. Without a region every region is promoted, the
// followers first. A request with rollback and a region puts that region back
// to the main recorded at the stage of the request that promoted it: refused
// unless the region's main is still the commit that promote pushed, it pushes
// one commit whose tree is the recorded main's and whose parent is the
// promoted commit, a fast-forward, so Flux applies the earlier tree and no
// force push is ever made. It runs no stage and no prewarm. Across the
// requests of one candidate the state keeps, per region, <region>.promotedFrom,
// <region>.promotedTo and <region>.phase. A region request reaches only a
// candidate whose release-run.sh has the phase rollback (the stage refuses any
// other, nothing moved); a rollback needs the recorded promote, which only
// such a release-run.sh records.
//
// GET /api/v1/sovereign/release answers the state the Job records in the
// ConfigMap sovereign-release, with the step the Job runs now.
//
// The candidate cut (scripts/release-candidate.sh) creates an unsigned branch
// ref, so there is no signature to check. CATALYST_RELEASE_SIGNATURE names the
// policy: unset or "unverified" delivers and records "unverified"; any other
// value, "required" among them, refuses every request until the cut signs.

const (
	envReleaseSignature  = "CATALYST_RELEASE_SIGNATURE"
	signatureUnverified  = "unverified"
	envCutoverNamespace  = "CATALYST_CUTOVER_NAMESPACE"
	envCutoverStatusCM   = "CATALYST_CUTOVER_STATUS_CONFIGMAP"
	defaultCutoverNS     = "catalyst"
	defaultCutoverStatus = "self-sovereign-cutover-status"
	releaseReadTimeout   = 20 * time.Second
	maxReleaseBody       = 4096
)

var (
	candidateID = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{7}$`)
	objectName  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	commitID    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// regionStateKeys are the keys the state keeps per region across the requests
// of one candidate, as <region>.<key> (SOV-521).
var regionStateKeys = []string{"promotedFrom", "promotedTo", "phase"}

// keptRegionState is the per-region state of the earlier requests that a
// request for candidate keeps: all of it when the state is of that candidate,
// none otherwise.
func keptRegionState(cur map[string]string, candidate string) map[string]string {
	out := map[string]string{}
	if cur["candidate"] != candidate {
		return out
	}
	for k, v := range cur {
		for _, s := range regionStateKeys {
			if r, ok := strings.CutSuffix(k, "."+s); ok && r != "" {
				out[k] = v
			}
		}
	}
	return out
}

// promotedView is the per-region state as GET answers it:
// {"<region>": {"promotedFrom": ..., "promotedTo": ..., "phase": ...}}.
func promotedView(data map[string]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for k, v := range data {
		for _, s := range regionStateKeys {
			if r, ok := strings.CutSuffix(k, "."+s); ok && r != "" {
				if out[r] == nil {
					out[r] = map[string]string{}
				}
				out[r][s] = v
			}
		}
	}
	return out
}

// ReleaseRoute serves PUT and GET /api/v1/sovereign/release.
type ReleaseRoute struct {
	log     *slog.Logger
	clients func() (kubernetes.Interface, error)
	now     func() time.Time
	getenv  func(string) string
}

// NewReleaseRoute returns the route with the cluster this pod runs in.
func NewReleaseRoute(log *slog.Logger) *ReleaseRoute {
	return &ReleaseRoute{log: log, clients: jobauth.InCluster, now: time.Now, getenv: os.Getenv}
}

// SovereignAdmin reports whether a session may deliver a release: the realm
// role catalyst-admin or catalyst-owner, or the tier admin or owner. No claims,
// an Organization-scoped tier and every other role are refused.
func SovereignAdmin(c *auth.Claims) bool {
	if c == nil {
		return false
	}
	if c.HasRealmRole("catalyst-admin") || c.HasRealmRole("catalyst-owner") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(c.Tier)) {
	case "admin", "owner":
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, code int, errCode, detail string) {
	writeJSON(w, code, map[string]string{"error": errCode, "detail": detail})
}

func (rt *ReleaseRoute) namespace() string {
	if v := strings.TrimSpace(rt.getenv(envCutoverNamespace)); v != "" {
		return v
	}
	return defaultCutoverNS
}

// cutoverHelmRelease is <namespace>/<name> of the installed cutover chart's
// HelmRelease, whose values the candidate's mechanism is rendered with.
func (rt *ReleaseRoute) cutoverHelmRelease() string {
	if v := strings.TrimSpace(rt.getenv(envCutoverHelmRelease)); v != "" {
		return v
	}
	return defaultCutoverHelmRelease
}

func (rt *ReleaseRoute) cutoverStatus() string {
	if v := strings.TrimSpace(rt.getenv(envCutoverStatusCM)); v != "" {
		return v
	}
	return defaultCutoverStatus
}

// Request handles PUT /api/v1/sovereign/release with the body
// {"candidate": "<id>", "dryRun": false, "pullSecret": "<name>", "region": "<region>",
// "rollback": false}; region and rollback are optional (SOV-521). pullSecret is a
// Secret of type kubernetes.io/dockerconfigjson in the cutover namespace with an
// entry for ghcr.io; a delivery needs it, and a dry run refuses it: a dry run
// copies, uses and deletes no credential, so a Secret named for one would stay
// on the Sovereign unnoticed (#7938). A delivery uses it
// for its run only: the Job copies it into every follower region for the
// prewarm and deletes it in every region, the named Secret included, when the
// prewarm ends or the stage fails. So it must be a Secret made for the delivery,
// not one another controller manages.
func (rt *ReleaseRoute) Request(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if !SovereignAdmin(claims) {
		refuse(w, http.StatusForbidden, "forbidden", "a release is delivered by a sovereign-admin (tier admin or owner)")
		return
	}
	var req releaseRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReleaseBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		refuse(w, http.StatusBadRequest, "bad-body", err.Error())
		return
	}
	if !candidateID.MatchString(req.Candidate) {
		refuse(w, http.StatusBadRequest, "bad-candidate", "candidate is the id of a release candidate ref rc/<id>: <yyyymmddThhmmssZ>-<first 7 hex of the commit>")
		return
	}
	if req.PullSecret != "" && !objectName.MatchString(req.PullSecret) {
		refuse(w, http.StatusBadRequest, "bad-pull-secret", "pullSecret is the name of a Secret")
		return
	}
	if req.Rollback {
		switch {
		case req.Region == "":
			refuse(w, http.StatusBadRequest, "rollback-without-region", "a rollback puts one region back: name it in region (primary, or a follower's name)")
			return
		case req.DryRun:
			refuse(w, http.StatusBadRequest, "rollback-dry-run", "a rollback runs no stage, so it has no dry run: leave dryRun out of a rollback")
			return
		case req.PullSecret != "":
			refuse(w, http.StatusBadRequest, "rollback-pull-secret",
				"a rollback runs no prewarm and copies no chart or image: leave pullSecret out, and delete the Secret "+req.PullSecret+" if it was made for it")
			return
		}
	}
	if req.DryRun && req.PullSecret != "" {
		refuse(w, http.StatusBadRequest, "dry-run-pull-secret",
			"a dry run copies, uses and deletes no pull credential: leave pullSecret out of a dry run, and delete the Secret "+req.PullSecret+" if it was made for it; name it in the delivery, which deletes it when its prewarm ends")
		return
	}
	if p := strings.TrimSpace(rt.getenv(envReleaseSignature)); p != "" && p != signatureUnverified {
		refuse(w, http.StatusPreconditionFailed, "signature-required",
			fmt.Sprintf("%s=%s asks for a verified release, and candidate refs are cut unsigned (scripts/release-candidate.sh signs nothing), so no candidate can pass", envReleaseSignature, p))
		return
	}
	cs, err := rt.clients()
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "cluster-unreachable", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), releaseReadTimeout)
	defer cancel()
	ns := rt.namespace()

	st, err := cs.CoreV1().ConfigMaps(ns).Get(ctx, rt.cutoverStatus(), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		refuse(w, http.StatusBadGateway, "status-read-failed", err.Error())
		return
	}
	if err != nil || st.Data["cutoverComplete"] != "true" {
		refuse(w, http.StatusConflict, "not-cut-over",
			"this Sovereign has not completed its cutover: its Flux still follows the upstream candidate ref, so a later release is a change of that ref, not a delivery")
		return
	}
	if running, err := activeReleaseJob(ctx, cs, ns); err != nil {
		refuse(w, http.StatusBadGateway, "job-list-failed", err.Error())
		return
	} else if running != "" {
		refuse(w, http.StatusConflict, "release-in-progress", "the release Job "+running+" is running")
		return
	}
	in, err := readReleaseInputs(ctx, cs, ns)
	if in != nil {
		in.cutoverRelease = rt.cutoverHelmRelease()
	}
	var dep *dependencyError
	if errors.As(err, &dep) {
		refuse(w, http.StatusFailedDependency, "cutover-chart-incomplete", dep.Error())
		return
	} else if err != nil {
		refuse(w, http.StatusBadGateway, "cutover-chart-read-failed", err.Error())
		return
	}
	if req.Region != "" && !in.hasRegion(req.Region) {
		followers := "none"
		if len(in.followerNames) > 0 {
			followers = strings.Join(in.followerNames, ", ")
		}
		refuse(w, http.StatusBadRequest, "bad-region",
			fmt.Sprintf("region %q is not a region of this Sovereign: name %s, or a follower region (%s)", req.Region, primaryRegion, followers))
		return
	}
	prev, err := cs.CoreV1().ConfigMaps(ns).Get(ctx, ReleaseStatusConfigMap, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		refuse(w, http.StatusBadGateway, "status-read-failed", err.Error())
		return
	}
	kept := map[string]string{}
	if err == nil {
		kept = keptRegionState(prev.Data, req.Candidate)
	}
	if req.Rollback {
		from, to := kept[req.Region+".promotedFrom"], kept[req.Region+".promotedTo"]
		if !commitID.MatchString(from) || !commitID.MatchString(to) {
			refuse(w, http.StatusConflict, "not-promoted",
				fmt.Sprintf("no promote of region %s by rc/%s is recorded in %s/%s (%s.promotedFrom, %s.promotedTo): a rollback puts back the main a promote of this candidate recorded, and only a release-run.sh with the phase rollback records it; nothing moved",
					req.Region, req.Candidate, ns, ReleaseStatusConfigMap, req.Region, req.Region))
			return
		}
		in.promotedFrom, in.promotedTo = from, to
	} else if !req.DryRun {
		if why := checkPullSecret(ctx, cs, ns, req.PullSecret); why != "" {
			refuse(w, http.StatusUnprocessableEntity, "pull-secret", why)
			return
		}
	}

	now := rt.now().UTC()
	name := fmt.Sprintf("sovereign-release-%d", now.Unix())
	who := "unattributed"
	if claims.Email != "" {
		who = claims.Email
	}
	labels := map[string]string{
		"app.kubernetes.io/part-of":   releaseComponent,
		"app.kubernetes.io/component": releaseComponent,
		releaseCandidateLabel:         req.Candidate,
	}
	annotations := map[string]string{"catalyst.openova.io/requested-by": who}
	job, scripts, err := composeRelease(in, req, ns, name, labels, annotations)
	if errors.As(err, &dep) {
		refuse(w, http.StatusFailedDependency, "cutover-chart-incomplete", dep.Error())
		return
	} else if err != nil {
		refuse(w, http.StatusInternalServerError, "compose-failed", err.Error())
		return
	}
	followerPrewarm := "no follower region"
	if in.followers > 0 {
		followerPrewarm = "unknown until the stage renders the candidate's step 03, which is the one that runs"
	}
	mechanism := "unknown until the stage renders it: the cutover chart rc/" + req.Candidate + " pins, from its own tree; the installed one only bootstraps the delivery"
	if req.Rollback {
		followerPrewarm = "none: a rollback runs no prewarm"
		mechanism = "the installed release scripts: a rollback runs no stage, so no candidate is rendered"
	}
	status := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ReleaseStatusConfigMap, Namespace: ns, Labels: map[string]string{"app.kubernetes.io/component": releaseComponent}},
		Data: map[string]string{
			"candidate":       req.Candidate,
			"dryRun":          boolString(req.DryRun),
			"pullSecret":      req.PullSecret,
			"job":             name,
			"phase":           "requested",
			"requestedBy":     who,
			"requestedAt":     now.Format(time.RFC3339),
			"updatedAt":       now.Format(time.RFC3339),
			"regions":         strconv.Itoa(1 + in.followers),
			"followerPrewarm": followerPrewarm,
			"mechanism":       mechanism,
			"signature":       signatureUnverified + ": candidate refs are cut unsigned (scripts/release-candidate.sh); " + envReleaseSignature + "=required refuses every request until the cut signs",
		},
	}
	for k, v := range kept {
		status.Data[k] = v
	}
	if req.Region != "" {
		status.Data["region"] = req.Region
	}
	if req.Rollback {
		status.Data["rollback"] = "true"
	}
	for _, cm := range []*corev1.ConfigMap{scripts, status} {
		if err := putConfigMap(ctx, cs, cm); err != nil {
			refuse(w, http.StatusBadGateway, "write-failed", err.Error())
			return
		}
	}
	if _, err := cs.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		refuse(w, http.StatusBadGateway, "job-create-failed", err.Error())
		return
	}
	if rt.log != nil {
		rt.log.Info("release requested", "candidate", req.Candidate, "dryRun", req.DryRun, "region", req.Region, "rollback", req.Rollback, "job", name, "by", who)
	}
	writeJSON(w, http.StatusAccepted, releaseView(status.Data, job, nil))
}

// putConfigMap creates cm, or replaces the data of the one that exists.
func putConfigMap(ctx context.Context, cs kubernetes.Interface, cm *corev1.ConfigMap) error {
	cur, err := cs.CoreV1().ConfigMaps(cm.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	cur.Data, cur.Labels = cm.Data, cm.Labels
	_, err = cs.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cur, metav1.UpdateOptions{})
	return err
}

// activeReleaseJob names a release Job that has not ended, or "".
func activeReleaseJob(ctx context.Context, cs kubernetes.Interface, ns string) (string, error) {
	jobs, err := cs.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=" + releaseComponent})
	if err != nil {
		return "", err
	}
	for i := range jobs.Items {
		if jobState(&jobs.Items[i]) == "running" {
			return jobs.Items[i].Name, nil
		}
	}
	return "", nil
}

func jobState(j *batchv1.Job) string {
	if j == nil {
		return "missing"
	}
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return "succeeded"
		case batchv1.JobFailed:
			return "failed"
		}
	}
	return "running"
}

// checkPullSecret answers why the named Secret cannot carry the prewarm's
// upstream credential, or "" when it can.
func checkPullSecret(ctx context.Context, cs kubernetes.Interface, ns, name string) string {
	if name == "" {
		return "a delivery copies charts and images from ghcr.io, and after cutover the Sovereign holds no credential for it (cutover step 06 strips it): name a Secret of type kubernetes.io/dockerconfigjson in namespace " + ns + " with an entry for ghcr.io as pullSecret; the delivery copies it to every follower region and deletes it in every region when its prewarm ends"
	}
	sec, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Sprintf("Secret %s/%s: %v", ns, name, err)
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(sec.Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		return fmt.Sprintf("Secret %s/%s has no readable %s", ns, name, corev1.DockerConfigJsonKey)
	}
	if cfg.Auths["ghcr.io"].Auth == "" {
		return fmt.Sprintf("Secret %s/%s has no auth entry for ghcr.io", ns, name)
	}
	if owner := managedBy(sec.ObjectMeta); owner != "" {
		return fmt.Sprintf("Secret %s/%s is managed by %s, and a delivery deletes its pull Secret when the prewarm ends: name a Secret made for this delivery", ns, name, owner)
	}
	return ""
}

// managedBy names what manages an object, or "": an owner, Helm, Flux, the
// reflector, or a managed-by label. The release deletes its pull Secret, so it
// refuses one another controller would recreate or that something else reads.
func managedBy(m metav1.ObjectMeta) string {
	if len(m.OwnerReferences) > 0 {
		return fmt.Sprintf("its owner %s/%s", m.OwnerReferences[0].Kind, m.OwnerReferences[0].Name)
	}
	if v := m.Labels["app.kubernetes.io/managed-by"]; v != "" {
		return "app.kubernetes.io/managed-by=" + v
	}
	if v := m.Annotations["meta.helm.sh/release-name"]; v != "" {
		return "the Helm release " + v
	}
	for k := range m.Labels {
		if strings.HasPrefix(k, "kustomize.toolkit.fluxcd.io/") || strings.HasPrefix(k, "helm.toolkit.fluxcd.io/") {
			return "Flux (" + k + ")"
		}
	}
	for k := range m.Annotations {
		if strings.HasPrefix(k, "reflector.v1.k8s.emberstack.com/") {
			return "the reflector (" + k + ")"
		}
	}
	return ""
}

// releaseView is the answer of both routes: the recorded state, the state of
// the Job and of the step it runs now, and per region what its promote and
// rollback recorded (promoted).
func releaseView(data map[string]string, job *batchv1.Job, pods []corev1.Pod) map[string]any {
	v := map[string]any{"status": data, "phase": data["phase"], "job": data["job"], "jobState": jobState(job)}
	if p := promotedView(data); len(p) > 0 {
		v["promoted"] = p
	}
	if s := data["failedStep"]; s != "" {
		v["failedStep"] = s
	}
	if job == nil {
		return v
	}
	for _, p := range pods {
		for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
			for _, c := range list {
				switch {
				case c.State.Running != nil:
					v["step"] = c.Name
				case c.State.Terminated != nil && c.State.Terminated.ExitCode != 0:
					v["failedStep"] = c.Name
				}
			}
		}
	}
	if jobState(job) == "failed" {
		switch data["phase"] {
		case "requested", "staging", "staged", "promoting", "rolling-back":
			v["phase"] = "failed"
		}
	}
	return v
}

// Status handles GET /api/v1/sovereign/release.
func (rt *ReleaseRoute) Status(w http.ResponseWriter, r *http.Request) {
	if !SovereignAdmin(auth.ClaimsFromContext(r.Context())) {
		refuse(w, http.StatusForbidden, "forbidden", "the release state is read by a sovereign-admin (tier admin or owner)")
		return
	}
	cs, err := rt.clients()
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "cluster-unreachable", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), releaseReadTimeout)
	defer cancel()
	ns := rt.namespace()
	st, err := cs.CoreV1().ConfigMaps(ns).Get(ctx, ReleaseStatusConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		writeJSON(w, http.StatusOK, map[string]any{"phase": "none", "status": map[string]string{}})
		return
	} else if err != nil {
		refuse(w, http.StatusBadGateway, "status-read-failed", err.Error())
		return
	}
	var job *batchv1.Job
	var pods []corev1.Pod
	if name := st.Data["job"]; name != "" {
		if j, err := cs.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			job = j
			if pl, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + name}); err == nil {
				pods = pl.Items
			}
		} else if !apierrors.IsNotFound(err) {
			refuse(w, http.StatusBadGateway, "job-read-failed", err.Error())
			return
		}
	}
	// The delivery Job the candidate's mechanism runs: its step follows the
	// bootstrap's, so prewarm and promote are named while they run.
	if name := st.Data["deliveryJob"]; name != "" && job != nil {
		if pl, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + name}); err == nil {
			pods = append(pods, pl.Items...)
		}
	}
	writeJSON(w, http.StatusOK, releaseView(st.Data, job, pods))
}
