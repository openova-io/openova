package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var root = filepath.Join("..", "..", "..", "..")

func tree(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, manifest)) // #nosec G304 -- a file of the tree under judgement
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// change replaces each from (which the manifest must hold once) by its to.
func change(t *testing.T, text string, pairs ...string) string {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		if n := strings.Count(text, pairs[i]); n != 1 {
			t.Fatalf("the manifest holds %q %d times, the case needs it once", pairs[i], n)
		}
		text = strings.Replace(text, pairs[i], pairs[i+1], 1)
	}
	return text
}

// httpLiveness puts back the liveness probe of main before CLD-068.
func httpLiveness(t *testing.T, text string) string {
	t.Helper()
	from := strings.Index(text, "              livenessProbe:\n")
	to := strings.Index(text, "                initialDelaySeconds: 30\n")
	if from < 0 || to < from {
		t.Fatal("the manifest has no livenessProbe followed by initialDelaySeconds: 30")
	}
	return text[:from] + "              livenessProbe:\n                httpGet:\n                  path: /metrics\n                  port: 8080\n" + text[to:]
}

func rules(t *testing.T, text string) string {
	t.Helper()
	s, findings := read(text)
	if findings == nil {
		findings = check(s)
	}
	set := map[string]bool{}
	for _, f := range findings {
		set[f[:strings.Index(f, ":")]] = true
	}
	var out []string
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	t.Logf("findings: %q", findings)
	return strings.Join(out, " ")
}

func TestMeasuredArithmetic(t *testing.T) {
	// 144 due an hour / (3452 x 3600 / 106491 = 116.7 an hour) = 1.23, so 2.
	if got := minWorkers(); got != 2 {
		t.Errorf("minWorkers() = %d, want 2", got)
	}
	// (50000 + 55872664968 / 230349) / 100 = 2925.6 millicores.
	if got := peakMilli(); got < 2925 || got > 2926 {
		t.Errorf("peakMilli() = %.1f, want 2925.6", got)
	}
}

// The tree: on main before CLD-068 it fails all five rules (TestEachRuleFails,
// case "main before CLD-068", is that file).
func TestTree(t *testing.T) {
	var out bytes.Buffer
	if code := run(root, &out); code != 0 {
		t.Fatalf("exit %d on the tree:\n%s", code, out.String())
	}
	t.Log(out.String())
}

func TestEachRuleFails(t *testing.T) {
	m := tree(t)
	end := "END { if (seen && !held) exit 0; exit 1 }"
	cases := []struct{ name, text, want string }{
		{"main before CLD-068", httpLiveness(t, change(t, m,
			"              args:\n                - --max-reconcile-rate=2\n                - --timeout=20m\n", "",
			`cpu: "3"`, "cpu: 500m", "memory: 1Gi", "memory: 512Mi")), "cpu liveness memory timeout workers"},
		{"one worker", change(t, m, "- --max-reconcile-rate=2\n", "- --max-reconcile-rate=1\n"), "workers"},
		{"no worker flag", change(t, m, "                - --max-reconcile-rate=2\n", ""), "workers"},
		{"no timeout flag", change(t, m, "                - --timeout=20m\n", ""), "liveness timeout"},
		{"the old cpu limit", change(t, m, `cpu: "3"`, "cpu: 500m"), "cpu"},
		{"2 CPUs, below two plans at the peak over 2", change(t, m, `cpu: "3"`, `cpu: "2"`), "cpu"},
		{"the old memory limit", change(t, m, "memory: 1Gi", "memory: 512Mi"), "memory"},
		{"864Mi, below 872 MiB", change(t, m, "memory: 1Gi", "memory: 864Mi"), "memory"},
		{"an HTTP liveness probe", httpLiveness(t, m), "liveness"},
		{"a probe that always passes", change(t, m, end, "END { exit 0 }"), "liveness"},
		{"a probe that always fails", change(t, m, end, "END { exit 1 }"), "liveness"},
		{"a bound below the deadline", change(t, m, "max=1800", "max=1200"), "liveness"},
		{"a bound past 2 x the timeout", change(t, m, "max=1800", "max=2500"), "liveness"},
		{"a missing Workspace queue read as clean", change(t, m, "seen && !held", "!held"), "liveness"},
		{"the namespaced queue not judged", change(t, m, "if (f[1] + 0 > max)", `if (index($0, "opentofu.upbound.io\"") && f[1] + 0 > max)`), "liveness"},
		{"another port", change(t, m, "127.0.0.1:8080", "127.0.0.1:8081"), "liveness"},
		{"another shell", change(t, m, "                    - /bin/sh\n", "                    - /bin/bash\n"), "liveness"},
		{"no runtime config", change(t, m, "kind: DeploymentRuntimeConfig", "kind: ControllerConfig"), "read"},
		{"no package-runtime", change(t, m, "- name: package-runtime", "- name: sidecar"), "read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rules(t, c.text); got != c.want {
				t.Errorf("rules %q, want %q", got, c.want)
			}
		})
	}
}
