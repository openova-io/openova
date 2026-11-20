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
//     credential, with which the route signs in to the registry and which it
//     keeps in the Secret it makes for the delivery (release_credential.go);
//     the stage copies it into every follower region (step 03's follower leg
//     reads it there); every end of the delivery deletes it in every region,
//     and a failed prewarm is recorded as phase failed with the step and its
//     cause.
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
	// checkCredential signs in to the upstream registry with the request's
	// credential: nil, an *errCredentialRefused, or why it could not be asked.
	checkCredential func(context.Context, registryCredential) error
}

// NewReleaseRoute returns the route with the cluster this pod runs in.
func NewReleaseRoute(log *slog.Logger) *ReleaseRoute {
	return &ReleaseRoute{log: log, clients: jobauth.InCluster, now: time.Now, getenv: os.Getenv,
		checkCredential: registryProbe{client: &http.Client{Timeout: registryCheckDeadline}}.check}
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
// {"candidate": "<id>", "dryRun": false, "credential": {"registry": "ghcr.io",
// "username": "<user>", "password": "<password or token>"}, "region": "<region>",
// "rollback": false}; region and rollback are optional (SOV-521). A delivery
// needs the credential: the route signs in to the registry with it (a sign-in,
// not read access to the pinned packages, which the prewarm proves), and
// refuses, naming why, before anything is written; then it keeps it in the
// pull Secret it makes for the delivery (release_credential.go). A dry run and
// a rollback copy nothing from the registry, so they refuse a credential: it
// would travel for nothing. The field pullSecret, a Secret a sovereign-admin
// made by hand, is removed and refused.
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
	if req.RemovedPullSecret != nil {
		refuse(w, http.StatusBadRequest, "pull-secret-removed",
			`pullSecret is removed: the route makes the delivery's pull Secret itself, so nothing is made by hand on the Sovereign. Send the upstream registry's credential in the request instead, as credential {"registry": "`+upstreamRegistry+`", "username": "<user>", "password": "<password or token>"}; nothing was staged`)
		return
	}
	if !candidateID.MatchString(req.Candidate) {
		refuse(w, http.StatusBadRequest, "bad-candidate", "candidate is the id of a release candidate ref rc/<id>: <yyyymmddThhmmssZ>-<first 7 hex of the commit>")
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
		case req.Credential != nil:
			refuse(w, http.StatusBadRequest, "rollback-credential",
				"a rollback runs no prewarm and copies no chart or image: leave credential out of a rollback")
			return
		}
	}
	if req.DryRun && req.Credential != nil {
		refuse(w, http.StatusBadRequest, "dry-run-credential",
			"a dry run copies nothing from the registry and uses no credential: leave credential out of a dry run, and send it with the delivery")
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
	// A pull Secret an earlier delivery left (its Job killed before it could
	// delete it) is removed when the next request arrives: no Job runs.
	if removed, err := removeReleasePullSecret(ctx, cs, ns); err != nil {
		refuse(w, http.StatusBadGateway, "credential-cleanup-failed", fmt.Sprintf("cannot remove the pull Secret %s/%s an earlier delivery left: %v", ns, releasePullSecret, err))
		return
	} else if removed && rt.log != nil {
		rt.log.Info("release: removed the pull Secret an earlier delivery left", "secret", ns+"/"+releasePullSecret)
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
		if code, why := credentialProblem(req.Credential); code != "" {
			refuse(w, http.StatusUnprocessableEntity, code, why)
			return
		}
		var refused *errCredentialRefused
		if err := rt.checkCredential(ctx, *req.Credential); errors.As(err, &refused) {
			refuse(w, http.StatusUnprocessableEntity, "registry-credential-refused",
				refused.Error()+": send a credential that may pull from "+upstreamRegistry+"; nothing was staged")
			return
		} else if err != nil {
			refuse(w, http.StatusBadGateway, "registry-unreachable",
				"cannot ask "+upstreamRegistry+" whether it takes the credential: "+err.Error()+"; nothing was staged")
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
	// The state names where the credential is held, never the credential.
	heldIn := "none: a dry run uses no credential"
	if req.Rollback {
		heldIn = "none: a rollback uses no credential"
	}
	var pull *corev1.Secret
	if len(credentialEnv(req)) > 0 {
		heldIn = "signed in to " + upstreamRegistry + " (read access to the packages the release pins is proven by its prewarm); held in the Secret " + ns + "/" + releasePullSecret + " for this delivery only, and deleted in every region when it ends"
		if pull, err = pullSecretFor(ns, name, req.Candidate, *req.Credential); err != nil {
			refuse(w, http.StatusInternalServerError, "compose-failed", err.Error())
			return
		}
	}
	status := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ReleaseStatusConfigMap, Namespace: ns, Labels: map[string]string{"app.kubernetes.io/component": releaseComponent}},
		Data: map[string]string{
			"candidate":       req.Candidate,
			"dryRun":          boolString(req.DryRun),
			"credential":      heldIn,
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
	made, err := cs.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		refuse(w, http.StatusBadGateway, "job-create-failed", err.Error())
		return
	}
	// The pull Secret after the Job, owned by it: the garbage collector deletes
	// it with the Job, which its ttlSecondsAfterFinished (releaseTTLSeconds)
	// deletes after it ends, killed or not. The stage reads it only at its end,
	// minutes on.
	if pull != nil {
		pull.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: made.Name, UID: made.UID}}
		if err := makeReleasePullSecret(ctx, cs, pull); err != nil {
			why := fmt.Sprintf("cannot make the pull Secret %s/%s: %v; the Job %s was deleted and nothing was staged", ns, releasePullSecret, err, name)
			bg := metav1.DeletePropagationBackground
			if derr := cs.BatchV1().Jobs(ns).Delete(context.WithoutCancel(ctx), name, metav1.DeleteOptions{PropagationPolicy: &bg}); derr != nil {
				why = fmt.Sprintf("cannot make the pull Secret %s/%s: %v; the Job %s could not be deleted (%v): its stage fails when it finds no pull Secret, and nothing moves", ns, releasePullSecret, err, name, derr)
			}
			status.Data["phase"], status.Data["message"] = "failed", why
			_ = putConfigMap(context.WithoutCancel(ctx), cs, status)
			refuse(w, http.StatusBadGateway, "credential-write-failed", why)
			return
		}
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
