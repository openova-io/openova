package chartupgrade

// The case of bp-openbao (run by platform/openbao/chart/tests/
// upgrade-over-deployed.sh), SEC-163. Two objects a Sovereign holds change: the
// CronJob openbao-restore-drill gains a writable /tmp for its scratch store,
// and the Job openbao-pki-bootstrap-<hash> is deleted as soon as it finishes
// (ttlSecondsAfterFinished 0). The chart under test applies over a fresh install
// and over each release a Sovereign holds, with the live objects that release
// left: the PKI Job as the API server and the Job controller keep it after it
// succeeded, as measured on the Sovereign on 27 Nov 2026 in the primary region
// (succeeded 1, no time to live, no hook, managed by Helm, chart 1.2.93).
//
// The known answer: an upgrade that keeps 1.2.93 leaves that Job finished with
// no time to live, so it stays, as on the Sovereign.
//
// What it cannot see: the time-to-live controller deleting the Job (it checks
// the fields that controller reads), whether a real snapshot then restores into
// the scratch store (the next drill on the Sovereign shows it), and the
// defaults the API server adds to the other live objects.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	baoChart   = "platform/openbao/chart"
	baoRelease = "openbao"
	baoDrill   = "CronJob/openbao-restore-drill"
	baoPKIJob  = "Job/openbao-pki-bootstrap-"
)

// The values of kit slot 08 that render both objects, for a primary region.
var baoValues = []string{"--set", "seal.stanza=x", "--set", "pki.enabled=true", "--set", "pki.regionRole=primary",
	"--set", "pki.domain=sovereign.example.test", "--set", "peerTrust.selfRegion=region-a",
	"--set", "backup.enabled=true", "--set", "backup.bucket=bucket-a", "--set", "restoreDrill.enabled=true"}

// The release the Sovereign ran on 27 Nov 2026, and the release of main after it.
var baoDeployed = []string{"1.2.93", "1.2.94"}

func TestOpenBaoAppliesOverEveryState(t *testing.T) {
	root := needTools(t)
	renders := map[string]map[string]Object{}
	for _, v := range baoDeployed {
		c := releaseCommit(t, root, baoChart, v)
		t.Logf("%s: the chart at commit %s", v, c)
		renders[v] = render(t, withDependencies(t, extract(t, root, baoChart, c)), baoRelease, baoValues)
	}
	chart := chartUnderTest(root, baoChart)
	t.Logf("the chart under test: %s", chart)
	copied := filepath.Join(t.TempDir(), "chart")
	if err := os.CopyFS(copied, os.DirFS(chart)); err != nil {
		t.Fatal(err)
	}
	next := render(t, withDependencies(t, copied), baoRelease, baoValues)

	t.Run("known answer: 1.2.93 kept leaves the finished PKI Job with no time to live", func(t *testing.T) {
		d := renders["1.2.93"]
		k := pkiJob(t, d)
		live := baoLive(t, d)
		o, err := Upgrade(objPtr(d[k]), objPtr(live[k]), d[k])
		if err != nil {
			t.Fatal(err)
		}
		if ttl := jobOf(t, o.Object).Spec.TTLSecondsAfterFinished; o.Refusal != "" || ttl != nil {
			t.Fatalf("%s: refusal %q, ttlSecondsAfterFinished %v; the Sovereign kept it with none", k, o.Refusal, ttl)
		}
	})
	t.Run("fresh install", func(t *testing.T) { baoCheck(t, nil, nil, next) })
	for _, v := range baoDeployed {
		t.Run(v+" deployed and live, the PKI Job finished", func(t *testing.T) {
			baoCheck(t, renders[v], baoLive(t, renders[v]), next)
		})
	}
}

// baoCheck: every object the model judges applies; each object but the PKI Job
// becomes the new render; the PKI Job keeps its template and is deleted once
// it is finished; the drill's scratch store gets a writable /tmp.
func baoCheck(t *testing.T, deployed, live, next map[string]Object) {
	t.Helper()
	outcomes := map[string]Outcome{}
	var jobs, unjudged []string
	for k, m := range next {
		if !kinds.Recognizes(m.GVK) {
			unjudged = append(unjudged, k)
			continue
		}
		o, err := Upgrade(objPtr(deployed[k]), objPtr(live[k]), m)
		if err != nil {
			t.Fatal(err)
		}
		if m.GVK.Kind != "Job" {
			outcomes[k] = o
			continue
		}
		jobs = append(jobs, k)
		if o.Refusal != "" {
			t.Errorf("%s: the upgrade is refused: %s", k, o.Refusal)
			continue
		}
		after := jobOf(t, o.Object)
		if l, ok := live[k]; ok && !reflect.DeepEqual(after.Spec.Template, jobOf(t, l).Spec.Template) {
			t.Errorf("%s: the patch changes the template of the live Job, which the API server refuses (field is immutable)", k)
		}
		if ttl := after.Spec.TTLSecondsAfterFinished; ttl == nil || *ttl != 0 {
			t.Errorf("%s after the upgrade: ttlSecondsAfterFinished %v, want 0: the finished Job stays in namespace openbao", k, ttl)
		} else if finished(after) {
			t.Logf("%s: finished, time to live 0: the time-to-live controller deletes it", k)
		}
	}
	t.Logf("not judged (kinds the model does not hold): %v", unjudged)
	for k := range deployed {
		if _, ok := next[k]; !ok {
			t.Logf("%s: not in the new release; Helm deletes it (pkg/kube/client.go, update)", k)
		}
	}
	if len(jobs) != 1 || !strings.HasPrefix(jobs[0], baoPKIJob) {
		t.Errorf("the new render holds the Jobs %v, want one %s<hash>", jobs, baoPKIJob)
	}
	converges(t, outcomes, next)
	drill, ok := outcomes[baoDrill]
	if !ok {
		t.Fatalf("the render has no %s", baoDrill)
	}
	spec, _, err := PodSpec(drill.Object)
	if err != nil {
		t.Fatal(err)
	}
	if !writableTmp(spec, "scratch") {
		t.Errorf("%s after the upgrade: init container scratch has no writable emptyDir at /tmp, where its restore writes the snapshot", baoDrill)
	}
}

// baoLive is the deployed release as the Sovereign holds it: every object as
// rendered, the PKI Job with what the API server and the Job controller add.
func baoLive(t *testing.T, deployed map[string]Object) map[string]Object {
	t.Helper()
	live := map[string]Object{}
	for k, o := range deployed {
		live[k] = o
	}
	k := pkiJob(t, deployed)
	j := jobOf(t, deployed[k])
	uid, one := "0f6c2e9a-5b1d-4c3e-9a7f-2d8b4e6c1a30", int32(1)
	j.Labels = merged(j.Labels, map[string]string{"app.kubernetes.io/managed-by": "Helm"})
	j.Annotations = merged(j.Annotations, map[string]string{"meta.helm.sh/release-name": baoRelease, "meta.helm.sh/release-namespace": baoRelease})
	j.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"batch.kubernetes.io/controller-uid": uid}}
	j.Spec.Template.Labels = merged(j.Spec.Template.Labels, map[string]string{
		"batch.kubernetes.io/controller-uid": uid, "batch.kubernetes.io/job-name": j.Name, "controller-uid": uid, "job-name": j.Name})
	mode := batchv1.NonIndexedCompletion
	j.Spec.Completions, j.Spec.Parallelism, j.Spec.CompletionMode = &one, &one, &mode
	done := metav1.NewTime(time.Date(2026, 11, 27, 13, 8, 51, 0, time.UTC))
	j.Status = batchv1.JobStatus{Succeeded: 1, CompletionTime: &done,
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: done}}}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	live[k] = Object{GVK: deployed[k].GVK, Name: deployed[k].Name, JSON: b}
	return live
}

// withDependencies builds the dependencies of a chart that has none in charts/.
func withDependencies(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "Chart.yaml")) // #nosec G304 -- a chart of the repository
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Dependencies []struct{} `json:"dependencies"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if built, _ := filepath.Glob(filepath.Join(dir, "charts", "*.tgz")); len(built) >= len(c.Dependencies) {
		return dir
	}
	if out, err := exec.Command("helm", "dependency", "build", dir).CombinedOutput(); err != nil { // #nosec G204 -- helm on a chart of the repository
		t.Fatalf("helm dependency build %s: %v\n%s", dir, err, out)
	}
	return dir
}

func pkiJob(t *testing.T, objs map[string]Object) string {
	t.Helper()
	var found []string
	for k := range objs {
		if strings.HasPrefix(k, baoPKIJob) {
			found = append(found, k)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the render holds %v, want one %s<hash>", found, baoPKIJob)
	}
	return found[0]
}

func jobOf(t *testing.T, o Object) batchv1.Job {
	t.Helper()
	var j batchv1.Job
	if err := json.Unmarshal(o.JSON, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

// finished is the rule of the time-to-live controller: a Job with condition
// Complete or Failed true.
func finished(j batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// writableTmp: the init container mounts, not read-only, an emptyDir volume at /tmp.
func writableTmp(spec *corev1.PodSpec, container string) bool {
	for _, c := range spec.InitContainers {
		if c.Name != container {
			continue
		}
		for _, m := range c.VolumeMounts {
			if m.MountPath != "/tmp" || m.ReadOnly {
				continue
			}
			for _, v := range spec.Volumes {
				if v.Name == m.Name && v.EmptyDir != nil {
					return true
				}
			}
		}
	}
	return false
}

func objPtr(o Object) *Object {
	if o.JSON == nil {
		return nil
	}
	return &o
}

func merged(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
