// Command opentofu-runtime holds the runtime config of provider-opentofu
// (container package-runtime of its DeploymentRuntimeConfig) to what the
// Sovereign measured on 27 Nov 2026 (CLD-068): the only worker of the
// Workspace controller held one reconcile for 208099 s after a tofu timeout
// kill, and at 500m the container was throttled in a third of its periods.
// It fails on
//
//	workers   no single --max-reconcile-rate=N with N >= minWorkers()
//	timeout   no single --timeout=D (the liveness probe is bound to it)
//	liveness  not exec /bin/sh -c <script>, or the script, run with a stand-in
//	          curl that serves only metricsURL, fails a reconcile at its
//	          deadline D + 30 s or passes a hold past 2 x D on either Workspace
//	          queue, the measured hold, no Workspace queue or no answer
//	cpu       limits.cpu below N x peakMilli() / maxStretch
//	memory    limits.memory below the resident provider + N x one plan
//	read      the runtime config or its container is not found
//
// Known limits: it reads a subset of YAML as text (block mappings and
// sequences, plain or double-quoted scalars, one literal block scalar; comment
// and blank lines are dropped). It runs the script with the host's sh and awk,
// not the image's busybox. Whether the throttling and the queue fall with these
// settings only the Sovereign can show.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const manifest = "clusters/_template/infrastructure/providers/base/provider-opentofu.yaml"

// Measured on the Sovereign on 27 Nov 2026: provider-opentofu v1.1.11, default
// flags, limits cpu 500m and memory 512Mi.
const (
	workspaces      = 24
	pollSeconds     = 600
	reconcilesDone  = 3452   // reconcile_total requeue_after 3449 + requeue 3
	workedSeconds   = 106491 // container start 24 Nov 01:37:32 to the hold 25 Nov 07:12:23
	heldSeconds     = "208099.401290885"
	periodUsec      = 100000
	oldQuotaUsec    = 50000
	nrThrottled     = 230349
	throttledUsec   = 55872664968
	providerRSSKiB  = 155356
	oldMemLimitKiB  = 512 * 1024 // memory.events max 0 and oom 0 with one plan
	graceSeconds    = 30         // crossplane-runtime v2.0.0 reconcileGracePeriod
	maxStretch      = 2          // the most two plans at the peak may be slowed
	metricsURL      = "http://127.0.0.1:8080/metrics"
	workspaceQueue  = "managed/workspace.opentofu.upbound.io"
	namespacedQueue = "managed/workspace.opentofu.m.upbound.io"
)

// minWorkers: 144 reconciles are due an hour and one worker did 116.7.
func minWorkers() int {
	return int(math.Ceil(float64(workspaces) * 3600 / pollSeconds / (float64(reconcilesDone) * 3600 / workedSeconds)))
}

// peakMilli is what the container asked for in a throttled period: the quota
// it used plus the time it was held back, in millicores.
func peakMilli() float64 {
	return (oldQuotaUsec + float64(throttledUsec)/nrThrottled) * 1000 / periodUsec
}

type line struct {
	ind  int
	text string
}

func linesOf(doc string) []line {
	var out []line
	for _, s := range strings.Split(doc, "\n") {
		if t := strings.TrimSpace(s); t != "" && !strings.HasPrefix(t, "#") {
			out = append(out, line{len(s) - len(strings.TrimLeft(s, " ")), t})
		}
	}
	return out
}

// under is the block of ls[i]: the lines after it that are indented deeper.
func under(ls []line, i int) []line {
	if i < 0 {
		return nil
	}
	j := i + 1
	for j < len(ls) && ls[j].ind > ls[i].ind {
		j++
	}
	return ls[i+1 : j]
}

// entry is the index of `k:`, `k: v` or the item `k` at the shallowest
// indentation of ls, or -1.
func entry(ls []line, k string) int {
	top := math.MaxInt
	for _, l := range ls {
		top = min(top, l.ind)
	}
	for i, l := range ls {
		if l.ind == top && (l.text == k+":" || strings.HasPrefix(l.text, k+": ") || l.text == k) {
			return i
		}
	}
	return -1
}

func path(ls []line, keys ...string) []line {
	for _, k := range keys {
		ls = under(ls, entry(ls, k))
	}
	return ls
}

// items are the entries of a block sequence, the `- ` taken off.
func items(ls []line) []string {
	var out []string
	for _, l := range ls {
		if l.ind == ls[0].ind && strings.HasPrefix(l.text, "- ") {
			out = append(out, strings.TrimPrefix(l.text, "- "))
		}
	}
	return out
}

func scalar(ls []line, k string) string {
	i := entry(ls, k)
	if i < 0 {
		return ""
	}
	v, _, _ := strings.Cut(strings.TrimPrefix(ls[i].text, k+":"), " #")
	return strings.Trim(strings.TrimSpace(v), `"`)
}

// settings are the fields of container package-runtime the rules judge.
type settings struct {
	args                                  []string
	cpu, memory, handler, command, script string
}

func read(text string) (settings, []string) {
	var s settings
	var drc []line
	for _, doc := range strings.Split(text, "\n---\n") {
		if ls := linesOf(doc); entry(ls, "kind: DeploymentRuntimeConfig") >= 0 && scalar(path(ls, "metadata"), "name") == "provider-opentofu" {
			drc = ls
		}
	}
	cs := path(drc, "spec", "deploymentTemplate", "spec", "template", "spec", "containers")
	c := under(cs, entry(cs, "- name: package-runtime"))
	if len(c) == 0 {
		return s, []string{"read: no container package-runtime of a DeploymentRuntimeConfig provider-opentofu in " + manifest}
	}
	s.args = items(path(c, "args"))
	lim := path(c, "resources", "limits")
	s.cpu, s.memory = scalar(lim, "cpu"), scalar(lim, "memory")
	probe := path(c, "livenessProbe")
	for _, h := range []string{"exec", "httpGet", "tcpSocket", "grpc"} {
		if entry(probe, h) >= 0 {
			s.handler += h
		}
	}
	cmd := path(probe, "exec", "command")
	var words []string
	for i, it := range items(cmd) {
		if it != "|" || i != 2 {
			words = append(words, it)
			continue
		}
		block := under(cmd, entry(cmd, "- |"))
		left := math.MaxInt
		for _, l := range block {
			left = min(left, l.ind)
		}
		for _, l := range block {
			s.script += strings.Repeat(" ", l.ind-left) + l.text + "\n"
		}
	}
	s.command = strings.Join(words, " ")
	return s, nil
}

func flag(args []string, name string) (string, int) {
	v, n := "", 0
	for _, a := range args {
		if rest, ok := strings.CutPrefix(a, name+"="); ok {
			v, n = rest, n+1
		}
	}
	return v, n
}

// quantity reads a quantity with the first of the suffixes it ends in (in
// order) and multiplies it by that suffix's factor.
func quantity(q string, suffixes []string, factors ...float64) (float64, bool) {
	for i, suffix := range suffixes {
		if rest, ok := strings.CutSuffix(q, suffix); ok {
			v, err := strconv.ParseFloat(rest, 64)
			return v * factors[i], err == nil
		}
	}
	return 0, false
}

// probeRun runs the script as the kubelet does, with a curl that answers only
// metricsURL; metrics "" means /metrics does not answer. It returns the status.
func probeRun(script, metrics string) (int, error) {
	dir, err := os.MkdirTemp("", "opentofu-runtime-")
	if err != nil {
		return -1, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	stub := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = '" + metricsURL + "' ] && ok=1; done\n" +
		"[ -n \"$ok\" ] && [ -f \"$STUB_METRICS\" ] || exit 7\ncat \"$STUB_METRICS\"\n"
	// #nosec G306 -- the stand-in curl must be executable; it lives in a temporary folder of this run
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(stub), 0o755); err != nil {
		return -1, err
	}
	if metrics != "" {
		if err := os.WriteFile(filepath.Join(dir, "metrics"), []byte(metrics), 0o600); err != nil {
			return -1, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "STUB_METRICS="+filepath.Join(dir, "metrics"))
	var ee *exec.ExitError
	if err = cmd.Run(); errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

func metricsText(workspace, namespaced string) string {
	gauge := "workqueue_longest_running_processor_seconds{controller=%q,name=%q} %s\n"
	m := "# TYPE workqueue_longest_running_processor_seconds gauge\n" + fmt.Sprintf(gauge, namespacedQueue, namespacedQueue, namespaced)
	if workspace != "" {
		m += fmt.Sprintf(gauge, workspaceQueue, workspaceQueue, workspace)
	}
	return m + fmt.Sprintf("workqueue_depth{controller=%q,name=%q} 23\n", workspaceQueue, workspaceQueue)
}

func check(s settings) []string {
	var out []string
	n := minWorkers()
	if v, c := flag(s.args, "--max-reconcile-rate"); c != 1 {
		out = append(out, fmt.Sprintf("workers: %d --max-reconcile-rate arguments; one of at least %d is needed", c, n))
	} else if w, err := strconv.Atoi(v); err != nil || w < n {
		out = append(out, fmt.Sprintf("workers: --max-reconcile-rate=%s; %d reconciles are due an hour, at least %d workers are needed", v, workspaces*3600/pollSeconds, n))
	} else {
		n = w
	}
	var timeout time.Duration
	if v, c := flag(s.args, "--timeout"); c != 1 {
		out = append(out, fmt.Sprintf("timeout: %d --timeout arguments; one is needed, the liveness probe is bound to it", c))
	} else if d, err := time.ParseDuration(v); err != nil || d <= 0 {
		out = append(out, "timeout: --timeout="+v+" is not a positive duration")
	} else {
		timeout = d
	}
	out = append(out, checkProbe(s, timeout)...)
	if v, ok := quantity(s.cpu, []string{"m", ""}, 1, 1000); !ok || v < float64(n)*peakMilli()/maxStretch {
		out = append(out, fmt.Sprintf("cpu: limits.cpu %q; %d workers at the measured peak of %.0fm need at least %.0fm", s.cpu, n, peakMilli(), float64(n)*peakMilli()/maxStretch))
	}
	need := providerRSSKiB + float64(n)*(oldMemLimitKiB-providerRSSKiB)
	if v, ok := quantity(s.memory, []string{"Ki", "Mi", "Gi"}, 1, 1024, 1024*1024); !ok || v < need {
		out = append(out, fmt.Sprintf("memory: limits.memory %q (Ki, Mi or Gi); %d workers need at least %.0f MiB", s.memory, n, need/1024))
	}
	return out
}

func checkProbe(s settings, timeout time.Duration) []string {
	if s.handler != "exec" || s.command != "/bin/sh -c" || s.script == "" {
		return []string{fmt.Sprintf("liveness: handler %q, command %q; an exec probe /bin/sh -c with a literal script is needed, an HTTP probe cannot see a held worker", s.handler, s.command)}
	}
	if timeout == 0 {
		return []string{"liveness: no --timeout to judge the probe against"}
	}
	deadline := strconv.FormatFloat(timeout.Seconds()+graceSeconds, 'f', 1, 64)
	past := strconv.FormatFloat(2*timeout.Seconds()+1, 'f', 1, 64)
	cases := []struct {
		name, metrics string
		pass          bool
	}{
		{"a reconcile of 12.5 s", metricsText("12.5", "0"), true},
		{"a reconcile at its deadline of " + deadline + " s", metricsText(deadline, deadline), true},
		{"the Workspace queue held " + past + " s", metricsText(past, "0"), false},
		{"the namespaced Workspace queue held " + past + " s", metricsText("0", past), false},
		{"the measured hold of " + heldSeconds + " s", metricsText(heldSeconds, "0"), false},
		{"no Workspace queue listed", metricsText("", "0"), false},
		{"/metrics not answering", "", false},
	}
	var out []string
	for _, c := range cases {
		if code, err := probeRun(s.script, c.metrics); err != nil {
			out = append(out, "liveness: "+c.name+": the script did not run: "+err.Error())
		} else if (code == 0) != c.pass {
			out = append(out, fmt.Sprintf("liveness: %s: the probe exits %d, it must %s", c.name, code, map[bool]string{true: "pass", false: "fail"}[c.pass]))
		}
	}
	return out
}

func run(root string, stdout io.Writer) int {
	b, err := os.ReadFile(filepath.Join(root, manifest)) // #nosec G304 -- a file of the tree under judgement
	if err != nil {
		_, _ = fmt.Fprintln(stdout, "opentofu-runtime: "+err.Error())
		return 2
	}
	s, findings := read(string(b))
	if findings == nil {
		findings = check(s)
	}
	for _, f := range findings {
		_, _ = fmt.Fprintln(stdout, "opentofu-runtime: "+f)
	}
	if len(findings) > 0 {
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "opentofu-runtime: PASS %s: %s, limits cpu %s memory %s, the liveness probe ends a held reconcile\n", manifest, strings.Join(s.args, " "), s.cpu, s.memory)
	return 0
}

func main() {
	wd, err := os.Getwd() // tools/guards, where go run starts
	if err != nil {
		wd = "."
	}
	os.Exit(run(filepath.Join(wd, "..", ".."), os.Stdout))
}
