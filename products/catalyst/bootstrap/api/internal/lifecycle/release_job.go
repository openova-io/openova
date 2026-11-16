package lifecycle

import (
	"context"
	"embed"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// The release Job reuses the cutover chart that is installed on the Sovereign:
// the image and the Gitea and upstream settings of step 01, the whole prewarm
// of step 03, the kit rewrite of step 06, the pin extractor of step 03 (03b) and,
// on a Sovereign with follower regions, the follower pivot Job of step 06. It
// copies none of their logic; it reads them where the chart put them.
const (
	mirrorStepConfigMap    = "cutover-step-01-gitea-mirror"
	prewarmStepConfigMap   = "cutover-step-03-harbor-prewarm"
	pivotStepConfigMap     = "cutover-step-06-helmrepository-patches"
	pinsConfigMap          = "cutover-bootstrap-kit-pins"
	followerPivotConfigMap = "cutover-secondary-pivot-script"
	secondaryKubeconfigs   = "cutover-secondary-kubeconfigs"

	// ReleaseStatusConfigMap holds the state of the last release request.
	ReleaseStatusConfigMap  = "sovereign-release"
	releaseScriptsConfigMap = "sovereign-release-scripts"
	releaseComponent        = "sovereign-release"
	releaseCandidateLabel   = "catalyst.openova.io/release-candidate"

	// followerPrewarmMarker is printed by cutover step 03 when its follower leg
	// also mirrors the pins of the branch it reads, so a follower region's own
	// registry holds what a release pins (bp-self-sovereign-cutover, #7922).
	followerPrewarmMarker = "also mirrors the pins of"

	// releaseGitSeconds is the budget of the stage and promote containers on
	// top of the prewarm's own deadline: a full clone of the Sovereign's
	// repository and a fetch of the candidate in every region.
	releaseGitSeconds = 7200
	releaseTTLSeconds = 3 * 24 * 3600
)

//go:embed release/release-run.sh release/release-region.sh
var releaseScripts embed.FS

// dependencyError names a part of the installed cutover chart a release needs
// and does not find. The route answers 424.
type dependencyError struct{ what string }

func (e *dependencyError) Error() string { return e.what }

// releaseInputs are the parts of the installed cutover chart a release reuses.
type releaseInputs struct {
	mirror          corev1.Container
	prewarm         corev1.PodSpec
	follower        *batchv1.Job
	followerNS      string
	followers       int
	followerPrewarm bool
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
	in.mirror = ps.Containers[0]
	for _, n := range []string{"GITEA_INTERNAL_URL", "GITEA_ORG", "GITEA_REPO", "GITEA_USERNAME", "UPSTREAM_REPO_URL", "UPSTREAM_BRANCH",
		"PAT_SOURCE_NAMESPACE", "PAT_SOURCE_SECRET_NAME", "PAT_SOURCE_KEY"} {
		if v, ok := envValue(in.mirror.Env, n); !ok || v == "" {
			return nil, &dependencyError{fmt.Sprintf("step 01 (%s) sets no %s", mirrorStepConfigMap, n)}
		}
	}

	cm, err = getConfigMap(ctx, cs, ns, prewarmStepConfigMap)
	if err != nil {
		return nil, err
	}
	if in.prewarm, err = podSpecOf(cm); err != nil {
		return nil, err
	}
	// The prewarm reads the pins of the branch UPSTREAM_BRANCH names, and its
	// upstream credential from GHCR_DOCKERCONFIG. A step 03 without both cannot
	// copy what a candidate pins.
	pw := in.prewarm.Containers[0]
	for _, n := range []string{"UPSTREAM_BRANCH", "UPSTREAM_PREFIX", "GHCR_DOCKERCONFIG"} {
		found := false
		for _, e := range pw.Env {
			found = found || e.Name == n
		}
		if !found {
			return nil, &dependencyError{fmt.Sprintf("step 03 (%s) reads no %s, so it cannot prewarm what a candidate pins", prewarmStepConfigMap, n)}
		}
	}
	in.followerPrewarm = strings.Contains(cm.Data["run.sh"], followerPrewarmMarker)

	for _, need := range []struct{ cm, key string }{{pivotStepConfigMap, "kit-rewrite.sh"}, {pinsConfigMap, "extract-pins.sh"}} {
		cm, err := getConfigMap(ctx, cs, ns, need.cm)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(cm.Data[need.key]) == "" {
			return nil, &dependencyError{fmt.Sprintf("ConfigMap %s/%s has no %s", ns, need.cm, need.key)}
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
			}
		}
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

// releaseRequest is the body of PUT /api/v1/sovereign/release.
type releaseRequest struct {
	Candidate  string `json:"candidate"`
	DryRun     bool   `json:"dryRun"`
	PullSecret string `json:"pullSecret,omitempty"`
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func plain(name, value string) corev1.EnvVar { return corev1.EnvVar{Name: name, Value: value} }

// runContainer is a stage or promote container: release-run.sh with the image,
// environment, resources and security context of cutover step 01.
func (in *releaseInputs) runContainer(name, phase, ns string, req releaseRequest) corev1.Container {
	c := *in.mirror.DeepCopy()
	c.Name = name
	c.Command = []string{"/bin/sh", "/release/release-run.sh", phase}
	c.Args = nil
	upstreamPrefix, _ := envValue(in.prewarm.Containers[0].Env, "UPSTREAM_PREFIX")
	for _, v := range []corev1.EnvVar{
		plain("HOME", "/tmp"),
		plain("CANDIDATE", req.Candidate),
		plain("DRY_RUN", boolString(req.DryRun)),
		plain("RELEASE_NAMESPACE", ns),
		plain("RELEASE_STATUS_CM", ReleaseStatusConfigMap),
		plain("RELEASE_SCRIPTS", "/release"),
		plain("STATE", "/release-state"),
		plain("UPSTREAM_PREFIX", upstreamPrefix),
		plain("KIT_REWRITE", "/kit-rewrite/kit-rewrite.sh"),
		plain("PIN_EXTRACTOR", "/pin-extractor/extract-pins.sh"),
		plain("SECONDARY_KUBECONFIGS", "/secondary-kubeconfigs"),
		plain("SECONDARY_GITEA_NAMESPACE", in.followerNS),
		// false: the stage reads whether each follower's registry shares the
		// primary's storage (release-run.sh follower_storage).
		plain("FOLLOWER_PREWARM_COVERED", boolString(in.followerPrewarm)),
	} {
		c.Env = setEnv(c.Env, v)
	}
	c.Env = setEnvs(c.Env, in.credentialEnv(req)...)
	c.VolumeMounts = []corev1.VolumeMount{
		{Name: "release-scripts", MountPath: "/release", ReadOnly: true},
		{Name: "release-kit-rewrite", MountPath: "/kit-rewrite", ReadOnly: true},
		{Name: "release-pins", MountPath: "/pin-extractor", ReadOnly: true},
		{Name: "release-state", MountPath: "/release-state"},
		{Name: "release-secondary-kubeconfigs", MountPath: "/secondary-kubeconfigs", ReadOnly: true},
	}
	return c
}

// credentialEnv tells release-run.sh which Secrets hold the pull credential
// during a delivery: PULL_SECRET, the request's, which the stage copies into
// every follower, and the copy step 03 makes of it in each follower's harbor
// namespace. release-run.sh deletes all of them when the prewarm ends, or when
// the stage fails. A dry run names none, so it deletes none.
func (in *releaseInputs) credentialEnv(req releaseRequest) []corev1.EnvVar {
	if req.DryRun || req.PullSecret == "" {
		return nil
	}
	out := []corev1.EnvVar{plain("PULL_SECRET", req.PullSecret)}
	for _, n := range []string{"SECONDARY_HARBOR_NAMESPACE", "SECONDARY_GHCR_DEST_SECRET"} {
		if v, ok := envValue(in.prewarm.Containers[0].Env, n); ok && v != "" {
			out = append(out, plain(n, v))
		}
	}
	return out
}

// setEnvs is setEnv for several variables.
func setEnvs(env []corev1.EnvVar, vs ...corev1.EnvVar) []corev1.EnvVar {
	for _, v := range vs {
		env = setEnv(env, v)
	}
	return env
}

// prewarmContainer is cutover step 03 itself, reading the pins of the staged
// branch release/<candidate> and the upstream credential the request names.
// Post-cutover the Sovereign holds no upstream registry credential: step 06
// strips it, so the sovereign-admin names one for this run.
//
// Step 03's follower leg reads that credential in each FOLLOWER region, as
// SECONDARY_GHCR_SOURCE_NAMESPACE/SECRET through the follower's kubeconfig
// (03-harbor-prewarm-job.yaml, step (2) of the #6764 leg); the stage copies it
// there. Step 03's own command runs under "release-run.sh prewarm", which
// deletes the credential in every region when step 03 ends and, when it
// fails, writes the failure and its cause into the release state.
func (in *releaseInputs) prewarmContainer(ns string, req releaseRequest) (corev1.Container, error) {
	c := *in.prewarm.Containers[0].DeepCopy()
	c.Name = "prewarm"
	c.Env = setEnv(c.Env, plain("UPSTREAM_BRANCH", "release/"+req.Candidate))
	c.Env = setEnv(c.Env, corev1.EnvVar{Name: "GHCR_DOCKERCONFIG", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: req.PullSecret}, Key: corev1.DockerConfigJsonKey}}})
	c.Env = setEnv(c.Env, plain("GHCR_DOCKERCONFIG_NAMESPACE", ns))
	if _, ok := envValue(c.Env, "SECONDARY_GHCR_SOURCE_SECRET"); ok {
		c.Env = setEnv(c.Env, plain("SECONDARY_GHCR_SOURCE_NAMESPACE", ns))
		c.Env = setEnv(c.Env, plain("SECONDARY_GHCR_SOURCE_SECRET", req.PullSecret))
	}
	if len(c.Command) == 0 {
		return c, &dependencyError{fmt.Sprintf("step 03 (%s) names no command, so the release cannot run it", prewarmStepConfigMap)}
	}
	c.Args = append(append([]string{}, c.Command...), c.Args...)
	c.Command = []string{"/bin/sh", "/release/release-run.sh", "prewarm"}
	c.Env = setEnvs(c.Env,
		plain("CANDIDATE", req.Candidate),
		plain("DRY_RUN", boolString(req.DryRun)),
		plain("RELEASE_NAMESPACE", ns),
		plain("RELEASE_STATUS_CM", ReleaseStatusConfigMap),
		plain("RELEASE_SCRIPTS", "/release"),
		plain("STATE", "/release-state"),
		plain("SECONDARY_KUBECONFIGS", "/release-secondary-kubeconfigs"))
	c.Env = setEnvs(c.Env, in.credentialEnv(req)...)
	for _, m := range []corev1.VolumeMount{
		{Name: "release-scripts", MountPath: "/release", ReadOnly: true},
		{Name: "release-state", MountPath: "/release-state"},
		{Name: "release-secondary-kubeconfigs", MountPath: "/release-secondary-kubeconfigs", ReadOnly: true},
	} {
		for _, have := range c.VolumeMounts {
			if have.MountPath == m.MountPath {
				return c, &dependencyError{fmt.Sprintf("step 03 (%s) already mounts %s", prewarmStepConfigMap, m.MountPath)}
			}
		}
		c.VolumeMounts = append(c.VolumeMounts, m)
	}
	return c, nil
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

// composeRelease builds the release Job and the ConfigMap of the scripts it runs.
// A dry run is the stage container alone. A delivery runs stage and prewarm as
// init containers, in that order, and promote as the container: Kubernetes runs
// each only when the one before it succeeded, so a conflict or a missing image
// stops the run before any branch Flux follows moves.
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
	vols := append([]corev1.Volume{}, in.prewarm.Volumes...)
	for _, v := range []corev1.Volume{
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
	} {
		for _, have := range vols {
			if have.Name == v.Name {
				return nil, nil, &dependencyError{fmt.Sprintf("step 03 (%s) already has a volume named %s", prewarmStepConfigMap, v.Name)}
			}
		}
		vols = append(vols, v)
	}

	pod := corev1.PodSpec{
		ServiceAccountName: in.prewarm.ServiceAccountName,
		RestartPolicy:      corev1.RestartPolicyNever,
		SecurityContext:    in.prewarm.SecurityContext,
		ImagePullSecrets:   in.prewarm.ImagePullSecrets,
		NodeSelector:       in.prewarm.NodeSelector,
		Tolerations:        in.prewarm.Tolerations,
		Affinity:           in.prewarm.Affinity,
		Volumes:            vols,
	}
	deadline := int64(releaseGitSeconds)
	if req.DryRun {
		pod.Containers = []corev1.Container{in.runContainer("stage", "stage", ns, req)}
	} else {
		if in.prewarm.ActiveDeadlineSeconds != nil {
			deadline += *in.prewarm.ActiveDeadlineSeconds
		}
		pw, err := in.prewarmContainer(ns, req)
		if err != nil {
			return nil, nil, err
		}
		pod.InitContainers = []corev1.Container{in.runContainer("stage", "stage", ns, req), pw}
		pod.Containers = []corev1.Container{in.runContainer("promote", "promote", ns, req)}
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
