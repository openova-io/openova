package lifecycle

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// The release route composes only the BOOTSTRAP of a delivery (#7938): a Job
// whose stage container stages every region and renders the candidate's own
// cutover chart, and whose deliver container runs that chart's release scripts
// (release-run.sh compose), which build and run the delivery Job: the
// candidate's step 03 as prewarm, then promote. The installed step 03 is never
// read, so a repair of the mechanism acts on the delivery that carries it.
//
// From the installed cutover chart the bootstrap reads only what staging needs:
// the image and the Gitea and upstream settings of step 01, the kit rewrite and
// UPSTREAM_PREFIX of step 06, the pin extractor (03b) and, on a Sovereign with
// follower regions, the follower pivot Job of step 06. Its contract with a
// chart version: platform/self-sovereign-cutover/chart/README.md, "Release
// mechanism".
const (
	mirrorStepConfigMap    = "cutover-step-01-gitea-mirror"
	pivotStepConfigMap     = "cutover-step-06-helmrepository-patches"
	pinsConfigMap          = "cutover-bootstrap-kit-pins"
	followerPivotConfigMap = "cutover-secondary-pivot-script"
	secondaryKubeconfigs   = "cutover-secondary-kubeconfigs"

	// ReleaseStatusConfigMap holds the state of the last release request.
	ReleaseStatusConfigMap  = "sovereign-release"
	releaseScriptsConfigMap = "sovereign-release-scripts"
	releaseComponent        = "sovereign-release"
	releaseCandidateLabel   = "catalyst.openova.io/release-candidate"

	// defaultCutoverHelmRelease is the HelmRelease of the installed cutover
	// chart (bootstrap-kit slot 06a), whose values the candidate's copy of the
	// chart is rendered with.
	envCutoverHelmRelease     = "CATALYST_CUTOVER_HELMRELEASE"
	defaultCutoverHelmRelease = "flux-system/bp-self-sovereign-cutover"

	// releaseGitSeconds is the budget of the stage: a full clone of the
	// Sovereign's repository and a fetch of the candidate in every region, and
	// the render. releaseDeliverySeconds is what the deliver container waits
	// for the delivery Job: the candidate's step 03 and the promote.
	releaseGitSeconds      = 7200
	releaseDeliverySeconds = 6 * 3600
	releaseTTLSeconds      = 3 * 24 * 3600
)

//go:embed release/release-run.sh release/release-region.sh
var releaseScripts embed.FS

// dependencyError names a part of the installed cutover chart a release needs
// and does not find. The route answers 424.
type dependencyError struct{ what string }

func (e *dependencyError) Error() string { return e.what }

// releaseInputs are the parts of the installed cutover chart the bootstrap uses.
type releaseInputs struct {
	mirrorPod      corev1.PodSpec
	mirror         corev1.Container
	upstreamPrefix string
	follower       *batchv1.Job
	followerNS     string
	followers      int
	followerNames  []string // the follower regions, as the state names them
	cutoverRelease string   // <namespace>/<name> of the cutover HelmRelease
	// promotedFrom and promotedTo are what the promote of the region a
	// rollback names recorded (SOV-521): the main of the stage and the commit
	// pushed.
	promotedFrom, promotedTo string
}

// hasRegion reports whether name is a region of the Sovereign as the state
// ConfigMap names it: primary, or a follower's name.
func (in *releaseInputs) hasRegion(name string) bool {
	if name == primaryRegion {
		return true
	}
	for _, f := range in.followerNames {
		if f == name {
			return true
		}
	}
	return false
}

func podSpecOf(cm *corev1.ConfigMap) (corev1.PodSpec, error) {
	var ps corev1.PodSpec
	raw := cm.Data["podSpec"]
	if strings.TrimSpace(raw) == "" {
		return ps, &dependencyError{fmt.Sprintf("ConfigMap %s has no podSpec", cm.Name)}
	}
	if err := yaml.Unmarshal([]byte(raw), &ps); err != nil {
		return ps, &dependencyError{fmt.Sprintf("ConfigMap %s: the podSpec does not parse: %v", cm.Name, err)}
	}
	if len(ps.Containers) == 0 {
		return ps, &dependencyError{fmt.Sprintf("ConfigMap %s: the podSpec has no container", cm.Name)}
	}
	return ps, nil
}

func envValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// setEnv replaces the variable name, or appends it, keeping the order.
func setEnv(env []corev1.EnvVar, v corev1.EnvVar) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == v.Name {
			env[i] = v
			return env
		}
	}
	return append(env, v)
}

func dropEnv(env []corev1.EnvVar, names ...string) []corev1.EnvVar {
	out := env[:0]
	for _, e := range env {
		keep := true
		for _, n := range names {
			keep = keep && e.Name != n
		}
		if keep {
			out = append(out, e)
		}
	}
	return out
}

func getConfigMap(ctx context.Context, cs kubernetes.Interface, ns, name string) (*corev1.ConfigMap, error) {
	cm, err := cs.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, &dependencyError{fmt.Sprintf("ConfigMap %s/%s is not installed (bp-self-sovereign-cutover)", ns, name)}
	}
	return cm, err
}

// readReleaseInputs reads the parts of the installed cutover chart a release
// reuses from namespace ns. A missing part is a *dependencyError.
func readReleaseInputs(ctx context.Context, cs kubernetes.Interface, ns string) (*releaseInputs, error) {
	in := &releaseInputs{}
	cm, err := getConfigMap(ctx, cs, ns, mirrorStepConfigMap)
	if err != nil {
		return nil, err
	}
	ps, err := podSpecOf(cm)
	if err != nil {
		return nil, err
	}
	in.mirrorPod, in.mirror = ps, ps.Containers[0]
	for _, n := range []string{"GITEA_INTERNAL_URL", "GITEA_ORG", "GITEA_REPO", "GITEA_USERNAME", "UPSTREAM_REPO_URL", "UPSTREAM_BRANCH",
		"PAT_SOURCE_NAMESPACE", "PAT_SOURCE_SECRET_NAME", "PAT_SOURCE_KEY"} {
		if v, ok := envValue(in.mirror.Env, n); !ok || v == "" {
			return nil, &dependencyError{fmt.Sprintf("step 01 (%s) sets no %s", mirrorStepConfigMap, n)}
		}
	}

	for _, need := range []struct{ cm, key string }{{pivotStepConfigMap, "kit-rewrite.sh"}, {pinsConfigMap, "extract-pins.sh"}} {
		cm, err := getConfigMap(ctx, cs, ns, need.cm)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(cm.Data[need.key]) == "" {
			return nil, &dependencyError{fmt.Sprintf("ConfigMap %s/%s has no %s", ns, need.cm, need.key)}
		}
		if need.cm != pivotStepConfigMap {
			continue
		}
		// The prefix the stage pivots away from is step 06's: the step that
		// pivots the kit. Step 03 is not read.
		pivot, err := podSpecOf(cm)
		if err != nil {
			return nil, err
		}
		if in.upstreamPrefix, _ = envValue(pivot.Containers[0].Env, "UPSTREAM_PREFIX"); in.upstreamPrefix == "" {
			return nil, &dependencyError{fmt.Sprintf("step 06 (%s) sets no UPSTREAM_PREFIX", pivotStepConfigMap)}
		}
	}

	sec, err := cs.CoreV1().Secrets(ns).Get(ctx, secondaryKubeconfigs, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, err
	default:
		for k, v := range sec.Data {
			if strings.HasSuffix(k, ".yaml") && len(v) > 0 {
				in.followers++
				in.followerNames = append(in.followerNames, strings.TrimSuffix(k, ".yaml"))
			}
		}
		sort.Strings(in.followerNames)
	}
	if in.followers > 0 {
		cm, err := getConfigMap(ctx, cs, ns, followerPivotConfigMap)
		if err != nil {
			return nil, &dependencyError{fmt.Sprintf("%d follower region(s) in Secret %s, but %v", in.followers, secondaryKubeconfigs, err)}
		}
		var job batchv1.Job
		if err := yaml.Unmarshal([]byte(cm.Data["secondary-pivot-job.yaml"]), &job); err != nil || len(job.Spec.Template.Spec.Containers) == 0 || job.Namespace == "" {
			return nil, &dependencyError{fmt.Sprintf("ConfigMap %s/%s carries no usable secondary-pivot-job.yaml", ns, followerPivotConfigMap)}
		}
		in.follower, in.followerNS = &job, job.Namespace
	}
	return in, nil
}

// releaseRequest is the body of PUT /api/v1/sovereign/release. Region names
// the one region a delivery promotes, or a rollback puts back (SOV-521).
type releaseRequest struct {
	Candidate  string `json:"candidate"`
	DryRun     bool   `json:"dryRun"`
	PullSecret string `json:"pullSecret,omitempty"`
	Region     string `json:"region,omitempty"`
	Rollback   bool   `json:"rollback,omitempty"`
}

// primaryRegion is the name of the primary region in the state ConfigMap.
const primaryRegion = "primary"

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func plain(name, value string) corev1.EnvVar { return corev1.EnvVar{Name: name, Value: value} }

// runContainer is a container of the bootstrap: release-run.sh <phase> (stage,
// deliver or rollback) with the image, environment, resources and security
// context of cutover step 01. A whole delivery sets no RELEASE_REGION.
func (in *releaseInputs) runContainer(name, phase, ns, job string, req releaseRequest) corev1.Container {
	c := *in.mirror.DeepCopy()
	c.Name = name
	c.Command = []string{"/bin/sh", "/release/release-run.sh", phase}
	c.Args = nil
	for _, v := range []corev1.EnvVar{
		plain("HOME", "/tmp"),
		plain("CANDIDATE", req.Candidate),
		plain("DRY_RUN", boolString(req.DryRun)),
		plain("RELEASE_NAMESPACE", ns),
		plain("RELEASE_STATUS_CM", ReleaseStatusConfigMap),
		plain("RELEASE_SCRIPTS", "/release"),
		plain("RELEASE_JOB", job),
		plain("STATE", "/release-state"),
		plain("UPSTREAM_PREFIX", in.upstreamPrefix),
		plain("KIT_REWRITE", "/kit-rewrite/kit-rewrite.sh"),
		plain("PIN_EXTRACTOR", "/pin-extractor/extract-pins.sh"),
		plain("SECONDARY_KUBECONFIGS", "/secondary-kubeconfigs"),
		plain("SECONDARY_GITEA_NAMESPACE", in.followerNS),
		plain("CUTOVER_HELMRELEASE", in.cutoverRelease),
	} {
		c.Env = setEnv(c.Env, v)
	}
	c.Env = setEnvs(c.Env, credentialEnv(req)...)
	if req.Region != "" {
		c.Env = setEnv(c.Env, plain("RELEASE_REGION", req.Region))
	}
	if req.Rollback {
		c.Env = setEnvs(c.Env, plain("PROMOTED_FROM", in.promotedFrom), plain("PROMOTED_TO", in.promotedTo))
	}
	c.VolumeMounts = []corev1.VolumeMount{
		{Name: "release-scripts", MountPath: "/release", ReadOnly: true},
		{Name: "release-kit-rewrite", MountPath: "/kit-rewrite", ReadOnly: true},
		{Name: "release-pins", MountPath: "/pin-extractor", ReadOnly: true},
		{Name: "release-state", MountPath: "/release-state"},
		{Name: "release-secondary-kubeconfigs", MountPath: "/secondary-kubeconfigs", ReadOnly: true},
	}
	return c
}

// credentialEnv names the request's pull credential, which the stage copies
// into every follower and the candidate's prewarm deletes in every region when
// it ends (the bootstrap deletes it when the stage or the deliver container
// fails first). A dry run names none, so it deletes none.
func credentialEnv(req releaseRequest) []corev1.EnvVar {
	if req.DryRun || req.PullSecret == "" {
		return nil
	}
	return []corev1.EnvVar{plain("PULL_SECRET", req.PullSecret)}
}

// setEnvs is setEnv for several variables.
func setEnvs(env []corev1.EnvVar, vs ...corev1.EnvVar) []corev1.EnvVar {
	for _, v := range vs {
		env = setEnv(env, v)
	}
	return env
}

// followerJobManifest is the follower pivot Job of the installed cutover chart,
// turned into one phase of release-region.sh. release-run.sh replaces __JOB__,
// __REGION__, __PHASE__, __MAIN_SHA__ and __STAGED_SHA__ and applies it in each
// follower region.
func (in *releaseInputs) followerJobManifest(req releaseRequest) (string, error) {
	j := in.follower.DeepCopy()
	j.Name = "__JOB__"
	j.ResourceVersion, j.UID = "", ""
	if j.Labels == nil {
		j.Labels = map[string]string{}
	}
	j.Labels["app.kubernetes.io/component"] = releaseComponent
	j.Labels[releaseCandidateLabel] = req.Candidate
	j.Labels["catalyst.openova.io/secondary-region"] = "__REGION__"
	backoff, deadline, ttl := int32(0), int64(releaseGitSeconds), int32(releaseTTLSeconds)
	j.Spec.BackoffLimit, j.Spec.ActiveDeadlineSeconds, j.Spec.TTLSecondsAfterFinished = &backoff, &deadline, &ttl
	if j.Spec.Template.Labels == nil {
		j.Spec.Template.Labels = map[string]string{}
	}
	j.Spec.Template.Labels["app.kubernetes.io/component"] = releaseComponent
	pod := &j.Spec.Template.Spec
	c := &pod.Containers[0]
	c.Name = "release"
	c.Command, c.Args = []string{"/bin/sh", "/release/release-region.sh"}, nil
	upstreamRepo, _ := envValue(in.mirror.Env, "UPSTREAM_REPO_URL")
	c.Env = dropEnv(c.Env, "LOCAL_PREFIX", "TRUST_SECRET")
	for _, v := range []corev1.EnvVar{
		plain("HOME", "/tmp"),
		plain("REGION", "__REGION__"),
		plain("PHASE", "__PHASE__"),
		plain("CANDIDATE", req.Candidate),
		plain("DRY_RUN", boolString(req.DryRun)),
		plain("MAIN_SHA", "__MAIN_SHA__"),
		plain("STAGED_SHA", "__STAGED_SHA__"),
		plain("UPSTREAM_REPO_URL", upstreamRepo),
		plain("KIT_REWRITE", "/release/kit-rewrite.sh"),
		plain("PIN_EXTRACTOR", "/release/extract-pins.sh"),
	} {
		c.Env = setEnv(c.Env, v)
	}
	c.VolumeMounts = []corev1.VolumeMount{{Name: "release", MountPath: "/release", ReadOnly: true}}
	mode := int32(0o755)
	pod.Volumes = []corev1.Volume{{Name: "release", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: releaseScriptsConfigMap}, DefaultMode: &mode}}}}
	b, err := yaml.Marshal(j)
	return string(b), err
}

// composeRelease builds the bootstrap Job and the ConfigMap of the scripts it
// runs. A dry run is the stage container alone: it stages every region in a
// scratch copy and renders the candidate's mechanism, and pushes nothing. A
// delivery runs the stage as the init container and deliver as the container,
// which runs the candidate's release-run.sh compose: Kubernetes starts deliver
// only when the stage succeeded, and the delivery Job it runs promotes only
// when its prewarm succeeded. A rollback (SOV-521) is the rollback container
// alone: no stage, no prewarm.
func composeRelease(in *releaseInputs, req releaseRequest, ns, name string, labels, annotations map[string]string) (*batchv1.Job, *corev1.ConfigMap, error) {
	scripts := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: releaseScriptsConfigMap, Namespace: ns, Labels: map[string]string{
			"app.kubernetes.io/component": releaseComponent}},
		Data: map[string]string{},
	}
	for _, f := range []string{"release-run.sh", "release-region.sh"} {
		b, err := releaseScripts.ReadFile("release/" + f)
		if err != nil {
			return nil, nil, err
		}
		scripts.Data[f] = string(b)
	}
	if in.follower != nil {
		m, err := in.followerJobManifest(req)
		if err != nil {
			return nil, nil, err
		}
		scripts.Data["follower-job.yaml"] = m
	}

	mode := int32(0o755)
	optional := true
	vols := []corev1.Volume{
		{Name: "release-scripts", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: releaseScriptsConfigMap}, DefaultMode: &mode}}},
		{Name: "release-kit-rewrite", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: pivotStepConfigMap},
			Items:                []corev1.KeyToPath{{Key: "kit-rewrite.sh", Path: "kit-rewrite.sh"}}}}},
		{Name: "release-pins", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: pinsConfigMap},
			Items:                []corev1.KeyToPath{{Key: "extract-pins.sh", Path: "extract-pins.sh"}}}}},
		{Name: "release-state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "release-secondary-kubeconfigs", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: secondaryKubeconfigs, Optional: &optional}}},
	}
	pod := corev1.PodSpec{
		ServiceAccountName: in.mirrorPod.ServiceAccountName,
		RestartPolicy:      corev1.RestartPolicyNever,
		SecurityContext:    in.mirrorPod.SecurityContext,
		ImagePullSecrets:   in.mirrorPod.ImagePullSecrets,
		NodeSelector:       in.mirrorPod.NodeSelector,
		Tolerations:        in.mirrorPod.Tolerations,
		Affinity:           in.mirrorPod.Affinity,
		Volumes:            vols,
	}
	deadline := int64(releaseGitSeconds)
	switch {
	case req.Rollback:
		pod.Containers = []corev1.Container{in.runContainer("rollback", "rollback", ns, name, req)}
	case req.DryRun:
		pod.Containers = []corev1.Container{in.runContainer("stage", "stage", ns, name, req)}
	default:
		deadline += releaseDeliverySeconds
		pod.InitContainers = []corev1.Container{in.runContainer("stage", "stage", ns, name, req)}
		pod.Containers = []corev1.Container{in.runContainer("deliver", "deliver", ns, name, req)}
	}
	backoff, ttl := int32(0), int32(releaseTTLSeconds)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, Annotations: annotations},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template:                corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: pod},
		},
	}
	return job, scripts, nil
}
