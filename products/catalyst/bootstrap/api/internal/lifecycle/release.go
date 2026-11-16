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
//     release/<id>. A merge conflict in any region names the files and moves
//     nothing. A dry run stops here and pushes nothing.
//  2. prewarm: cutover step 03 itself, reading the pins of release/<id>, copies
//     every chart and image they declare into the Sovereign's registry and stops
//     by name on any it cannot copy. Its upstream credential is the request's
//     pullSecret, which the stage copies into every follower region (step 03's
//     follower leg reads it there); when the prewarm ends, or the stage fails,
//     the Job deletes it in every region, and a failed prewarm is recorded as
//     phase failed with the step and its cause.
//  3. promote: if no region's main moved since stage, push release/<id> as main
//     in every region, a fast-forward the server checks. Never a force push.
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
)

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

func (rt *ReleaseRoute) cutoverStatus() string {
	if v := strings.TrimSpace(rt.getenv(envCutoverStatusCM)); v != "" {
		return v
	}
	return defaultCutoverStatus
}

// Request handles PUT /api/v1/sovereign/release with the body
// {"candidate": "<id>", "dryRun": false, "pullSecret": "<name>"}. pullSecret is a
// Secret of type kubernetes.io/dockerconfigjson in the cutover namespace with an
// entry for ghcr.io; a delivery needs it, a dry run does not. A delivery uses it
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
	var dep *dependencyError
	if errors.As(err, &dep) {
		refuse(w, http.StatusFailedDependency, "cutover-chart-incomplete", dep.Error())
		return
	} else if err != nil {
		refuse(w, http.StatusBadGateway, "cutover-chart-read-failed", err.Error())
		return
	}
	if !req.DryRun {
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
		followerPrewarm = "covered: step 03 mirrors the release's pins into every follower registry"
		if !in.followerPrewarm {
			followerPrewarm = "unknown until the stage compares each follower registry's storage with the primary's: the installed step 03 mirrors a follower's running set only, so a follower's registry that stores elsewhere may lack what the release pins until the release has installed a cutover chart that mirrors them"
		}
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
			"signature":       signatureUnverified + ": candidate refs are cut unsigned (scripts/release-candidate.sh); " + envReleaseSignature + "=required refuses every request until the cut signs",
		},
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
		rt.log.Info("release requested", "candidate", req.Candidate, "dryRun", req.DryRun, "job", name, "by", who)
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

// releaseView is the answer of both routes: the recorded state, and the state
// of the Job and of the step it runs now.
func releaseView(data map[string]string, job *batchv1.Job, pods []corev1.Pod) map[string]any {
	v := map[string]any{"status": data, "phase": data["phase"], "job": data["job"], "jobState": jobState(job)}
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
		case "requested", "staging", "staged", "promoting":
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
	writeJSON(w, http.StatusOK, releaseView(st.Data, job, pods))
}
