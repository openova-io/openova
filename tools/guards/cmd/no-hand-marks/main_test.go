package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Each case is one fixture file; want is the rule each finding of the file
// breaks, in order ("" for a write that breaks none).
func TestScan(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		// restart: a kubectl rollout restart, whatever its object.
		{"rollout restart of a kube-system workload", `kubectl --kubeconfig="$KC" -n kube-system rollout restart deploy/coredns >/dev/null 2>&1 || true`, []string{"restart"}},
		{"rollout restart with a variable object and namespace", `kubectl rollout restart "${kind}/${name}" -n "${ns}" >/dev/null 2>&1 || true`, []string{"restart"}},
		{"rollout restart of another namespace", `kubectl -n gitea rollout restart deploy/gitea`, []string{"restart"}},
		{"rollout restart split over lines", "kubectl -n kube-system \\\n  rollout restart deploy/coredns", []string{"restart"}},
		{"rollout restart with its own field manager is still the hand's annotation", `kubectl rollout restart deploy/x -n kube-system --field-manager=catalyst-x`, []string{"restart"}},
		{"rollout status is a read", `kubectl -n kube-system rollout status ds/cilium --timeout=240s`, nil},

		// manager: each write verb against a tracked object without the flag.
		{"patch of a kube-system Deployment", `kubectl -n kube-system patch deploy coredns -p '{}'`, []string{"manager"}},
		{"patch with the flag", `kubectl -n kube-system patch deploy coredns --field-manager=catalyst-first-boot -p '{}'`, []string{""}},
		{"patch with the flag as two words", `kubectl -n kube-system patch deploy coredns --field-manager catalyst-first-boot -p '{}'`, []string{""}},
		{"patch with a manager of the hand", `kubectl -n kube-system patch deploy coredns --field-manager=kubectl-x -p '{}'`, []string{"manager"}},
		{"patch with another manager than catalyst-", `kubectl -n kube-system patch deploy coredns --field-manager=dr-promoter -p '{}'`, []string{"manager"}},
		{"apply into a tracked namespace", `kubectl apply -n catalyst-system -f x.yaml`, []string{"manager"}},
		{"apply into a tracked namespace with the flag", `kubectl apply -n catalyst-system --field-manager=catalyst-x -f x.yaml`, []string{""}},
		{"annotate a HelmRelease of a variable namespace", `kubectl -n "${HR_NAMESPACE}" annotate helmrelease "${HR_NAME}" "a=b" --overwrite`, []string{"manager"}},
		{"annotate a HelmRelease by its group", `kubectl annotate helmreleases.helm.toolkit.fluxcd.io/bp-x -n flux-system a=b`, []string{"manager"}},
		{"label a cnpg-system DaemonSet", `kubectl label -n cnpg-system ds x a=b`, []string{"manager"}},
		{"scale a catalyst-system Deployment", `kubectl -n catalyst-system scale deploy x --replicas=0`, []string{"manager"}},
		{"set env on a Deployment of a variable namespace", "kubectl set env \"deploy/${DEPLOYMENT_NAME}\" \\\n  -n \"${DEPLOYMENT_NAMESPACE}\" \\\n  \"A=b\"", []string{"manager"}},
		{"set env on a Deployment with the flag on a continuation line", "kubectl set env \"deploy/${D}\" \\\n  -n \"${N}\" --field-manager=catalyst-x \\\n  \"A=b\"", []string{""}},
		{"patch a StatefulSet without a namespace", `kubectl patch sts db -p '{}'`, []string{"manager"}},
		{"patch hr in short form", `kubectl patch hr -n "$NS" "$NAME" --type=merge -p '{"spec":{"suspend":true}}' >/dev/null`, []string{"manager"}},
		{"the flag in a variable the file assigns", "FM=\"--field-manager=catalyst-dr-promoter\"\nkubectl -n \"$NS\" patch helmrelease \"$N\" --type merge ${FM} -p x", []string{""}},
		{"a variable that assigns another manager", "FM=\"--field-manager=dr-promoter\"\nkubectl -n \"$NS\" patch helmrelease \"$N\" --type merge ${FM} -p x", []string{"manager"}},
		{"the flag after a patch body with nested quotes", `kubectl -n "${HR_NAMESPACE}" patch helmrelease "${HR_NAME}" --type merge -p "{\"a\":\"$(date -u +"%FT%TZ")\"}" --field-manager=catalyst-x`, []string{""}},
		{"two commands on one line are judged one by one", `kubectl -n kube-system get deploy coredns && kubectl -n kube-system patch deploy coredns -p x`, []string{"manager"}},

		// Writes that reach no tracked object, and what writes nothing.
		{"patch of a Secret of a tracked namespace", `kubectl -n flux-system patch secret ghcr-pull --type=merge -p '{}'`, []string{""}},
		{"annotate a GitRepository of flux-system", `kubectl annotate --overwrite gitrepository openova -n flux-system "a=b"`, []string{""}},
		{"patch a Deployment of another namespace written out", `kubectl -n newapi patch deploy newapi -p '{}'`, []string{""}},
		{"apply of a file without a namespace", `kubectl apply -f /var/lib/catalyst/flux.yaml || true`, []string{""}},
		{"annotate --local writes nothing", `kubectl annotate --local -f - -o yaml hr/x a=b`, nil},
		{"a server dry run writes nothing", `kubectl apply --dry-run=server -n kube-system -f x.yaml`, nil},
		{"a get is a read", `kubectl -n kube-system get deploy coredns >/dev/null 2>&1 && break`, nil},

		// Comments and messages hold nothing.
		{"a comment line", `# kubectl -n kube-system rollout restart deploy/coredns`, nil},
		{"a comment after a command", `echo hi # kubectl rollout restart deploy/x`, nil},
		{"a Go template comment over lines", "{{- /*\n roll via `kubectl rollout restart` then\n*/}}\nkubectl -n kube-system get deploy x", nil},
		{"an echo message", `echo "[x] WARN: kubectl patch of loft HR failed (#5650)" >&2`, nil},
		{"an echo message that tells a person what to run", `echo "[x]   kubectl annotate helmrelease <name> -n <namespace> a=\"<why>\""`, nil},
		{"a command in a YAML string is a command", `- 'for i in 1 2; do kubectl -n kube-system rollout restart deploy/x && break; done'`, []string{"restart"}},
		{"the annotation key and manager names are not kubectl", `jq '{"kubectl.kubernetes.io/restartedAt": $t}' # kubectl-rollout`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, f := range scan(c.text) {
				got = append(got, f.Rule)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") || len(got) != len(c.want) {
				t.Fatalf("scan(%q) = %q, want %q (%+v)", c.text, got, c.want, scan(c.text))
			}
		})
	}
}

func TestScanLineNumbers(t *testing.T) {
	text := "a: b\n# kubectl -n kube-system rollout restart deploy/x\nrun: |\n  kubectl -n kube-system \\\n    rollout restart deploy/coredns\n"
	got := scan(text)
	if len(got) != 1 || got[0].Line != 4 || got[0].Rule != "restart" {
		t.Fatalf("scan = %+v, want one restart on line 4", got)
	}
}

func TestScope(t *testing.T) {
	in := []string{
		"infra/providers/_shared/cloudinit-control-plane.tftpl",
		"infra/providers/hetzner/main.tf",
		"infra/providers/huawei/cloudinit-worker.tftpl",
		"clusters/_template/sovereign-tls/cilium-envoy-tls-restart-job.yaml",
		"clusters/_template/bootstrap-kit/03-flux.yaml",
		"platform/flux/chart/templates/helm-release-stuck-recovery.yaml",
		"platform/self-sovereign-cutover/chart/files/follower-registry-pivot.sh",
		"platform/x/chart/templates/tests/function-test.yaml",
		"products/catalyst/chart/templates/x.yaml",
		"products/catalyst/charts/sub/templates/x.tpl",
	}
	out := []string{
		"infra/providers/huawei/tests/x.yaml",
		"clusters/_template/tests/x.yaml",
		"clusters/_template/sovereign-tls/README.md",
		"platform/flux/chart/tests/leader-election-and-recovery.sh",
		"products/catalyst/charts/sub/tests/x.sh",
		"platform/flux/chart/README.md",
		"platform/self-sovereign-cutover/chart/Chart.yaml",
		"platform/flux/README.md",
		"platform/flux/blueprint.yaml",
		"scripts/check-x.sh",
		"products/catalyst/bootstrap/api/internal/x.go",
	}
	for _, p := range in {
		if !inScope(p) {
			t.Errorf("inScope(%q) = false, want true", p)
		}
	}
	for _, p := range out {
		if inScope(p) {
			t.Errorf("inScope(%q) = true, want false", p)
		}
	}
}

// A tree with one finding of each rule in scope, the same text out of scope,
// and a clean file: the guard fails naming exactly the two in scope, and
// passes once they are repaired.
func TestRunOnATree(t *testing.T) {
	root := t.TempDir()
	put := func(p, text string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restart := "  - |\n    kubectl -n kube-system rollout restart deploy/coredns\n"
	manager := "        kubectl patch hr -n \"$NS\" \"$NAME\" --type=merge -p '{}'\n"
	// A Job Flux applies to every Sovereign (the shape of the sovereign-tls
	// cilium-envoy restart, with Flux's $$ escape).
	fluxJob := "              kubectl rollout restart -n \"$${DS_NS}\" \"ds/$${DS_NAME}\"\n"
	put("infra/providers/_shared/cloudinit-control-plane.tftpl", restart)
	put("platform/flux/chart/templates/recovery.yaml", manager)
	put("clusters/_template/sovereign-tls/restart-job.yaml", fluxJob)
	put("clusters/_template/tests/render.sh", fluxJob)
	put("platform/flux/chart/tests/render.sh", restart+manager)
	put("platform/flux/chart/README.md", restart)
	put("platform/x/chart/templates/clean.yaml", "kubectl -n kube-system get deploy x\n")
	gitInit(t, root)

	var out, errw bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errw); code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out.String(), errw.String())
	}
	for _, want := range []string{
		"FAIL restart infra/providers/_shared/cloudinit-control-plane.tftpl:2:",
		"FAIL manager platform/flux/chart/templates/recovery.yaml:1:",
		"FAIL restart clusters/_template/sovereign-tls/restart-job.yaml:1:",
		"3 findings in 4 files read",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "tests/render.sh") || strings.Contains(out.String(), "README.md") {
		t.Errorf("a file out of scope was judged:\n%s", out.String())
	}
	if strings.Contains(out.String(), "clusters/_template/tests/") {
		t.Errorf("a file out of scope was judged:\n%s", out.String())
	}

	put("infra/providers/_shared/cloudinit-control-plane.tftpl", "  - |\n    M=--field-manager=catalyst-first-boot\n    kubectl -n kube-system patch deploy coredns $M -p '{}'\n")
	put("platform/flux/chart/templates/recovery.yaml", "        kubectl patch hr -n \"$NS\" \"$NAME\" --field-manager=catalyst-helm-release-stuck-recovery --type=merge -p '{}'\n")
	put("clusters/_template/sovereign-tls/restart-job.yaml", "              kubectl patch -n \"$${DS_NS}\" \"ds/$${DS_NAME}\" --field-manager=catalyst-sovereign-tls \\\n                -p '{}'\n")
	out.Reset()
	errw.Reset()
	if code := run([]string{"--root", root}, &out, &errw); code != 0 {
		t.Fatalf("exit %d after the repair, want 0\n%s%s", code, out.String(), errw.String())
	}
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// A tree that holds no file in scope cannot be judged.
func TestNothingReadIsNotAPass(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("kubectl rollout restart x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root)
	var out, errw bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errw); code != 2 {
		t.Fatalf("exit %d, want 2\n%s%s", code, out.String(), errw.String())
	}
}
