package bootstrapkit

// SEC-153, SEC-154: the upgrade path of a Sovereign installed before the seed
// Secret flux-system/bootstrap-seed existed, or before a Secret joined it.
//
// Such a Sovereign holds the bootstrap Secrets its first boot wrote
// (harbor-robot-token, pdm-basicauth, sovereign-tls-secrets, ghcr-pull and
// object-storage in flux-system, catalyst-handover-jwt-public in
// catalyst-system) and no seed, or (installed between SEC-153 and SEC-154) a
// seed without the values of ghcr-pull and object-storage. When a tree with
// these changes reaches it (the branch it follows before its cutover, or a
// release delivered into its Gitea after it), the root Kustomization applies
// three children in this order: bootstrap-seed, whose Job runs build-seed.sh
// (clusters/_template/bootstrap-secrets/seed/job.yaml) and builds the seed
// from the Secrets or adds what it lacks; bootstrap-secrets, which renders the
// manifests under clusters/_template/bootstrap-secrets from the seed; and
// bootstrap-kit. cloud-credentials stays as first boot wrote it: the builder
// does not read it and nothing renders it from the seed.
//
// The tests run that script against a kubectl stub that keeps Secrets as files,
// render the manifests the way Flux substitutes them (a Secret source is
// read decoded; ${NAME} is the value, the empty text when unset), and replay
// Flux's order over the root Kustomizations of every region folder:
//
//	TestBootstrapSeedUpgradeKeepsTheKitReconciling              (a)
//	TestBootstrapSeedUpgradeNeverWritesAnEmptyOrUnresolvedValue (b)
//	TestBootstrapSeedUpgradeKeepsTheOldValues                   (c)
//	TestBootstrapSeedUpgradeAddsWhatTheSeedLacks                (d)
//
// Each also proves on a broken copy that its check can fail.

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	seedNamespace  = "flux-system"
	seedSecret     = "bootstrap-seed"
	seedJobFile    = "clusters/_template/bootstrap-secrets/seed/job.yaml"
	seedSecretsDir = "clusters/_template/bootstrap-secrets"
	seedTemplate   = "infra/providers/_shared/cloudinit-control-plane.tftpl"
	reflectorKey   = "reflector.v1.k8s.emberstack.com/"
)

// seedKubectlStub stands for kubectl in build-seed.sh. A Secret is the folder
// STUB_DIR/store/<namespace>/<name>, one file per key holding the key's stored
// value (base64), as the API server keeps data. Every call is kept in
// STUB_DIR/calls. It answers what the script asks and nothing else.
const seedKubectlStub = `#!/bin/sh
echo "kubectl $*" >>"$STUB_DIR/calls"
ns=default out="" envfile="" ignore="" manager="" ptype="" pfile="" words=""
while [ $# -gt 0 ]; do
  case "$1" in
    -n) ns=$2; shift ;;
    -o) out=$2; shift ;;
    --ignore-not-found) ignore=1 ;;
    --from-env-file=*) envfile=${1#*=} ;;
    --field-manager=*) manager=${1#*=} ;;
    --type=*) ptype=${1#*=} ;;
    --patch-file=*) pfile=${1#*=} ;;
    -*) echo "kubectl stub: unexpected flag $1" >&2; exit 9 ;;
    *) words="$words $1" ;;
  esac
  shift
done
set -- $words
s="$STUB_DIR/store/$ns/$3"
case "$1 $2" in
  "get secret")
    if [ ! -d "$s" ]; then
      [ -n "$ignore" ] && exit 0
      echo "Error from server (NotFound): secrets \"$3\" not found" >&2
      exit 1
    fi
    case "$out" in
      name) echo "secret/$3" ;;
      "jsonpath={.data."*"}")
        k=$(printf '%s' "${out#"jsonpath={.data."}" | sed 's/}$//; s/\\\././g')
        if [ -f "$s/$k" ]; then cat "$s/$k"; fi ;;
      "go-template="*'{{$k}}{{"\n"}}'*)
        ls -A "$s" ;;
      *) echo "kubectl stub: unexpected output $out" >&2; exit 9 ;;
    esac ;;
  "create secret")
    s="$STUB_DIR/store/$ns/$4"
    if [ "$3" != generic ] || [ -z "$envfile" ] || [ "$manager" != catalyst-bootstrap-seed ]; then
      echo "kubectl stub: unexpected create $*" >&2
      exit 9
    fi
    if [ -d "$s" ]; then
      echo "Error from server (AlreadyExists): secrets \"$4\" already exists" >&2
      exit 1
    fi
    mkdir -p "$s"
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      printf '%s' "${line#*=}" | base64 | tr -d '\n' >"$s/${line%%=*}"
    done <"$envfile"
    echo "secret/$4 created" ;;
  "patch secret")
    if [ "$ptype" != merge ] || [ -z "$pfile" ] || [ "$manager" != catalyst-bootstrap-seed ] || [ ! -d "$s" ]; then
      echo "kubectl stub: unexpected patch $*" >&2
      exit 9
    fi
    body=$(sed 's/^{"stringData":{//; s/}}$//' "$pfile")
    printf '%s\n' "$body" | tr ',' '\n' | while IFS= read -r kv; do
      [ -n "$kv" ] || continue
      k=$(printf '%s' "$kv" | sed 's/^"\([^"]*\)":".*$/\1/')
      v=$(printf '%s' "$kv" | sed 's/^"[^"]*":"\(.*\)"$/\1/')
      printf '%s' "$v" | base64 | tr -d '\n' >"$s/$k"
    done
    echo "secret/$3 patched" ;;
  *) echo "kubectl stub: unexpected call $*" >&2; exit 9 ;;
esac
`

// liveSecret is a Secret as a consumer reads it: data holds each key's value.
type liveSecret struct {
	ns, name, typ string
	annotations   map[string]string
	data          map[string]string
}

func (s liveSecret) id() string { return s.ns + "/" + s.name }

func reflection(namespaces string) map[string]string {
	return map[string]string{
		reflectorKey + "reflection-allowed":            "true",
		reflectorKey + "reflection-allowed-namespaces": namespaces,
		reflectorKey + "reflection-auto-enabled":       "true",
		reflectorKey + "reflection-auto-namespaces":    namespaces,
	}
}

// oldBootstrapSecrets are the four Secrets as the first boot before SEC-153
// wrote them (infra/providers/_shared/cloudinit-control-plane.tftpl at
// 803bc9ee04): names, namespaces, types, reflector annotations and keys. The
// values are made up, with the characters Flux substitution and YAML would
// change ($, braces, a colon, a space, JSON).
func oldBootstrapSecrets() []liveSecret {
	return []liveSecret{
		{ns: "flux-system", name: "harbor-robot-token", typ: "Opaque", annotations: reflection("catalyst-system"),
			data: map[string]string{"token": "robot$token-${HOME}{x}"}},
		{ns: "flux-system", name: "pdm-basicauth", typ: "Opaque", annotations: reflection("org-services,catalyst,catalyst-system,gitea,harbor"),
			data: map[string]string{"username": "pool-user", "password": "pool pass: $1 {x}"}},
		{ns: "flux-system", name: "sovereign-tls-secrets", typ: "Opaque",
			data: map[string]string{"POWERDNS_API_KEY": base64.StdEncoding.EncodeToString([]byte("dns-api-key-for-tests"))}},
		{ns: "catalyst-system", name: "catalyst-handover-jwt-public", typ: "Opaque",
			data: map[string]string{"public.jwk": `{"kty":"RSA","e":"AQAB","n":"made-up_for-tests"}`}},
		// SEC-154: the two Secrets as the first boot before SEC-154 wrote
		// them (the template at 2e1c2d0d7c).
		{ns: "flux-system", name: "ghcr-pull", typ: "kubernetes.io/dockerconfigjson", annotations: map[string]string{
			reflectorKey + "reflection-allowed":            "true",
			reflectorKey + "reflection-allowed-namespaces": "org-services,catalyst,catalyst-system,gitea,harbor,flux-system,org-.*,[a-z][a-z0-9-]*",
			reflectorKey + "reflection-auto-enabled":       "true",
			reflectorKey + "reflection-auto-namespaces":    "org-services,catalyst,catalyst-system,gitea,harbor,org-.*,[a-z][a-z0-9-]*",
		}, data: map[string]string{".dockerconfigjson": `{"auths":{"ghcr.io":{"auth":"dXNlcjp0byRrZW57eH0=","password":"to$ken{x}","username":"user"}}}`}},
		{ns: "flux-system", name: "object-storage", typ: "Opaque", data: map[string]string{
			"endpoint": "https://s3.example.test:443/a b", "region": "region ${x}", "bucket": "bucket-1",
			"s3-endpoint": "https://s3.example.test:443/a b", "s3-region": "region ${x}", "s3-bucket": "bucket-1",
			"s3-access-key": "ak$1", "s3-secret-key": "sk {x}: \"y\"", "access-key": "ak$1", "secret-key": "sk {x}: \"y\"",
		}},
	}
}

// seedFixture is one region: the stub's Secrets, its calls and the work
// folder (the Job's emptyDir).
type seedFixture struct {
	dir, script, entries string
}

func newSeedFixture(t *testing.T, root string) *seedFixture {
	t.Helper()
	f := &seedFixture{dir: t.TempDir()}
	for _, d := range []string{"bin", "store", "work"} {
		if err := os.MkdirAll(filepath.Join(f.dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.dir, "bin", "kubectl"), []byte(seedKubectlStub), 0o700); err != nil { //nolint:gosec // a fake program must be executable
		t.Fatal(err)
	}
	f.script = filepath.Join(f.dir, "build-seed.sh")
	script, entries := seedBuilderScript(t, root)
	f.entries = entries
	if err := os.WriteFile(f.script, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// put writes a Secret into the stub: each value is stored base64-encoded.
func (f *seedFixture) put(t *testing.T, ns, name string, data map[string]string) {
	t.Helper()
	d := filepath.Join(f.dir, "store", ns, name)
	if err := os.RemoveAll(d); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	for k, v := range data {
		if err := os.WriteFile(filepath.Join(d, k), []byte(base64.StdEncoding.EncodeToString([]byte(v))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *seedFixture) putAll(t *testing.T, secrets []liveSecret) {
	t.Helper()
	for _, s := range secrets {
		f.put(t, s.ns, s.name, s.data)
	}
}

// get reads a Secret of the stub decoded, as Flux reads a substituteFrom Secret.
func (f *seedFixture) get(t *testing.T, ns, name string) (map[string]string, bool) {
	t.Helper()
	d := filepath.Join(f.dir, "store", ns, name)
	entries, err := os.ReadDir(d)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	} else if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(d, e.Name())) //nolint:gosec // a file of the stub under the test's temporary folder
		if err != nil {
			t.Fatal(err)
		}
		v, err := base64.StdEncoding.DecodeString(string(b))
		if err != nil {
			t.Fatalf("stub Secret %s/%s key %s is not base64: %v", ns, name, e.Name(), err)
		}
		out[e.Name()] = string(v)
	}
	return out, true
}

// snapshot is every file of the stub's Secrets and its work folder.
func (f *seedFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, top := range []string{"store", "work"} {
		err := filepath.WalkDir(filepath.Join(f.dir, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p) //nolint:gosec // a file of the stub under the test's temporary folder
			rel, _ := filepath.Rel(f.dir, p)
			out[rel] = string(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (f *seedFixture) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// build runs build-seed.sh once, as the Job's container runs it.
func (f *seedFixture) build(t *testing.T) (int, string) {
	t.Helper()
	cmd := exec.Command("sh", f.script) //nolint:gosec // the script of this repository, copied into the test's temporary folder
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(f.dir, "bin")+":"+os.Getenv("PATH"),
		"STUB_DIR="+f.dir, "SEED_WORK="+filepath.Join(f.dir, "work"), "SEED_ENTRIES="+f.entries)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &exit):
		return exit.ExitCode(), out.String()
	}
	t.Fatalf("run build-seed.sh: %v", err)
	return -1, ""
}

func readYAMLDocs(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a file of this repository
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var d map[string]any
		if err := dec.Decode(&d); errors.Is(err, io.EOF) {
			return docs
		} else if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
}

func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func list(v any) []any { l, _ := v.([]any); return l }

// seedBuilderScript is build-seed.sh of the ConfigMap that the Job mounts at
// /seed, and the value of SEED_ENTRIES in the Job's environment (SEC-154); it
// also checks that the Job runs that script and nothing else.
func seedBuilderScript(t *testing.T, root string) (string, string) {
	t.Helper()
	var script, configMap string
	var job map[string]any
	for _, d := range readYAMLDocs(t, filepath.Join(root, seedJobFile)) {
		switch d["kind"] {
		case "ConfigMap":
			configMap, _ = dig(d, "metadata", "name").(string)
			script, _ = dig(d, "data", "build-seed.sh").(string)
		case "Job":
			job = d
		}
	}
	if script == "" || job == nil {
		t.Fatalf("%s: no ConfigMap with build-seed.sh, or no Job", seedJobFile)
	}
	spec := dig(job, "spec", "template", "spec")
	mounted := false
	for _, v := range list(dig(spec, "volumes")) {
		mounted = mounted || dig(v, "configMap", "name") == configMap
	}
	containers := list(dig(spec, "containers"))
	if !mounted || len(containers) != 1 || fmt.Sprint(dig(containers[0], "command")) != "[/bin/sh /seed/build-seed.sh]" || dig(containers[0], "args") != nil {
		t.Fatalf("%s: the Job does not run build-seed.sh of ConfigMap %s", seedJobFile, configMap)
	}
	entries := ""
	for _, e := range list(dig(containers[0], "env")) {
		if dig(e, "name") == "SEED_ENTRIES" {
			entries, _ = dig(e, "value").(string)
		}
	}
	if entries == "" {
		t.Fatalf("%s: the Job's container has no SEED_ENTRIES", seedJobFile)
	}
	return script, entries
}

// fluxKustomization is what Flux's order reads of a root child.
type fluxKustomization struct {
	name, path     string
	wait           bool
	dependsOn      []string
	substituteFrom []substituteRef
}

type substituteRef struct {
	kind, name string
	optional   bool
}

var seedChildren = map[string]bool{"bootstrap-seed": true, "bootstrap-secrets": true, "bootstrap-kit": true}

// rootFolders are the region folders a first boot's root Kustomization names.
func rootFolders(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{"clusters/_template/root/*/kustomization.yaml", "clusters/_template/providers/*/root/*/kustomization.yaml"} {
		m, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range m {
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("no region folder under clusters/_template/root")
	}
	return out
}

// rootKustomizations builds a region folder: its resources (files and
// folders) and its patches (a delete by name; any other patch of the three
// children is refused, because this test does not model it).
func rootKustomizations(t *testing.T, root, dir string) map[string]fluxKustomization {
	t.Helper()
	k := readYAMLDocs(t, filepath.Join(root, dir, "kustomization.yaml"))[0]
	out := map[string]fluxKustomization{}
	for _, r := range list(k["resources"]) {
		rel := filepath.Join(dir, fmt.Sprint(r))
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() {
			for n, v := range rootKustomizations(t, root, rel) {
				out[n] = v
			}
			continue
		}
		for _, d := range readYAMLDocs(t, filepath.Join(root, rel)) {
			if d["kind"] != "Kustomization" || !strings.HasPrefix(fmt.Sprint(d["apiVersion"]), "kustomize.toolkit.fluxcd.io/") {
				continue
			}
			fk := fluxKustomization{name: fmt.Sprint(dig(d, "metadata", "name")), path: fmt.Sprint(dig(d, "spec", "path"))}
			fk.wait, _ = dig(d, "spec", "wait").(bool)
			for _, dep := range list(dig(d, "spec", "dependsOn")) {
				fk.dependsOn = append(fk.dependsOn, fmt.Sprint(dig(dep, "name")))
			}
			for _, s := range list(dig(d, "spec", "postBuild", "substituteFrom")) {
				opt, _ := dig(s, "optional").(bool)
				fk.substituteFrom = append(fk.substituteFrom, substituteRef{fmt.Sprint(dig(s, "kind")), fmt.Sprint(dig(s, "name")), opt})
			}
			out[fk.name] = fk
		}
	}
	for _, p := range list(k["patches"]) {
		var patch map[string]any
		if err := yaml.Unmarshal([]byte(fmt.Sprint(dig(p, "patch"))), &patch); err != nil {
			t.Fatalf("%s: a patch: %v", dir, err)
		}
		name := fmt.Sprint(dig(patch, "metadata", "name"))
		if patch["$patch"] == "delete" {
			delete(out, name)
		} else if seedChildren[name] {
			t.Fatalf("%s patches the Kustomization %s, which this test does not model", dir, name)
		}
	}
	return out
}

func (k fluxKustomization) seedSource() (substituteRef, bool) {
	for _, s := range k.substituteFrom {
		if s.kind == "Secret" && s.name == seedSecret {
			return s, true
		}
	}
	return substituteRef{}, false
}

// renderedSecret is a manifest of clusters/_template/bootstrap-secrets after
// Flux's substitution, read as the API server stores it.
type renderedSecret struct {
	liveSecret
	problems []string
}

var fluxReference = regexp.MustCompile(`\$\{([^}]*)\}`)

// renderBootstrapSecrets substitutes the manifests from vars as Flux does: in
// the text of each document, ${NAME} becomes the value, the empty text when
// vars has no NAME. A problem is an empty value, a "${" left in a value, a
// reference vars does not hold, or a data value that is not base64.
func renderBootstrapSecrets(t *testing.T, root string, vars map[string]string) []renderedSecret {
	t.Helper()
	dir := filepath.Join(root, seedSecretsDir)
	var out []renderedSecret
	for _, r := range list(readYAMLDocs(t, filepath.Join(dir, "kustomization.yaml"))[0]["resources"]) {
		path := filepath.Join(dir, fmt.Sprint(r))
		raw, err := os.ReadFile(path) //nolint:gosec // a file of this repository
		if err != nil {
			t.Fatal(err)
		}
		for i, text := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(raw), -1) {
			var before map[string]any
			if err := yaml.Unmarshal([]byte(text), &before); err != nil || before["kind"] != "Secret" {
				continue
			}
			var rs renderedSecret
			unresolved := map[string]bool{}
			for _, field := range []string{"data", "stringData"} {
				for k, v := range asMap(before[field]) {
					for _, m := range fluxReference.FindAllStringSubmatch(fmt.Sprint(v), -1) {
						if _, ok := vars[m[1]]; !ok {
							rs.problems = append(rs.problems, fmt.Sprintf("%s document %d %s.%s: ${%s} is not a key of the seed", r, i, field, k, m[1]))
							unresolved[field+"."+k] = true
						}
					}
				}
			}
			substituted := fluxReference.ReplaceAllStringFunc(text, func(m string) string { return vars[m[2:len(m)-1]] })
			var after map[string]any
			if err := yaml.Unmarshal([]byte(substituted), &after); err != nil {
				t.Fatalf("%s document %d after substitution: %v", r, i, err)
			}
			rs.ns, rs.name = fmt.Sprint(dig(after, "metadata", "namespace")), fmt.Sprint(dig(after, "metadata", "name"))
			rs.typ = fmt.Sprint(after["type"])
			rs.annotations, rs.data = map[string]string{}, map[string]string{}
			for k, v := range asMap(dig(after, "metadata", "annotations")) {
				rs.annotations[k] = fmt.Sprint(v)
			}
			for field, encoded := range map[string]bool{"data": true, "stringData": false} {
				for k, v := range asMap(after[field]) {
					s, _ := v.(string)
					if s == "" || unresolved[field+"."+k] || strings.Contains(s, "${") {
						rs.problems = append(rs.problems, fmt.Sprintf("%s %s.%s renders %q", rs.id(), field, k, s))
					}
					if encoded {
						b, err := base64.StdEncoding.DecodeString(s)
						if err != nil || len(b) == 0 {
							rs.problems = append(rs.problems, fmt.Sprintf("%s data.%s renders %q, not the base64 of a value", rs.id(), k, s))
						}
						s = string(b)
					}
					rs.data[k] = s
				}
			}
			out = append(out, rs)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id() < out[j].id() })
	return out
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// reconcileRegion replays Flux's order over the root children of one region,
// with the stub as its cluster, and returns what went wrong. Each pass takes
// the children in name order, which puts bootstrap-kit and bootstrap-secrets
// before bootstrap-seed; a child is reconciled only when every child it
// dependsOn is ready. bootstrap-seed runs the seed builder: with wait it is
// ready when the Job has succeeded, without wait it is ready at once and its
// Job runs only after the next pass. bootstrap-secrets renders from the seed
// and applies the four Secrets; a missing seed fails its build unless the
// source is optional, and then it renders without it. bootstrap-kit's first
// consumers need the four Secrets.
func reconcileRegion(t *testing.T, root string, ks map[string]fluxKustomization, f *seedFixture) []string {
	t.Helper()
	var problems []string
	names := make([]string, 0, len(ks))
	for n := range ks {
		names = append(names, n)
	}
	sort.Strings(names)
	ready := map[string]bool{}
	var queued, next []func()
	runJob := func() bool {
		rc, out := f.build(t)
		if rc != 0 {
			problems = append(problems, "the seed builder exits "+fmt.Sprint(rc)+": "+strings.TrimSpace(out))
		}
		return rc == 0
	}
	for pass := 0; pass < 6; pass++ {
		for _, n := range names {
			k := ks[n]
			waiting := false
			for _, d := range k.dependsOn {
				waiting = waiting || !ready[d]
			}
			if ready[n] || waiting {
				continue
			}
			switch n {
			case "bootstrap-seed":
				if k.path != "./clusters/_template/bootstrap-secrets/seed" {
					problems = append(problems, "bootstrap-seed builds "+k.path+", not the seed builder")
				}
				if k.wait {
					ready[n] = runJob()
				} else {
					ready[n] = true
					queued = append(queued, func() { runJob() })
				}
			case "bootstrap-secrets":
				src, ok := k.seedSource()
				if !ok {
					problems = append(problems, "bootstrap-secrets does not substitute from the Secret "+seedSecret)
					continue
				}
				vars, have := f.get(t, seedNamespace, seedSecret)
				if !have && !src.optional {
					problems = append(problems, "bootstrap-secrets was reconciled before the seed existed (its build fails: Secret flux-system/bootstrap-seed not found)")
					continue
				}
				for _, r := range renderBootstrapSecrets(t, root, vars) {
					problems = append(problems, r.problems...)
					f.put(t, r.ns, r.name, r.data)
				}
				ready[n] = true
			case "bootstrap-kit":
				for _, s := range oldBootstrapSecrets() {
					if _, ok := f.get(t, s.ns, s.name); !ok {
						problems = append(problems, "bootstrap-kit was reconciled before the Secret "+s.id()+" existed")
					}
				}
				ready[n] = true
			default:
				ready[n] = true
			}
		}
		for _, job := range next {
			job()
		}
		next, queued = queued, nil
	}
	for _, n := range names {
		if !ready[n] {
			problems = append(problems, n+" never became ready")
		}
	}
	return problems
}

// builtSeed is the seed the builder makes from the four old Secrets.
func builtSeed(t *testing.T, root string) map[string]string {
	t.Helper()
	f := newSeedFixture(t, root)
	f.putAll(t, oldBootstrapSecrets())
	if rc, out := f.build(t); rc != 0 {
		t.Fatalf("build-seed.sh on the four Secrets exits %d: %s", rc, out)
	}
	seed, _ := f.get(t, seedNamespace, seedSecret)
	return seed
}

func cloneKustomizations(ks map[string]fluxKustomization) map[string]fluxKustomization {
	out := map[string]fluxKustomization{}
	for n, k := range ks {
		k.dependsOn = append([]string(nil), k.dependsOn...)
		k.substituteFrom = append([]substituteRef(nil), k.substituteFrom...)
		out[n] = k
	}
	return out
}

func without(l []string, s string) []string {
	var out []string
	for _, v := range l {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// (a) On a region with the four Secrets and no seed, the seed exists before
// bootstrap-secrets renders from it, and every root child becomes ready; on a
// region whose first boot created the seed, the four Secrets exist before the
// kit starts. In every region folder.
func TestBootstrapSeedUpgradeKeepsTheKitReconciling(t *testing.T) {
	root := repoRoot(t)
	seed := builtSeed(t, root)
	regions := func(ks map[string]fluxKustomization) []string {
		var problems []string
		old := newSeedFixture(t, root)
		old.putAll(t, oldBootstrapSecrets())
		for _, p := range reconcileRegion(t, root, ks, old) {
			problems = append(problems, "installed before the seed: "+p)
		}
		if _, ok := old.get(t, seedNamespace, seedSecret); !ok {
			problems = append(problems, "installed before the seed: no seed at the end")
		}
		fresh := newSeedFixture(t, root)
		fresh.put(t, seedNamespace, seedSecret, seed)
		for _, p := range reconcileRegion(t, root, ks, fresh) {
			problems = append(problems, "installed with the seed: "+p)
		}
		return problems
	}
	for _, dir := range rootFolders(t, root) {
		ks := rootKustomizations(t, root, dir)
		for n := range seedChildren {
			if _, ok := ks[n]; !ok {
				t.Errorf("%s: no root child %s", dir, n)
			}
		}
		for _, p := range regions(ks) {
			t.Errorf("%s: %s", dir, p)
		}
	}
	// The check can fail: each order broken on a copy of the primary folder.
	base := rootKustomizations(t, root, "clusters/_template/root/base-primary")
	for what, breakIt := range map[string]func(map[string]fluxKustomization){
		"bootstrap-secrets without dependsOn bootstrap-seed": func(m map[string]fluxKustomization) {
			k := m["bootstrap-secrets"]
			k.dependsOn = without(k.dependsOn, "bootstrap-seed")
			m["bootstrap-secrets"] = k
		},
		"bootstrap-seed without wait": func(m map[string]fluxKustomization) {
			k := m["bootstrap-seed"]
			k.wait = false
			m["bootstrap-seed"] = k
		},
		"bootstrap-kit without dependsOn bootstrap-secrets": func(m map[string]fluxKustomization) {
			k := m["bootstrap-kit"]
			k.dependsOn = without(k.dependsOn, "bootstrap-secrets")
			m["bootstrap-kit"] = k
		},
		"no bootstrap-seed": func(m map[string]fluxKustomization) { delete(m, "bootstrap-seed") },
	} {
		broken := cloneKustomizations(base)
		breakIt(broken)
		if len(regions(broken)) == 0 {
			t.Errorf("the order broken (%s) is not found", what)
		}
	}
}

// (b) A missing Secret, a missing key or an empty value builds no seed, leaves
// the four Secrets untouched and stops naming the Secret and the key; the
// substituteFrom of bootstrap-secrets is not optional; the seed's keys are
// those first boot writes and the manifests read; no rendered value is empty
// or unresolved.
func TestBootstrapSeedUpgradeNeverWritesAnEmptyOrUnresolvedValue(t *testing.T) {
	root := repoRoot(t)
	change := func(id, key string, value *string) func([]liveSecret) []liveSecret {
		return func(in []liveSecret) []liveSecret {
			var out []liveSecret
			for _, s := range in {
				if s.id() == id {
					if key == "" {
						continue
					}
					data := map[string]string{}
					for k, v := range s.data {
						data[k] = v
					}
					if value == nil {
						delete(data, key)
					} else {
						data[key] = *value
					}
					s.data = data
				}
				out = append(out, s)
			}
			return out
		}
	}
	empty, notBase64 := "", "not base64 text: $x"
	for _, c := range []struct {
		what, secret, key string
		edit              func([]liveSecret) []liveSecret
	}{
		{"a missing Secret", "flux-system/harbor-robot-token", "token", change("flux-system/harbor-robot-token", "", nil)},
		{"a missing Secret of catalyst-system", "catalyst-system/catalyst-handover-jwt-public", "public.jwk", change("catalyst-system/catalyst-handover-jwt-public", "", nil)},
		{"a missing Secret pdm-basicauth", "flux-system/pdm-basicauth", "username", change("flux-system/pdm-basicauth", "", nil)},
		{"a missing Secret sovereign-tls-secrets", "flux-system/sovereign-tls-secrets", "POWERDNS_API_KEY", change("flux-system/sovereign-tls-secrets", "", nil)},
		{"a missing key", "flux-system/pdm-basicauth", "password", change("flux-system/pdm-basicauth", "password", nil)},
		{"an empty value", "flux-system/pdm-basicauth", "username", change("flux-system/pdm-basicauth", "username", &empty)},
		{"an empty value under stringData", "flux-system/sovereign-tls-secrets", "POWERDNS_API_KEY", change("flux-system/sovereign-tls-secrets", "POWERDNS_API_KEY", &empty)},
		{"a stringData value that is not base64 text", "flux-system/sovereign-tls-secrets", "POWERDNS_API_KEY", change("flux-system/sovereign-tls-secrets", "POWERDNS_API_KEY", &notBase64)},
		{"a missing Secret ghcr-pull", "flux-system/ghcr-pull", ".dockerconfigjson", change("flux-system/ghcr-pull", "", nil)},
		{"a missing key of object-storage", "flux-system/object-storage", "s3-secret-key", change("flux-system/object-storage", "s3-secret-key", nil)},
		{"an empty value of object-storage", "flux-system/object-storage", "s3-bucket", change("flux-system/object-storage", "s3-bucket", &empty)},
	} {
		f := newSeedFixture(t, root)
		f.putAll(t, c.edit(oldBootstrapSecrets()))
		before := f.snapshot(t)
		rc, out := f.build(t)
		if rc != 1 || !strings.Contains(out, "Secret "+c.secret+" key "+c.key+": ") {
			t.Errorf("%s: build-seed.sh exits %d and says %q; want 1, naming %s and its key %s", c.what, rc, out, c.secret, c.key)
		}
		if _, ok := f.get(t, seedNamespace, seedSecret); ok {
			t.Errorf("%s: a seed was built", c.what)
		}
		if after := f.snapshot(t); !reflect.DeepEqual(before, after) {
			t.Errorf("%s: the Secrets or the work folder changed: %v", c.what, after)
		}
		if strings.Contains(f.calls(t), " create ") {
			t.Errorf("%s: kubectl create was called", c.what)
		}
	}

	for _, dir := range rootFolders(t, root) {
		src, ok := rootKustomizations(t, root, dir)["bootstrap-secrets"].seedSource()
		if !ok || src.optional {
			t.Errorf("%s: bootstrap-secrets must substitute from the Secret %s, not optional (found %v, optional %v)", dir, seedSecret, ok, src.optional)
		}
	}

	seed := builtSeed(t, root)
	built := sortedKeys(seed)
	if fb := firstBootSeedKeys(t, root); !reflect.DeepEqual(built, fb) {
		t.Errorf("the builder writes the keys %v, first boot writes %v", built, fb)
	}
	if refs := manifestReferences(t, root); !reflect.DeepEqual(built, refs) {
		t.Errorf("the builder writes the keys %v, the manifests read %v", built, refs)
	}
	for _, r := range renderBootstrapSecrets(t, root, seed) {
		for _, p := range r.problems {
			t.Errorf("rendered from the built seed: %s", p)
		}
	}

	// The check can fail: a seed without one key renders an empty value, and
	// an optional source reached on a region whose seed cannot be built writes
	// empty values into the live Secrets.
	short := map[string]string{}
	for k, v := range seed {
		short[k] = v
	}
	delete(short, "PDM_BASIC_AUTH_PASS")
	found := 0
	for _, r := range renderBootstrapSecrets(t, root, short) {
		found += len(r.problems)
	}
	if found == 0 {
		t.Error("a seed without PDM_BASIC_AUTH_PASS renders no empty value: the check cannot fail")
	}
	ks := cloneKustomizations(rootKustomizations(t, root, "clusters/_template/root/base-primary"))
	k := ks["bootstrap-secrets"]
	for i := range k.substituteFrom {
		k.substituteFrom[i].optional = true
	}
	k.dependsOn = without(k.dependsOn, "bootstrap-seed")
	ks["bootstrap-secrets"] = k
	f := newSeedFixture(t, root)
	f.putAll(t, change("flux-system/harbor-robot-token", "", nil)(oldBootstrapSecrets()))
	if p := strings.Join(reconcileRegion(t, root, ks, f), "\n"); !strings.Contains(p, `renders ""`) {
		t.Errorf("an optional seed source on a region without a seed writes no empty value: the check cannot fail (%s)", p)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// firstBootSeedKeys are the keys of bootstrap-seed.env in the first-boot template.
func firstBootSeedKeys(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, seedTemplate)) //nolint:gosec // a file of this repository
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	key := regexp.MustCompile(`^\s+([A-Z][A-Z0-9_]*)=\$\{base64encode\(`)
	var out []string
	for i, l := range lines {
		if strings.TrimSpace(l) != "- path: /var/lib/catalyst/bootstrap-seed.env" {
			continue
		}
		for _, l := range lines[i+1:] {
			if strings.TrimSpace(l) == "" {
				break
			}
			if m := key.FindStringSubmatch(l); m != nil {
				out = append(out, m[1])
			}
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("%s: no bootstrap-seed.env entry", seedTemplate)
	}
	return out
}

// manifestReferences are the names the four manifests reference.
func manifestReferences(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]string{}
	for _, r := range renderBootstrapSecrets(t, root, map[string]string{}) {
		for _, p := range r.problems {
			if m := regexp.MustCompile(`\$\{([A-Z0-9_]+)\} is not a key`).FindStringSubmatch(p); m != nil {
				seen[m[1]] = ""
			}
		}
	}
	return sortedKeys(seen)
}

// compareWithOld lists every difference between the rendered Secrets and the
// old ones: the set of Secrets, and per Secret its type, keys, values and
// reflector annotations.
func compareWithOld(old []liveSecret, rendered []renderedSecret) []string {
	var diffs []string
	byID := map[string]renderedSecret{}
	for _, r := range rendered {
		byID[r.id()] = r
	}
	if len(rendered) != len(old) {
		diffs = append(diffs, fmt.Sprintf("%d Secrets rendered, %d before", len(rendered), len(old)))
	}
	reflectorOnly := func(m map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			if strings.HasPrefix(k, reflectorKey) {
				out[k] = v
			}
		}
		return out
	}
	for _, o := range old {
		r, ok := byID[o.id()]
		switch {
		case !ok:
			diffs = append(diffs, o.id()+" is not rendered")
		case r.typ != o.typ:
			diffs = append(diffs, fmt.Sprintf("%s type %s, before %s", o.id(), r.typ, o.typ))
		case !reflect.DeepEqual(r.data, o.data):
			diffs = append(diffs, fmt.Sprintf("%s values %q, before %q", o.id(), r.data, o.data))
		case !reflect.DeepEqual(reflectorOnly(r.annotations), reflectorOnly(o.annotations)):
			diffs = append(diffs, fmt.Sprintf("%s reflector annotations %v, before %v", o.id(), reflectorOnly(r.annotations), reflectorOnly(o.annotations)))
		}
	}
	return diffs
}

// (c) The four manifests rendered from the seed the builder makes equal the
// four Secrets as they were, and a second run changes nothing.
func TestBootstrapSeedUpgradeKeepsTheOldValues(t *testing.T) {
	root := repoRoot(t)
	old := oldBootstrapSecrets()
	f := newSeedFixture(t, root)
	f.putAll(t, old)
	if rc, out := f.build(t); rc != 0 {
		t.Fatalf("build-seed.sh exits %d: %s", rc, out)
	}
	seed, _ := f.get(t, seedNamespace, seedSecret)
	rendered := renderBootstrapSecrets(t, root, seed)
	for _, d := range compareWithOld(old, rendered) {
		t.Errorf("rendered from the built seed: %s", d)
	}
	for _, r := range rendered {
		f.put(t, r.ns, r.name, r.data)
	}
	if work, _ := os.ReadDir(filepath.Join(f.dir, "work")); len(work) != 0 {
		t.Errorf("the seed's values stay in the work folder: %d files", len(work))
	}

	before, calls := f.snapshot(t), f.calls(t)
	rc, out := f.build(t)
	if rc != 0 || !strings.Contains(out, "holds every value, nothing to add") {
		t.Errorf("a second run exits %d and says %q; want 0 and nothing to add", rc, out)
	}
	if after := f.snapshot(t); !reflect.DeepEqual(before, after) {
		t.Error("a second run changed the Secrets")
	}
	if strings.Contains(strings.TrimPrefix(f.calls(t), calls), " create ") {
		t.Error("a second run called kubectl create")
	}
	seed2, _ := f.get(t, seedNamespace, seedSecret)
	if again := renderBootstrapSecrets(t, root, seed2); !reflect.DeepEqual(again, rendered) {
		t.Error("a second render differs from the first")
	}

	// The check can fail: one reflector namespace, one value and one type
	// changed are each found.
	for what, edit := range map[string]func(*liveSecret){
		"a reflector namespace": func(s *liveSecret) { s.annotations = reflection("catalyst-system,harbor") },
		"a value":               func(s *liveSecret) { s.data = map[string]string{"token": "another"} },
		"a type":                func(s *liveSecret) { s.typ = "kubernetes.io/basic-auth" },
	} {
		changed := oldBootstrapSecrets()
		edit(&changed[0])
		if len(compareWithOld(changed, rendered)) == 0 {
			t.Errorf("%s changed is not found", what)
		}
	}
}

// sec153Seed is the seed of a Sovereign installed between SEC-153 and
// SEC-154: the five values of SEC-153, as first boot wrote them.
func sec153Seed(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range builtSeed(t, root) {
		switch k {
		case "HARBOR_ROBOT_TOKEN", "PDM_BASIC_AUTH_USER", "PDM_BASIC_AUTH_PASS", "POWERDNS_API_KEY", "HANDOVER_JWT_PUBLIC_KEY":
			out[k] = v
		}
	}
	if len(out) != 5 {
		t.Fatalf("the built seed lacks a key of SEC-153: %v", sortedKeys(out))
	}
	return out
}

// (d) On a region whose seed lacks the values of ghcr-pull and object-storage
// (installed between SEC-153 and SEC-154), the builder adds
// them from those Secrets with one patch, never changes a value the seed holds,
// renders every Secret as it was, keeps the kit reconciling, and on a missing
// Secret writes nothing; a second run adds nothing.
func TestBootstrapSeedUpgradeAddsWhatTheSeedLacks(t *testing.T) {
	root := repoRoot(t)
	partial := sec153Seed(t, root)

	f := newSeedFixture(t, root)
	f.putAll(t, oldBootstrapSecrets())
	f.put(t, seedNamespace, seedSecret, partial)
	rc, out := f.build(t)
	if rc != 0 || !strings.Contains(out, "added to flux-system/bootstrap-seed") {
		t.Fatalf("build-seed.sh on a seed of SEC-153 exits %d and says %q; want 0 and the values added", rc, out)
	}
	if calls := f.calls(t); strings.Contains(calls, " create ") || strings.Count(calls, " patch secret bootstrap-seed ") != 1 {
		t.Errorf("want one patch of the seed and no create, calls:\n%s", calls)
	}
	seed, _ := f.get(t, seedNamespace, seedSecret)
	if want := builtSeed(t, root); !reflect.DeepEqual(seed, want) {
		t.Errorf("the completed seed %v differs from the seed built from the Secrets %v", sortedKeys(seed), sortedKeys(want))
	}
	for _, d := range compareWithOld(oldBootstrapSecrets(), renderBootstrapSecrets(t, root, seed)) {
		t.Errorf("rendered from the completed seed: %s", d)
	}
	if work, _ := os.ReadDir(filepath.Join(f.dir, "work")); len(work) != 0 {
		t.Errorf("the seed's values stay in the work folder: %d files", len(work))
	}
	before, calls := f.snapshot(t), f.calls(t)
	if rc, out := f.build(t); rc != 0 || !strings.Contains(out, "holds every value, nothing to add") {
		t.Errorf("a second run exits %d and says %q; want 0 and nothing to add", rc, out)
	}
	if after := f.snapshot(t); !reflect.DeepEqual(before, after) || strings.Contains(strings.TrimPrefix(f.calls(t), calls), " patch ") {
		t.Error("a second run changed the seed")
	}

	// A value the seed holds stays as first boot wrote it, also when the live
	// Secret has another one.
	held := map[string]string{}
	for k, v := range partial {
		held[k] = v
	}
	held["HARBOR_ROBOT_TOKEN"] = base64.StdEncoding.EncodeToString([]byte("the value first boot wrote"))
	keep := func(script string) bool {
		g := newSeedFixture(t, root)
		if script != "" {
			if err := os.WriteFile(g.script, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		g.putAll(t, oldBootstrapSecrets())
		g.put(t, seedNamespace, seedSecret, held)
		if rc, out := g.build(t); rc != 0 {
			t.Fatalf("build-seed.sh exits %d: %s", rc, out)
		}
		got, _ := g.get(t, seedNamespace, seedSecret)
		return got["HARBOR_ROBOT_TOKEN"] == held["HARBOR_ROBOT_TOKEN"]
	}
	if !keep("") {
		t.Error("the builder changed HARBOR_ROBOT_TOKEN, a value the seed holds")
	}
	// The check can fail: a builder that does not skip the values the seed
	// holds patches them with the live values.
	script, _ := seedBuilderScript(t, root)
	broken := strings.Replace(script, `if grep -qx "$var" "$have"; then`, `if false; then`, 1)
	if broken == script {
		t.Fatal("the builder has no skip of a value the seed holds to break")
	}
	if keep(broken) {
		t.Error("a builder that patches the values the seed holds is not found: the check cannot fail")
	}

	// A missing Secret writes nothing: the seed stays as it was.
	g := newSeedFixture(t, root)
	var without []liveSecret
	for _, s := range oldBootstrapSecrets() {
		if s.name != "ghcr-pull" {
			without = append(without, s)
		}
	}
	g.putAll(t, without)
	g.put(t, seedNamespace, seedSecret, partial)
	snap := g.snapshot(t)
	if rc, out := g.build(t); rc != 1 || !strings.Contains(out, "Secret flux-system/ghcr-pull key .dockerconfigjson: ") {
		t.Errorf("without ghcr-pull build-seed.sh exits %d and says %q; want 1, naming the Secret and its key", rc, out)
	}
	if after := g.snapshot(t); !reflect.DeepEqual(snap, after) || strings.Contains(g.calls(t), " patch ") {
		t.Error("without ghcr-pull the seed or a Secret changed")
	}

	// Flux's order on every region folder: the seed is completed before
	// bootstrap-secrets renders, and the kit finds every Secret.
	for _, dir := range rootFolders(t, root) {
		h := newSeedFixture(t, root)
		h.putAll(t, oldBootstrapSecrets())
		h.put(t, seedNamespace, seedSecret, partial)
		for _, p := range reconcileRegion(t, root, rootKustomizations(t, root, dir), h) {
			t.Errorf("%s, installed with a seed of SEC-153: %s", dir, p)
		}
	}
}
