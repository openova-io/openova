package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/applyconfigurations"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/yaml"
)

// The sixth delivery to a Sovereign was refused by the API server (#7938):
//
//	The Job "sovereign-release-1794999659-delivery" is invalid:
//	* spec.template.spec.volumes[8].configMap.defaultMode: Invalid value: 755
//	* spec.template.spec.initContainers[0].volumeMounts[8].name: Not found
//
// Step 03 writes that volume's mode 0755. Kubernetes reads YAML as YAML 1.1
// (sigs.k8s.io/yaml), where 0755 is octal, 493; the delivery read step 03's pod
// spec with yq, a YAML 1.2 reader, where it is the decimal 755, above 0777. The
// tests before this file replayed the scripts against a stub kubectl, which
// neither parses nor validates what it is sent, so nothing built the object.
//
// These tests build every object a delivery creates from the cutover chart of
// this checkout, by the delivery's own code: the stage of a dry run renders the
// chart and runs its release-run.sh verify, which composes every object and
// sends each as a server-side dry run; the stub keeps what it is sent. Then:
//
//	(a) every object is checked offline as the API server would: the schema of
//	    its kind (client-go's copy of the Kubernetes OpenAPI schema, the one
//	    server-side apply uses), a strict decode into its Go type, and the pod
//	    rules the sixth delivery was refused by;
//	(b) the delivery Job's pod is step 03's pod, as Kubernetes reads the render,
//	    apart from what compose sets on purpose; the same for the Jobs of the
//	    follower regions against the Jobs the render carries;
//	(c) the reader the delivery uses (k8s_json) agrees with sigs.k8s.io/yaml on
//	    every YAML document it reads.
//
// Each check is shown to fail on the reader the sixth delivery used.

// The values of a two-region Sovereign's cutover HelmRelease (kit slot 06a:
// replicaRegion and prewarmSecondaryHarbor), so the render holds the follower
// legs the delivery composes from.
var realChartValues = map[string]any{
	"sovereign":        map[string]any{"fqdn": "t01.omani.works"},
	"secondaryRegions": map[string]any{"prewarmSecondaryHarbor": true, "replicaRegion": "region-b"},
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o750)
		}
		b, err := os.ReadFile(filepath.Clean(p))
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// realCutoverRelease is laterRelease with this checkout's cutover chart, and
// the kit slot that pins it.
func (f *fixture) realCutoverRelease(w string) {
	f.t.Helper()
	f.laterRelease(w)
	dst := filepath.Join(w, cutoverChartPath)
	if err := os.RemoveAll(dst); err != nil {
		f.t.Fatal(err)
	}
	copyTree(f.t, filepath.Join(repoRoot(f.t), cutoverChartPath), dst)
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dst, "Chart.yaml")))
	if err != nil {
		f.t.Fatal(err)
	}
	var c struct{ Name, Version string }
	if err := yaml.Unmarshal(b, &c); err != nil || c.Name == "" || c.Version == "" {
		f.t.Fatalf("Chart.yaml of the cutover chart: %v", err)
	}
	f.write(w, cutoverSlotFile, slot(c.Name, c.Version, upstreamPrefix))
}

// realChartDryRun runs the stage of a dry run of a candidate that carries this
// checkout's cutover chart, to a two-region Sovereign: it renders the chart
// and runs the chart's own verify.
func realChartDryRun(t *testing.T) (*runFixture, releaseRequest) {
	t.Helper()
	f := newFixture(t)
	id, _ := f.candidate(1, f.realCutoverRelease)
	req := releaseRequest{Candidate: id, DryRun: true}
	r := newRunFixtureFrom(t, f, req, false)
	hr, _ := json.Marshal(map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "bp-self-sovereign-cutover", "namespace": "flux-system"},
		"spec":     map[string]any{"releaseName": "self-sovereign-cutover", "targetNamespace": testNS, "values": realChartValues}})
	r.write(r.stub, "cutover-hr.json", string(hr))
	out, code := r.run("stage", req)
	st := r.status()
	if code != 0 || st["phase"] != "dry-run" || !strings.HasPrefix(st["serverDryRun"], "passed: ") {
		t.Fatalf("the dry run of the real cutover chart: exit %d, phase %q, serverDryRun %q, message %q\n%s", code, st["phase"], st["serverDryRun"], st["message"], out)
	}
	return r, req
}

// sentObjects are the objects the stub was sent as server-side dry runs, by
// <region>.<kind>.<name>, decoded as the API server decodes JSON (integers
// stay integers).
func (r *runFixture) sentObjects() map[string]map[string]any {
	r.t.Helper()
	files, _ := filepath.Glob(filepath.Join(r.stub, "dry", "*.json"))
	out := map[string]map[string]any{}
	for _, p := range files {
		b, err := os.ReadFile(filepath.Clean(p))
		if err != nil {
			r.t.Fatal(err)
		}
		var o map[string]any
		if err := utiljson.Unmarshal(b, &o); err != nil {
			r.t.Fatalf("%s is not JSON: %v", filepath.Base(p), err)
		}
		out[strings.TrimSuffix(filepath.Base(p), ".json")] = o
	}
	return out
}

var (
	typeConverter = applyconfigurations.NewTypeConverter(scheme.Scheme)
	strictDecoder = serializer.NewCodecFactory(scheme.Scheme, serializer.EnableStrict).UniversalDeserializer()
)

// objectProblems is what the API server would refuse of obj, checked offline.
func objectProblems(obj map[string]any) []string {
	var out []string
	u := &unstructured.Unstructured{Object: obj}
	if _, err := typeConverter.ObjectToTyped(u); err != nil {
		out = append(out, "schema: "+strings.ReplaceAll(err.Error(), "\n", " "))
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return append(out, err.Error())
	}
	typed, _, err := strictDecoder.Decode(b, nil, nil)
	if err != nil {
		return append(out, "decode: "+err.Error())
	}
	name := u.GetName()
	if name == "" {
		name = u.GetGenerateName() + "x"
	}
	for _, e := range validation.IsDNS1123Subdomain(name) {
		out = append(out, "metadata.name: "+e)
	}
	switch o := typed.(type) {
	case *batchv1.Job:
		if len(name) > validation.DNS1123LabelMaxLength {
			out = append(out, "metadata.name: a Job's name is a label value of its Pods, at most 63 characters")
		}
		out = append(out, podProblems("spec.template.spec", &o.Spec.Template.Spec)...)
	case *corev1.ConfigMap:
		for k := range o.Data {
			for _, e := range validation.IsConfigMapKey(k) {
				out = append(out, "data["+k+"]: "+e)
			}
		}
	}
	return out
}

// podProblems are the pod rules of the API server's validation that the sixth
// delivery was refused by, and their neighbours: a file mode is 0 to 0777
// (octal); volume and container names are unique; a volume mount names a
// volume of the pod.
func podProblems(at string, ps *corev1.PodSpec) []string {
	var out []string
	mode := func(where string, m *int32) {
		if m != nil && (*m < 0 || *m > 0o777) {
			out = append(out, fmt.Sprintf("%s.%s: Invalid value: %d: must be a number between 0 and 0777 (octal), both inclusive", at, where, *m))
		}
	}
	items := func(where string, kp []corev1.KeyToPath) {
		for j := range kp {
			mode(fmt.Sprintf("%s.items[%d].mode", where, j), kp[j].Mode)
		}
	}
	vols := map[string]bool{}
	for i, v := range ps.Volumes {
		w := fmt.Sprintf("volumes[%d]", i)
		if vols[v.Name] {
			out = append(out, fmt.Sprintf("%s.%s.name: Duplicate value: %q", at, w, v.Name))
		}
		vols[v.Name] = true
		switch {
		case v.ConfigMap != nil:
			mode(w+".configMap.defaultMode", v.ConfigMap.DefaultMode)
			items(w+".configMap", v.ConfigMap.Items)
		case v.Secret != nil:
			mode(w+".secret.defaultMode", v.Secret.DefaultMode)
			items(w+".secret", v.Secret.Items)
		case v.Projected != nil:
			mode(w+".projected.defaultMode", v.Projected.DefaultMode)
		}
	}
	names := map[string]bool{}
	for _, set := range []struct {
		field string
		list  []corev1.Container
	}{{"initContainers", ps.InitContainers}, {"containers", ps.Containers}} {
		for i, c := range set.list {
			if names[c.Name] {
				out = append(out, fmt.Sprintf("%s.%s[%d].name: Duplicate value: %q", at, set.field, i, c.Name))
			}
			names[c.Name] = true
			for j, m := range c.VolumeMounts {
				if !vols[m.Name] {
					out = append(out, fmt.Sprintf("%s.%s[%d].volumeMounts[%d].name: Not found: %q", at, set.field, i, j, m.Name))
				}
			}
		}
	}
	return out
}

// renderedChart is the render of this checkout's cutover chart with the values
// of the dry run, read as Kubernetes reads it: the ConfigMaps by name.
func renderedChart(t *testing.T) map[string]corev1.ConfigMap {
	t.Helper()
	vals, _ := json.Marshal(realChartValues)
	vf := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(vf, vals, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("helm", "template", "self-sovereign-cutover", filepath.Join(repoRoot(t), cutoverChartPath), //nolint:gosec // the chart of this repository
		"--namespace", testNS, "-f", vf).Output()
	if err != nil {
		t.Fatalf("helm template %s: %v", cutoverChartPath, err)
	}
	cms := map[string]corev1.ConfigMap{}
	for _, part := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(out), -1) {
		var cm corev1.ConfigMap
		if err := yaml.Unmarshal([]byte(part), &cm); err != nil {
			t.Fatalf("the render does not parse: %v", err)
		}
		if cm.Kind == "ConfigMap" {
			cms[cm.Name] = cm
		}
	}
	return cms
}

func renderedPod(t *testing.T, cms map[string]corev1.ConfigMap, name string) corev1.PodSpec {
	t.Helper()
	var ps corev1.PodSpec
	if err := yaml.Unmarshal([]byte(cms[name].Data["podSpec"]), &ps); err != nil || len(ps.Containers) == 0 {
		t.Fatalf("%s: the rendered podSpec does not parse: %v", name, err)
	}
	return ps
}

func renderedJob(t *testing.T, cms map[string]corev1.ConfigMap, cm, key string, subst ...string) batchv1.Job {
	t.Helper()
	text := cms[cm].Data[key]
	for i := 0; i+1 < len(subst); i += 2 {
		text = strings.ReplaceAll(text, subst[i], subst[i+1])
	}
	var j batchv1.Job
	if err := yaml.Unmarshal([]byte(text), &j); err != nil || len(j.Spec.Template.Spec.Containers) == 0 {
		t.Fatalf("%s %s does not parse: %v", cm, key, err)
	}
	return j
}

func typedJob(t *testing.T, o map[string]any) *batchv1.Job {
	t.Helper()
	b, _ := json.Marshal(o)
	typed, _, err := strictDecoder.Decode(b, nil, nil)
	if err != nil {
		t.Fatalf("not a Job: %v", err)
	}
	j, ok := typed.(*batchv1.Job)
	if !ok {
		t.Fatalf("not a Job: %T", typed)
	}
	return j
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

// withoutEnv is env without the variables named.
func withoutEnv(env []corev1.EnvVar, names map[string]bool) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, e := range env {
		if !names[e.Name] {
			out = append(out, e)
		}
	}
	return out
}

func nameSet(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// What compose sets on step 03's container (prewarm) and step 01's (promote).
var (
	composeCommon = []string{"CANDIDATE", "DRY_RUN", "RELEASE_NAMESPACE", "RELEASE_STATUS_CM", "RELEASE_SCRIPTS", "STATE", "STATE_SEED",
		"RELEASE_JOB", "PULL_SECRET", "SECONDARY_HARBOR_NAMESPACE", "SECONDARY_GHCR_DEST_SECRET"}
	prewarmSets = nameSet(append([]string{"UPSTREAM_BRANCH", "GHCR_DOCKERCONFIG", "GHCR_DOCKERCONFIG_NAMESPACE",
		"SECONDARY_GHCR_SOURCE_NAMESPACE", "SECONDARY_GHCR_SOURCE_SECRET", "SECONDARY_KUBECONFIGS"}, composeCommon...)...)
	promoteSets = nameSet(append([]string{"HOME", "UPSTREAM_PREFIX", "KIT_REWRITE", "PIN_EXTRACTOR", "SECONDARY_KUBECONFIGS",
		"SECONDARY_GITEA_NAMESPACE"}, composeCommon...)...)
)

// againstRender lists where the delivery Job's pod differs from the rendered
// pods of step 03 and step 01 it is composed from, both read as Kubernetes
// reads them, apart from what compose sets on purpose: the names and commands
// of its containers, the variables above, the release-* volumes and mounts,
// the -release names of the ConfigMaps it mounts, promote's volume mounts, and
// the deadline (step 03's plus 7200 s).
func againstRender(job *batchv1.Job, s3, s1 corev1.PodSpec) []string {
	var d []string
	diff := func(what string, got, want any) {
		if !equality.Semantic.DeepEqual(got, want) {
			d = append(d, fmt.Sprintf("%s: the delivery Job has %s, the render %s", what, js(got), js(want)))
		}
	}
	pod := job.Spec.Template.Spec
	var vols []corev1.Volume
	for _, v := range pod.Volumes {
		if strings.HasPrefix(v.Name, "release-") {
			continue
		}
		v := *v.DeepCopy()
		if v.ConfigMap != nil {
			v.ConfigMap.Name = strings.TrimSuffix(v.ConfigMap.Name, "-release")
		}
		vols = append(vols, v)
	}
	if len(vols) != len(s3.Volumes) {
		diff("spec.template.spec.volumes (step 03's)", vols, s3.Volumes)
	} else {
		for i := range vols {
			diff(fmt.Sprintf("spec.template.spec.volumes[%d] (%s)", i, s3.Volumes[i].Name), vols[i], s3.Volumes[i])
		}
	}
	diff("serviceAccountName", pod.ServiceAccountName, s3.ServiceAccountName)
	diff("securityContext", pod.SecurityContext, s3.SecurityContext)
	diff("imagePullSecrets", pod.ImagePullSecrets, s3.ImagePullSecrets)
	diff("nodeSelector", pod.NodeSelector, s3.NodeSelector)
	diff("tolerations", pod.Tolerations, s3.Tolerations)
	diff("affinity", pod.Affinity, s3.Affinity)
	if s3.ActiveDeadlineSeconds != nil {
		diff("activeDeadlineSeconds", job.Spec.ActiveDeadlineSeconds, ptrTo(*s3.ActiveDeadlineSeconds+7200))
	}
	if len(pod.InitContainers) != 1 || len(pod.Containers) != 1 {
		return append(d, fmt.Sprintf("the delivery Job has %d init containers and %d containers, want 1 and 1", len(pod.InitContainers), len(pod.Containers)))
	}

	p, w := pod.InitContainers[0].DeepCopy(), s3.Containers[0].DeepCopy()
	diff("prewarm args (step 03's command)", p.Args, append(append([]string{}, w.Command...), w.Args...))
	diff("prewarm env (step 03's, but for what compose sets)", withoutEnv(p.Env, prewarmSets), withoutEnv(w.Env, prewarmSets))
	var mounts []corev1.VolumeMount
	for _, m := range p.VolumeMounts {
		if !strings.HasPrefix(m.Name, "release-") {
			mounts = append(mounts, m)
		}
	}
	diff("prewarm volumeMounts (step 03's)", mounts, w.VolumeMounts)
	p.Name, p.Command, p.Args, p.Env, p.VolumeMounts = w.Name, w.Command, w.Args, w.Env, w.VolumeMounts
	diff("prewarm (step 03's container)", p, w)

	q, w1 := pod.Containers[0].DeepCopy(), s1.Containers[0].DeepCopy()
	diff("promote env (step 01's, but for what compose sets)", withoutEnv(q.Env, promoteSets), withoutEnv(w1.Env, promoteSets))
	q.Name, q.Command, q.Args, q.Env, q.VolumeMounts = w1.Name, w1.Command, w1.Args, w1.Env, w1.VolumeMounts
	diff("promote (step 01's container)", q, w1)
	return d
}

func ptrTo[T any](v T) *T { return &v }

// followerJobsAgainstRender: the Jobs the delivery creates in a follower,
// against the Jobs the render carries for it: step 03's prewarm Job as it is
// (its name is generated in a dry run), and promote's from the follower pivot
// Job, whose image, resources, security and pod settings it keeps.
func followerJobsAgainstRender(t *testing.T, sent map[string]map[string]any, cms map[string]corev1.ConfigMap) []string {
	t.Helper()
	var d []string
	diff := func(what string, got, want any) {
		if !equality.Semantic.DeepEqual(got, want) {
			d = append(d, fmt.Sprintf("%s: the delivery has %s, the render %s", what, js(got), js(want)))
		}
	}
	prewarm := renderedJob(t, cms, "cutover-secondary-harbor-prewarm-script", "secondary-harbor-prewarm-job.yaml", "__REGION__", "region-b", "__REGISTRY_PIN_IP__", "")
	pivot := renderedJob(t, cms, "cutover-secondary-pivot-script", "secondary-pivot-job.yaml")
	seen := map[string]bool{}
	for k, o := range sent {
		if !strings.HasPrefix(k, "region-b.Job.") {
			continue
		}
		j := typedJob(t, o)
		switch {
		case strings.HasPrefix(j.GenerateName, prewarm.Name+"-"):
			seen["prewarm"] = true
			diff(k+" spec", j.Spec, prewarm.Spec)
			diff(k+" labels", j.Labels, prewarm.Labels)
		case strings.HasPrefix(j.GenerateName, "sovereign-release-check-") || strings.HasPrefix(j.GenerateName, "sovereign-release-promote-"):
			seen[strings.Split(j.GenerateName, "-")[2]] = true
			got, want := j.Spec.Template.Spec, pivot.Spec.Template.Spec
			c, wc := got.Containers[0], want.Containers[0]
			diff(k+" image", c.Image, wc.Image)
			diff(k+" imagePullPolicy", c.ImagePullPolicy, wc.ImagePullPolicy)
			diff(k+" resources", c.Resources, wc.Resources)
			diff(k+" securityContext", c.SecurityContext, wc.SecurityContext)
			diff(k+" pod securityContext", got.SecurityContext, want.SecurityContext)
			diff(k+" namespace", j.Namespace, pivot.Namespace)
		default:
			d = append(d, "an unexpected Job in the follower: "+k)
		}
	}
	for _, w := range []string{"prewarm", "check", "promote"} {
		if !seen[w] {
			d = append(d, "no "+w+" Job was composed for the follower")
		}
	}
	return d
}

// deliveryChecks are (a) and (b) on what verify sent.
func deliveryChecks(t *testing.T, r *runFixture) (problems []string, sent map[string]map[string]any) {
	t.Helper()
	sent = r.sentObjects()
	keys := make([]string, 0, len(sent))
	for k := range sent {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, p := range objectProblems(sent[k]) {
			problems = append(problems, k+": "+p)
		}
	}
	job, ok := sent["primary.Job.sovereign-release-delivery"]
	if !ok {
		return append(problems, fmt.Sprintf("no delivery Job was sent; sent: %v", keys)), sent
	}
	cms := renderedChart(t)
	problems = append(problems, againstRender(typedJob(t, job), renderedPod(t, cms, step03ConfigMap), renderedPod(t, cms, mirrorStepConfigMap))...)
	problems = append(problems, followerJobsAgainstRender(t, sent, cms)...)
	return problems, sent
}

func TestReleaseDeliveryObjectsOfTheCutoverChartAreValidAndAreItsRender(t *testing.T) {
	r, req := realChartDryRun(t)
	problems, sent := deliveryChecks(t, r)
	for _, p := range problems {
		t.Error(p)
	}
	// What a delivery to two regions creates: in the primary the ConfigMaps
	// the Job mounts, its scripts, its record and the Job; in the follower the
	// two copies of the pull credential, step 03's ConfigMap and Job, and
	// promote's scripts and two Jobs.
	want := []string{"primary.Job.sovereign-release-delivery", "primary.ConfigMap.cutover-step-03-harbor-prewarm-release",
		"primary.ConfigMap.cutover-secondary-harbor-prewarm-script-release", "primary.ConfigMap.sovereign-release-delivery-release",
		"primary.ConfigMap.sovereign-release-recorded", "region-b.Secret." + DryRunPullSecretForTest, "region-b.Secret.cutover-secondary-ghcr",
		"region-b.ConfigMap.cutover-secondary-harbor-prewarm", "region-b.ConfigMap.sovereign-release-scripts"}
	for _, k := range want {
		if _, ok := sent[k]; !ok {
			t.Errorf("verify did not send %s", k)
		}
	}
	if len(sent) < 14 {
		t.Errorf("verify sent %d objects, want at least 14", len(sent))
	}
	st := r.status()
	for _, s := range []string{"primary as system:serviceaccount:catalyst:bp-self-sovereign-cutover-runner", "region-b as system:serviceaccount:kube-system:handover-peer"} {
		if !strings.Contains(st["serverDryRun"], s) {
			t.Errorf("serverDryRun does not name the account %q: %q", s, st["serverDryRun"])
		}
	}
	if !strings.Contains(st["message"], "the API server accepted all") {
		t.Errorf("the dry run's message does not say the server accepted the objects: %q", st["message"])
	}
	if t.Failed() {
		return
	}

	// The control: the same verify with the reader the sixth delivery used
	// (yq, YAML 1.2) must fail these checks on step 03's 0755, as the API
	// server did. Step 03 writes that volume's mode 0755 today; should it come
	// to write 493, the control puts 0755 back into the rendered podSpec.
	s03 := r.state + "/mechanism/cm/" + step03ConfigMap + ".json"
	cmb, err := os.ReadFile(filepath.Clean(s03))
	if err != nil {
		t.Fatal(err)
	}
	modeLine := regexp.MustCompile(`(name: cutover-secondary-harbor-prewarm-script\\n +defaultMode: )(\d+)`)
	m := modeLine.FindStringSubmatch(string(cmb))
	if m == nil {
		t.Fatal("control: step 03's secondary-harbor-prewarm volume has no defaultMode to write as 0755")
	}
	if m[2] != "0755" {
		if err := os.WriteFile(s03, []byte(modeLine.ReplaceAllString(string(cmb), "${1}0755")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mech := r.state + "/mechanism/release-run.sh"
	b, err := os.ReadFile(filepath.Clean(mech))
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(b), "\ncase \"${1:-}\" in\n", "\nk8s_json() { yq -o=json -I=0 '.' \"$1\"; }\ncase \"${1:-}\" in\n", 1)
	if old == string(b) {
		t.Fatal("release-run.sh has no dispatch line to put the old reader before")
	}
	if err := os.WriteFile(mech, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(r.stub + "/dry"); err != nil {
		t.Fatal(err)
	}
	if out, code := r.runArgs([]string{"verify"}, req, "RELEASE_RUN_SH="+mech); code != 0 {
		t.Fatalf("verify with the old reader: exit %d\n%s", code, out)
	}
	problems, _ = deliveryChecks(t, r)
	joined := strings.Join(problems, "\n")
	for _, must := range []string{
		"primary.Job.sovereign-release-delivery: spec.template.spec.volumes[8].configMap.defaultMode: Invalid value: 755",
		"spec.template.spec.volumes[8] (secondary-harbor-prewarm): the delivery Job has",
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("with the old reader the checks do not report %q; they report:\n%s", must, joined)
		}
	}
}

// renderedDaemonSet is the DaemonSet <name> of the render of this checkout's
// cutover chart with the values of the dry run.
func renderedDaemonSet(t *testing.T, name string) map[string]any {
	t.Helper()
	vals, _ := json.Marshal(realChartValues)
	vf := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(vf, vals, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("helm", "template", "self-sovereign-cutover", filepath.Join(repoRoot(t), cutoverChartPath), //nolint:gosec // the chart of this repository
		"--namespace", testNS, "-f", vf).Output()
	if err != nil {
		t.Fatalf("helm template %s: %v", cutoverChartPath, err)
	}
	for _, part := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(out), -1) {
		j, err := yaml.YAMLToJSON([]byte(part))
		if err != nil {
			t.Fatalf("the render does not parse: %v", err)
		}
		var o map[string]any
		if err := utiljson.Unmarshal(j, &o); err != nil || o == nil {
			continue
		}
		if o["kind"] == "DaemonSet" {
			if md, _ := o["metadata"].(map[string]any); md != nil && md["name"] == name {
				return o
			}
		}
	}
	t.Fatalf("the render has no DaemonSet %s", name)
	return nil
}

// Cutover step 04 (the registry-pivot DaemonSet) runs where the chart is
// installed, the primary region; on hw307 region b's nodes therefore kept their
// first-boot mirror and could not pull an image only the Sovereign's registry
// held (#7938, gate runs 16 and 17). A delivery reaches a follower through the
// candidate's step 03, its prewarm, which ends by applying step 04 there. So
// what verify sends to the follower must hold that DaemonSet: the primary's pod
// but for its account and status ConfigMap, the account, a binding of its role
// in every namespace the pod's script calls, the status ConfigMap that asks
// for v2, and the primary's pivot.sh. The primary's own DaemonSet stays in the
// render.
func TestReleaseDeliveryPivotsTheRegistryOfEveryRegion(t *testing.T) {
	r, _ := realChartDryRun(t)
	sent := r.sentObjects()
	for _, k := range []string{"primary.ConfigMap.cutover-follower-registry-pivot-release", "primary.ConfigMap.registry-pivot-script-release",
		"region-b.ConfigMap.registry-pivot-script", "region-b.ConfigMap.cutover-registry-pivot-follower-status", "region-b.ServiceAccount.registry-pivot",
		"region-b.ClusterRole.self-sovereign-cutover-registry-pivot", "region-b.DaemonSet.registry-pivot"} {
		if _, ok := sent[k]; !ok {
			t.Errorf("verify did not send %s", k)
		}
	}
	follower, ok := sent["region-b.DaemonSet.registry-pivot"]
	if !ok {
		t.FailNow()
	}
	primary := renderedDaemonSet(t, "registry-pivot")
	pod := func(o map[string]any) map[string]any {
		b, _ := json.Marshal(o)
		var c map[string]any
		_ = json.Unmarshal(b, &c)
		ps := c["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		return ps
	}
	fp, pp := pod(follower), pod(primary)
	envOf := func(ps map[string]any) map[string]string {
		m := map[string]string{}
		for _, e := range ps["containers"].([]any)[0].(map[string]any)["env"].([]any) {
			em := e.(map[string]any)
			if v, ok := em["value"].(string); ok {
				m[em["name"].(string)] = v
			}
		}
		return m
	}
	fe, pe := envOf(fp), envOf(pp)
	sa, _ := fp["serviceAccountName"].(string)
	if fe["STATUS_CONFIGMAP_NAME"] == pe["STATUS_CONFIGMAP_NAME"] {
		t.Errorf("the follower DaemonSet acks into the primary's status ConfigMap name %q", pe["STATUS_CONFIGMAP_NAME"])
	}
	for _, ps := range []map[string]any{fp, pp} {
		delete(ps, "serviceAccountName")
		for _, e := range ps["containers"].([]any)[0].(map[string]any)["env"].([]any) {
			if em := e.(map[string]any); em["name"] == "STATUS_CONFIGMAP_NAME" {
				em["value"] = "X"
			}
		}
	}
	if !equality.Semantic.DeepEqual(fp, pp) {
		t.Errorf("the follower's registry-pivot pod is not the primary's (but for its account and status ConfigMap):\nfollower %s\nprimary  %s", js(fp), js(pp))
	}
	if _, ok := sent["region-b.ServiceAccount."+sa]; !ok {
		t.Errorf("the follower DaemonSet runs as %q, which verify did not send", sa)
	}
	for _, key := range []string{"STATUS_CONFIGMAP_NAMESPACE", "GATEWAY_LB_SVC_NAMESPACE", "HARBOR_CA_SRC_NAMESPACE", "DF_NAMESPACE"} {
		ns := fe[key]
		rb, ok := sent["region-b.RoleBinding.self-sovereign-cutover-registry-pivot-"+ns]
		if ns == "" || !ok || rb["metadata"].(map[string]any)["namespace"] != ns {
			t.Errorf("no RoleBinding of the follower's account in namespace %q (%s), which its script calls", ns, key)
		}
	}
	st := sent["region-b.ConfigMap."+fe["STATUS_CONFIGMAP_NAME"]]
	if d, _ := st["data"].(map[string]any); d["registriesYamlActive"] != "v2" {
		t.Errorf("the follower's status ConfigMap does not ask for v2: %s", js(st))
	}
	script := renderedChart(t)["registry-pivot-script"].Data["pivot.sh"]
	if d, _ := sent["region-b.ConfigMap.registry-pivot-script"]["data"].(map[string]any); script == "" || d["pivot.sh"] != script {
		t.Errorf("the follower's pivot.sh is not the primary's")
	}
}

// DryRunPullSecretForTest is release-run.sh's DRY_RUN_PULL_SECRET: a dry run
// names no pull Secret, and the objects that name one name this.
const DryRunPullSecretForTest = "release-pull-dry-run"

// shellFunctions is the text of the named functions of a shell script, each
// from "<name>() {" to the next line that is "}".
func shellFunctions(t *testing.T, path string, names ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	for _, n := range names {
		start := -1
		for i, l := range lines {
			if l == n+"() {" {
				start = i
				break
			}
		}
		if start < 0 {
			t.Fatalf("%s defines no function %s", path, n)
		}
		for _, l := range lines[start:] {
			out = append(out, l)
			if l == "}" {
				break
			}
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// readAs runs a reader (shell that defines k8s_json) on the YAML text and
// returns its documents, decoded.
func readAs(t *testing.T, reader, text string) []any {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(d+"/doc.yaml", []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "set -eu; STATE=\"$1\"; "+reader+"\nk8s_json \"$1/doc.yaml\"", "sh", d) //nolint:gosec // the reader of this repository's script
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the reader: %v\n%s", err, out)
	}
	var docs []any
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("the reader wrote no JSON: %q", l)
		}
		docs = append(docs, v)
	}
	return docs
}

// asKubernetes reads the YAML documents of text as Kubernetes does.
func asKubernetes(t *testing.T, text string) []any {
	t.Helper()
	var docs []any
	for _, part := range regexp.MustCompile(`(?m)^---[ \t]*$`).Split(text, -1) {
		j, err := yaml.YAMLToJSON([]byte(part))
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(j, &v); err != nil {
			t.Fatal(err)
		}
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			docs = append(docs, v)
		}
	}
	return docs
}

// The values a YAML 1.2 reader reads differently from Kubernetes, and the
// ones both read alike; the reader of the delivery must read them all as
// Kubernetes does, and the reader of the sixth delivery must not (the
// control: the comparison can fail).
const yamlVersionTraps = `mode: 0755
octal12: 0o755
leading: 012
hex: 0x1F
answerYes: yes
answerNo: no
switchOn: on
switchOff: off
letterY: y
upperNo: NO
version: 1.10
sexagesimal: 1:30
quoted: "0755"
list: [0644, "on", off]
---
kind: Second
nested: {defaultMode: 0400, optional: yes}
`

func TestReleaseYAMLReaderIsTheKubernetesReader(t *testing.T) {
	script := filepath.Join(repoRoot(t), cutoverChartPath, "files/release/release-run.sh")
	reader := shellFunctions(t, script, "helm_local", "k8s_json")
	if got, want := readAs(t, reader, yamlVersionTraps), asKubernetes(t, yamlVersionTraps); !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("the delivery's reader reads the YAML 1.1 values differently from Kubernetes:\n got %s\nwant %s", js(got), js(want))
	}
	yq := "k8s_json() { yq -o=json -I=0 '.' \"$1\"; }"
	if got, want := readAs(t, yq, yamlVersionTraps), asKubernetes(t, yamlVersionTraps); equality.Semantic.DeepEqual(got, want) {
		t.Fatal("control: yq reads the YAML 1.1 values as Kubernetes does, so this comparison cannot fail")
	}

	// Every YAML document the delivery reads from the render: the render
	// itself, each step's podSpec and each Job manifest a ConfigMap carries.
	vals, _ := json.Marshal(realChartValues)
	vf := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(vf, vals, 0o600); err != nil {
		t.Fatal(err)
	}
	render, err := exec.Command("helm", "template", "self-sovereign-cutover", filepath.Join(repoRoot(t), cutoverChartPath), //nolint:gosec // the chart of this repository
		"--namespace", testNS, "-f", vf).Output()
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{"the render": string(render)}
	for name, cm := range renderedChart(t) {
		for k, v := range cm.Data {
			if k == "podSpec" || strings.HasSuffix(k, ".yaml") {
				docs[name+" "+k] = v
			}
		}
	}
	if len(docs) < 8 {
		t.Fatalf("only %d YAML documents found in the render", len(docs))
	}
	differ := 0
	for what, text := range docs {
		want := asKubernetes(t, text)
		if got := readAs(t, reader, text); !equality.Semantic.DeepEqual(got, want) {
			t.Errorf("%s: the delivery's reader reads it differently from Kubernetes", what)
		}
		if !equality.Semantic.DeepEqual(readAs(t, yq, text), want) {
			differ++
		}
	}
	// The render writes modes as 0755 (step 03, the follower Jobs): what yq
	// misread. The delivery's reader reads every document as Kubernetes does.
	t.Logf("%d YAML documents of the render read as Kubernetes reads them; yq reads %d of them differently", len(docs), differ)
}

// The checks of (a) themselves: each refuses what the API server refused.
func TestReleaseObjectChecksRefuseWhatTheAPIServerRefuses(t *testing.T) {
	job := func(mode any, mount string, extra map[string]any) map[string]any {
		c := map[string]any{"name": "prewarm", "image": "alpine/k8s:1.31.4", "volumeMounts": []any{map[string]any{"name": mount, "mountPath": "/x"}}}
		for k, v := range extra {
			c[k] = v
		}
		return map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "sovereign-release-1-delivery", "namespace": testNS},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"restartPolicy": "Never", "containers": []any{c},
				"volumes": []any{map[string]any{"name": "v", "configMap": map[string]any{"name": "c", "defaultMode": mode}}}}}}}
	}
	if p := objectProblems(job(int64(493), "v", nil)); len(p) != 0 {
		t.Fatalf("a valid Job is refused: %v", p)
	}
	for name, c := range map[string]struct {
		obj  map[string]any
		want string
	}{
		"the sixth delivery's mode":  {job(int64(755), "v", nil), "volumes[0].configMap.defaultMode: Invalid value: 755"},
		"a mount of no volume":       {job(int64(493), "w", nil), `volumeMounts[0].name: Not found: "w"`},
		"a field Kubernetes lacks":   {job(int64(493), "v", map[string]any{"bogus": true}), "field not declared in schema"},
		"a string where a number is": {job("493", "v", nil), "defaultMode: expected numeric"},
		"a bool where a string is":   {job(int64(493), "v", map[string]any{"image": true}), "image: expected string"},
		"a name no object may have":  {map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "Not_A_Name"}}, "metadata.name"},
		"data that is not a string":  {map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "x"}, "data": map[string]any{"a": int64(1)}}, "expected string"},
	} {
		t.Run(name, func(t *testing.T) {
			p := strings.Join(objectProblems(c.obj), "\n")
			if !strings.Contains(p, c.want) {
				t.Fatalf("want a problem naming %q; got:\n%s", c.want, p)
			}
		})
	}
}

// refuseTwo makes the stub refuse the delivery Job and promote's scripts
// ConfigMap in the follower, as the API server would.
const refuseTwo = "^primary\\.Job\\.\n^region-b\\.ConfigMap\\.sovereign-release-scripts$\n"

// The route's dry run and a delivery's compose name every object the API
// server refuses, not the first; a delivery whose objects are refused creates
// none of them, and removes the pull credential.
func TestReleaseServerDryRunNamesEveryRefusalAndCreatesNothing(t *testing.T) {
	wants := []string{"the API server refuses 2 of", "Job catalyst/sovereign-release-delivery: Error from server (Invalid)",
		"ConfigMap gitea/sovereign-release-scripts in region-b (promote): Error from server (Invalid)"}

	f := newFixture(t)
	id, _ := f.candidate(1, f.candidateCutoverRelease(step03Marker(candidateCutover)))
	req := releaseRequest{Candidate: id, DryRun: true}
	r := newRunFixtureFrom(t, f, req, false)
	r.write(r.stub, "refuse", refuseTwo)
	out, code := r.run("stage", req)
	st := r.status()
	if code == 0 || st["phase"] != "failed" || st["failedStep"] != "stage" {
		t.Fatalf("a dry run whose objects are refused: exit %d, phase %q, failedStep %q\n%s", code, st["phase"], st["failedStep"], out)
	}
	for _, w := range wants {
		if !strings.Contains(st["serverDryRun"], w) || !strings.Contains(st["message"], w) {
			t.Errorf("the dry run does not name %q:\nserverDryRun %q\nmessage %q", w, st["serverDryRun"], st["message"])
		}
	}

	r, req = credentialFixture(t, false, false)
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("stage: exit %d\n%s", code, out)
	}
	r.write(r.stub, "refuse", refuseTwo)
	out, code = r.run("deliver", req)
	st = r.status()
	if code == 0 || st["phase"] != "failed" || st["failedStep"] != "deliver" {
		t.Fatalf("a delivery whose objects are refused: exit %d, phase %q, failedStep %q\n%s", code, st["phase"], st["failedStep"], out)
	}
	for _, w := range wants {
		if !strings.Contains(st["message"], w) {
			t.Errorf("the delivery does not name %q: %q", w, st["message"])
		}
	}
	created, _ := filepath.Glob(r.stub + "/cm/*-release.json")
	if _, err := os.Stat(r.stub + "/job-sovereign-release-delivery.json"); err == nil || len(created) > 0 {
		t.Fatalf("a delivery whose objects are refused created some: the Job %v, ConfigMaps %v", err == nil, created)
	}
	// The request's credential and the stage's copy of it are deleted (no step
	// 03 ran, so none made a copy of its own).
	for _, s := range [][3]string{{"primary", testNS, releasePull}, {"region-b", testNS, releasePull}} {
		if r.secretData(s[0], s[1], s[2]) != nil {
			t.Errorf("after a refused delivery, region %s still holds %s/%s", s[0], s[1], s[2])
		}
	}
	if !strings.HasPrefix(st["credential"], "deleted in every region") {
		t.Errorf("after a refused delivery the state does not say the credential is deleted: %q", st["credential"])
	}
	if !strings.HasPrefix(st["serverDryRun"], "refused: ") {
		t.Errorf("serverDryRun %q", st["serverDryRun"])
	}
}

// A delivery Job whose Pod starts no container (on a node that cannot start
// Pods, say) is named in the state while the delivery waits for it.
func TestReleaseDeliveryNamesAPodThatStartsNoContainer(t *testing.T) {
	r, req := credentialFixture(t, false, false)
	r.env = append(r.env, "DELIVERY_STALL_SECONDS=0")
	r.write(r.stub, "pods.json", `{"items": [{"metadata": {"name": "sovereign-release-delivery-x7k2p"}, "spec": {"nodeName": "worker-3"},
  "status": {"phase": "Pending", "initContainerStatuses": [{"name": "prewarm", "state": {"waiting": {"reason": "ContainerCreating"}}}],
    "conditions": [{"type": "PodReadyToStartContainers", "status": "False"}]}}]}`)
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("stage: exit %d\n%s", code, out)
	}
	out, code := r.run("deliver", req)
	st := r.status()
	if code != 0 || st["phase"] != "delivered" {
		t.Fatalf("deliver: exit %d, phase %q\n%s", code, st["phase"], out)
	}
	if want := "Pod sovereign-release-delivery-x7k2p on node worker-3 (Pending): ContainerCreating"; !strings.HasPrefix(st["deliveryPod"], want) {
		t.Fatalf("deliveryPod %q, want it to begin %q", st["deliveryPod"], want)
	}
}
