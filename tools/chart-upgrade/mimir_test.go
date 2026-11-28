package chartupgrade

// The case of bp-mimir (run by platform/mimir/chart/tests/
// upgrade-over-deployed.sh), SEC-253. The root password of the chart's MinIO
// was a fixed literal, public, held in Secret mimir-minio and in Mimir's
// configuration (ConfigMap mimir-config). It becomes a value External Secrets
// generates (platform/secrets-lib, delivery synced): Secret
// mimir-minio-password, key password. MinIO reads it from MINIO_ROOT_PASSWORD,
// the bucket Job from a file, every Mimir component from MINIO_ROOT_PASSWORD
// expanded into its configuration. The chart under test applies over a fresh
// install and over the release kit slot 23 pins, deployed and live, as Helm 3
// upgrades it, and every reader is rolled by that upgrade.
//
// The known answer: 1.0.14 holds the literal in exactly Secret/mimir-minio and
// ConfigMap/mimir-config, where the check of the new render must find none.
//
// What it cannot see: External Secrets generating the value and writing the
// Secret (until it exists a rolled Pod waits in CreateContainerConfigError and
// the bucket hook Job's Pod in ContainerCreating), MinIO starting over its
// volume with a new root password, and bp-reloader rolling the readers when a
// raised epoch changes the Secret. The kinds of External Secrets are not
// modelled: none is live before this release, so Helm creates them.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	mimirChart   = "platform/mimir/chart"
	mimirRelease = "mimir"
	mimirValue   = "mimir-minio-password"
	mimirLiteral = "supersecret"
	mimirUser    = "grafana-mimir"
	mimirReload  = "secret.reloader.stakater.com/reload"
)

// The release kit slot 23 pins (clusters/_template/bootstrap-kit/23-mimir.yaml).
var mimirDeployed = []string{"1.0.14"}

// Every Mimir component: each reads the configuration and gets the variable.
var mimirComponents = []string{"StatefulSet/mimir-alertmanager", "StatefulSet/mimir-compactor", "Deployment/mimir-distributor",
	"StatefulSet/mimir-ingester", "Deployment/mimir-overrides-exporter", "Deployment/mimir-querier", "Deployment/mimir-query-frontend",
	"Deployment/mimir-query-scheduler", "Deployment/mimir-ruler", "StatefulSet/mimir-store-gateway"}

const mimirMinIO = "Deployment/mimir-minio"

func TestMimirAppliesOverEveryState(t *testing.T) {
	root := needTools(t)
	renders := map[string]map[string]Object{}
	for _, v := range mimirDeployed {
		c := releaseCommit(t, root, mimirChart, v)
		t.Logf("%s: the chart at commit %s", v, c)
		renders[v] = render(t, withDependencies(t, extract(t, root, mimirChart, c)), mimirRelease, nil)
	}
	chart := chartUnderTest(root, mimirChart)
	t.Logf("the chart under test: %s", chart)
	next := render(t, withDependencies(t, chart), mimirRelease, nil)

	t.Run("known answer: 1.0.14 holds the literal in Secret/mimir-minio and ConfigMap/mimir-config", func(t *testing.T) {
		if got, want := holdingLiteral(renders["1.0.14"]), []string{"ConfigMap/mimir-config", "Secret/mimir-minio"}; !slices.Equal(got, want) {
			t.Fatalf("the literal is in %v, want %v", got, want)
		}
	})
	t.Run("the new render", func(t *testing.T) { mimirRender(t, next) })
	t.Run("fresh install", func(t *testing.T) { mimirCheck(t, nil, nil, next) })
	for _, v := range mimirDeployed {
		t.Run(v+" deployed and live", func(t *testing.T) { mimirCheck(t, renders[v], renders[v], next) })
	}
	t.Run("the bucket Job logs in with the generated password", func(t *testing.T) { mimirBucketJob(t, chart, next) })
}

// holdingLiteral lists the objects that carry the fixed password, as text or
// base64 (the data of a Secret).
func holdingLiteral(objs map[string]Object) []string {
	b64 := base64.StdEncoding.EncodeToString([]byte(mimirLiteral))
	var out []string
	for k, o := range objs {
		if bytes.Contains(o.JSON, []byte(mimirLiteral)) || bytes.Contains(o.JSON, []byte(b64)) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// mimirRender: no literal and no Secret in the release; the ExternalSecret
// writes Secret mimir-minio-password from the value its PushSecret generates;
// Mimir's three S3 stanzas read the variable; MinIO and every component get it
// from that Secret and carry the reload annotation.
func mimirRender(t *testing.T, next map[string]Object) {
	t.Helper()
	if got := holdingLiteral(next); len(got) != 0 {
		t.Errorf("the fixed password is still in %v", got)
	}
	for k, o := range next {
		if o.GVK.Kind == "Secret" {
			t.Errorf("%s: the release holds a Secret; the password must reach the cluster through External Secrets only", k)
		}
	}
	key := "openova/apps/" + mimirRelease + "/" + mimirValue + "/e1"
	var es struct {
		Metadata struct{ Annotations map[string]string }
		Spec     struct {
			SecretStoreRef struct{ Kind, Name string }
			Target         struct{ Name, CreationPolicy string }
			Data           []struct {
				SecretKey string
				RemoteRef struct{ Key, Property string }
			}
		}
	}
	unmarshal(t, next, "ExternalSecret/"+mimirValue, &es)
	if es.Spec.Target.Name != mimirValue || es.Spec.Target.CreationPolicy != "Owner" || es.Spec.SecretStoreRef.Name != "openbao" ||
		len(es.Spec.Data) != 1 || es.Spec.Data[0].SecretKey != "password" || es.Spec.Data[0].RemoteRef.Key != key ||
		es.Metadata.Annotations["secrets.openova.io/register-id"] != mimirValue {
		t.Errorf("ExternalSecret/%s does not write Secret %s key password from %s through SecretStore openbao with register id %s: %+v", mimirValue, mimirValue, key, mimirValue, es)
	}
	var ps struct {
		Spec struct {
			UpdatePolicy string
			Selector     struct{ GeneratorRef struct{ Kind, Name string } }
			Data         []struct {
				Match struct{ RemoteRef struct{ RemoteKey string } }
			}
		}
	}
	unmarshal(t, next, "PushSecret/"+mimirValue+"-e1", &ps)
	if g := ps.Spec.Selector.GeneratorRef; g.Kind != "Password" || g.Name != mimirValue+"-e1" || ps.Spec.UpdatePolicy != "IfNotExists" ||
		len(ps.Spec.Data) != 1 || ps.Spec.Data[0].Match.RemoteRef.RemoteKey != key {
		t.Errorf("PushSecret/%s-e1 does not push the Password %s-e1 once (IfNotExists) to %s: %+v", mimirValue, mimirValue, key, ps)
	}
	for _, k := range []string{"Password/" + mimirValue + "-e1", "SecretStore/openbao", "ServiceAccount/eso-reader"} {
		if _, ok := next[k]; !ok {
			t.Errorf("the render has no %s", k)
		}
	}
	var cm corev1.ConfigMap
	unmarshal(t, next, "ConfigMap/mimir-config", &cm)
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["mimir.yaml"]), &cfg); err != nil {
		t.Fatalf("ConfigMap/mimir-config mimir.yaml: %v", err)
	}
	for _, s := range []string{"blocks_storage", "ruler_storage", "alertmanager_storage"} {
		stanza, _ := cfg[s].(map[string]any)
		s3, _ := stanza["s3"].(map[string]any)
		if s3["secret_access_key"] != "${MINIO_ROOT_PASSWORD}" || s3["access_key_id"] != mimirUser {
			t.Errorf("Mimir's %s.s3: access_key_id %v, secret_access_key %v; want %s and ${MINIO_ROOT_PASSWORD}", s, s3["access_key_id"], s3["secret_access_key"], mimirUser)
		}
	}
	for _, k := range append([]string{mimirMinIO}, mimirComponents...) {
		readsSecret(t, k, next[k])
	}
}

// readsSecret: the workload's container that reads the password (MinIO's, or
// the one Mimir runs with -config.expand-env=true) takes MINIO_ROOT_PASSWORD
// from Secret mimir-minio-password key password, and its Pods carry the reload
// annotation for that Secret.
func readsSecret(t *testing.T, k string, o Object) {
	t.Helper()
	if o.JSON == nil {
		t.Errorf("the render has no %s", k)
		return
	}
	spec, _, err := PodSpec(o)
	if err != nil {
		t.Fatal(err)
	}
	var c []corev1.Container
	for _, x := range spec.Containers {
		if (k == mimirMinIO && x.Name == "minio") || slices.Contains(x.Args, "-config.expand-env=true") {
			c = append(c, x)
		}
	}
	if len(c) != 1 {
		t.Errorf("%s: %d containers read the password, want 1", k, len(c))
		return
	}
	want := corev1.EnvVar{Name: "MINIO_ROOT_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: mimirValue}, Key: "password"}}}
	if i := slices.IndexFunc(c[0].Env, func(e corev1.EnvVar) bool { return e.Name == want.Name }); i < 0 || !reflect.DeepEqual(c[0].Env[i], want) {
		t.Errorf("%s container %s: MINIO_ROOT_PASSWORD is not from Secret %s key password: %+v", k, c[0].Name, mimirValue, c[0].Env)
	}
	if k == mimirMinIO {
		user := corev1.EnvVar{Name: "MINIO_ROOT_USER", Value: mimirUser}
		if i := slices.IndexFunc(c[0].Env, func(e corev1.EnvVar) bool { return e.Name == user.Name }); i < 0 || !reflect.DeepEqual(c[0].Env[i], user) {
			t.Errorf("%s: MINIO_ROOT_USER is not %s, the access_key_id of Mimir's configuration: %+v", k, mimirUser, c[0].Env)
		}
	}
	if got := podAnnotations(t, o)[mimirReload]; got != mimirValue {
		t.Errorf("%s: its Pods carry %s %q, want %q: bp-reloader would not roll it with the others when the value changes", k, mimirReload, got, mimirValue)
	}
}

// mimirCheck: every object the model judges applies and becomes the new
// render; the kinds of External Secrets are new (not live, so created); and on
// an installed Sovereign the upgrade rolls MinIO and every Mimir component
// together, the Pod template of each one changed by the patch.
func mimirCheck(t *testing.T, deployed, live, next map[string]Object) {
	t.Helper()
	outcomes := map[string]Outcome{}
	var created []string
	for k, m := range next {
		if !kinds.Recognizes(m.GVK) {
			if _, ok := live[k]; ok {
				t.Errorf("%s is live already; the model does not judge its kind", k)
			}
			created = append(created, k)
			continue
		}
		o, err := Upgrade(objPtr(deployed[k]), objPtr(live[k]), m)
		if err != nil {
			t.Fatal(err)
		}
		outcomes[k] = o
	}
	converges(t, outcomes, next)
	slices.Sort(created)
	t.Logf("created by Helm, not live before: %v", created)
	for k := range deployed {
		if _, ok := next[k]; !ok {
			t.Logf("%s: not in the new release; Helm deletes it (pkg/kube/client.go, update)", k)
		}
	}
	if live == nil {
		return
	}
	for _, k := range append([]string{mimirMinIO}, mimirComponents...) {
		before, _, err := PodSpec(live[k])
		if err != nil || before == nil {
			t.Fatalf("%s is not live: %v", k, err)
		}
		after, _, err := PodSpec(outcomes[k].Object)
		if err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(before, after) && reflect.DeepEqual(podAnnotations(t, live[k]), podAnnotations(t, outcomes[k].Object)) {
			t.Errorf("%s: the upgrade leaves its Pod template as it is, so its Pods keep the old password while MinIO takes the new one", k)
		}
	}
	var minio appsv1.Deployment
	if err := json.Unmarshal(outcomes[mimirMinIO].Object.JSON, &minio); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s rolls with strategy %s; each Mimir component rolls with its own", mimirMinIO, minio.Spec.Strategy.Type)
}

// mimirBucketJob runs the bucket Job's script as the Job runs it, with its
// /config folder holding what the Job's volume projects (the password under
// rootPassword), and a stand-in mc: it must log in as the root user with the
// password of that file.
func mimirBucketJob(t *testing.T, chart string, next map[string]Object) {
	t.Helper()
	job := hookJob(t, chart, "mimir-make-minio-buckets")
	spec := job.Spec.Template.Spec
	var secretItems []corev1.KeyToPath
	for _, v := range spec.Volumes {
		if v.Projected == nil || v.Name != "minio-configuration" {
			continue
		}
		for _, s := range v.Projected.Sources {
			if s.Secret != nil {
				if s.Secret.Name != mimirValue {
					t.Errorf("the bucket Job projects Secret %s, want %s", s.Secret.Name, mimirValue)
				}
				secretItems = s.Secret.Items
			}
		}
	}
	if want := []corev1.KeyToPath{{Key: "password", Path: "rootPassword"}}; !reflect.DeepEqual(secretItems, want) {
		t.Fatalf("the bucket Job projects %+v of its Secret, want %+v", secretItems, want)
	}
	env := map[string]string{}
	for _, e := range spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	var cm corev1.ConfigMap
	unmarshal(t, next, "ConfigMap/mimir-minio", &cm)
	dir := t.TempDir()
	pass := "Gen3rated-by-the-PushSecret"
	if err := os.WriteFile(filepath.Join(dir, "rootPassword"), []byte(pass), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\necho \"$*\" >> \"$MC_LOG\"\n"
	err := os.WriteFile(filepath.Join(bin, "mc"), []byte(stub), 0o700) // #nosec G306 -- an executable stand-in in the test's folder
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "initialize")
	if err := os.WriteFile(script, []byte(strings.ReplaceAll(cm.Data["initialize"], "/config/", dir+"/")), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "mc.log")
	run := exec.Command("sh", script) // #nosec G204 -- the chart's own script, in the test's folder
	run.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "MC_LOG=" + log, "MINIO_ENDPOINT=" + env["MINIO_ENDPOINT"],
		"MINIO_PORT=" + env["MINIO_PORT"], "MINIO_ROOT_USER=" + env["MINIO_ROOT_USER"]}
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("the bucket script: %v\n%s", err, out)
	}
	calls, err := os.ReadFile(log) // #nosec G304 -- the stand-in's log in the test's folder
	if err != nil {
		t.Fatal(err)
	}
	login := "alias set myminio http://mimir-minio:9000 " + mimirUser + " " + pass
	if !slices.ContainsFunc(strings.Split(string(calls), "\n"), func(l string) bool { return strings.HasSuffix(l, login) }) {
		t.Errorf("the bucket script did not run mc ... %s; it ran:\n%s", login, calls)
	}
}

// hookJob is a Job of the chart that is a Helm hook (Parse leaves hooks out).
func hookJob(t *testing.T, chart, name string) batchv1.Job {
	t.Helper()
	out, err := exec.Command("helm", "template", mimirRelease, chart, "--namespace", mimirRelease).Output() // #nosec G204 -- helm on a chart of the repository
	if err != nil {
		t.Fatalf("helm template %s: %v", chart, err)
	}
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(out)))
	for {
		doc, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var head struct {
			Kind     string
			Metadata struct{ Name string }
		}
		if err := yaml.Unmarshal(doc, &head); err != nil {
			t.Fatal(err)
		}
		if head.Kind != "Job" || head.Metadata.Name != name {
			continue
		}
		var j batchv1.Job
		if err := yaml.Unmarshal(doc, &j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	t.Fatalf("the render of %s has no Job %s", chart, name)
	return batchv1.Job{}
}

func unmarshal(t *testing.T, objs map[string]Object, k string, into any) {
	t.Helper()
	o, ok := objs[k]
	if !ok {
		t.Fatalf("the render has no %s", k)
	}
	if err := json.Unmarshal(o.JSON, into); err != nil {
		t.Fatalf("%s: %v", k, err)
	}
}

func podAnnotations(t *testing.T, o Object) map[string]string {
	t.Helper()
	var w struct {
		Spec struct {
			Template struct {
				Metadata struct{ Annotations map[string]string }
			}
		}
	}
	if err := json.Unmarshal(o.JSON, &w); err != nil {
		t.Fatal(err)
	}
	return w.Spec.Template.Metadata.Annotations
}
