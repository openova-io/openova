package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Checks SEC-G03 (the OpenBao seal) and SEC-G05 (the OpenBao audit device),
// added by SEC-162. In every region OpenBao runs as pod openbao-0 of namespace
// openbao (bootstrap-kit slot 08). It opens itself with a key file of its
// control-plane node (contract docs/contracts/c8-seal-provider.md) and
// initialises itself; no Secret holds a key and no Job runs.
//
// The names of the unseal-key Secret and of the root-generation path are
// written in parts, as scripts/secret-guards.py writes them: guard G7 counts
// every line that holds them, and these checks only assert their absence.
//
// SEC-G03. On every control-plane node a probe Pod (sec_probe.go, namespace
// `sec-seal-<run>`) runs as the OpenBao user 100:1000 with only
// /var/lib/catalyst/seal mounted, read-only, and lists the key files: the step
// of a node fails unless there is at least one file openbao-seal-*.key and
// every one has mode 400, owner 100:1000 and 32 bytes. Whether the directory
// is a mount of its own is recorded, not judged: a pack with a seal volume
// does not start k3s without it (exit code 96 of the node start-up). Then, per
// region: none of the four Secrets of the old init flow (secSealSecrets:
// unseal keys, root token, init trace, recovery seed) and no Job exists in namespace
// openbao but those of the CronJobs openbao-backup and openbao-restore-drill; `bao status` in openbao-0 reports initialised, unsealed and a seal
// type other than shamir; a token-free PUT of the root-generation attempt from
// inside the cluster is refused with a 4xx answer; and after `delete pod
// openbao-0` the new pod is Ready and unsealed within 60 seconds. The
// readiness probe of the server is `bao status`, which fails while sealed.
//
// SEC-G05. Per region: ConfigMap openbao-seal-config declares the file audit
// device stdout/ writing to standard output, and the server loads that
// ConfigMap as a configuration directory; then the check reads one path no
// one else reads, secret/data/sec-g05-<run>, token-free (OpenBao answers 403
// and audits the request), and the container log of openbao-0 of the last 60
// seconds must hold the audit response of that path. The log collector reads
// that container log. The check records the request identifier of the line.
// A token-free check cannot list audit devices (sys/audit needs sudo); the
// workflow test-bp-openbao-fresh-prov lists stdout/ with a test-only token.
const (
	secSealID     = "SEC-G03"
	secAuditID    = "SEC-G05"
	secBaoNS      = "openbao"
	secBaoPod     = "openbao-0"
	secBaoBox     = "openbao"
	secBaoRestart = 60 * time.Second
)

// secSealSecrets are the Secrets the old init Job, auth hook and cloud-init
// wrote. secGenerateRoot is the token-free root-generation attempt.
var (
	secSealSecrets  = []string{"openbao-unseal-" + "keys", "openbao-root-token", "openbao-init-trace", "openbao-recovery-seed"}
	secGenerateRoot = "sys/generate-" + "root/attempt"
	// The Jobs of the chart's CronJobs openbao-backup (SEC-180) and
	// openbao-restore-drill (SEC-181) are no leftover of the old init flow.
	secSealScheduledJob = regexp.MustCompile(`^job\.batch/(openbao-backup|openbao-restore-drill)-[0-9]+$`)
)

// secSealPod is the probe Pod of SEC-G03: the OpenBao user and group, no
// capability, only the seal directory of its node, read-only.
const secSealPod = `{"apiVersion": "v1", "kind": "Pod",
  "metadata": {"name": %q, "namespace": %q, "labels": {"app.kubernetes.io/name": "sec-probe"}},
  "spec": {"nodeName": %q, "restartPolicy": "Never", "terminationGracePeriodSeconds": 1,
    "automountServiceAccountToken": false, "enableServiceLinks": false, "tolerations": [{"operator": "Exists"}],
    "containers": [{"name": "probe", "image": %q, "command": ["sleep", "900"],
      "securityContext": {"runAsUser": 100, "runAsGroup": 1000, "runAsNonRoot": true, "allowPrivilegeEscalation": false,
        "readOnlyRootFilesystem": true, "capabilities": {"drop": ["ALL"]}},
      "resources": {"requests": {"cpu": "50m", "memory": "32Mi"}, "limits": {"memory": "128Mi"}},
      "volumeMounts": [{"name": "seal", "mountPath": "/host/var/lib/catalyst/seal", "readOnly": true}]}],
    "volumes": [{"name": "seal", "hostPath": {"path": "/var/lib/catalyst/seal", "type": "Directory"}}]}}`

// secSealScript runs in a seal probe Pod as `sh -s -- /host seal`. It prints
// name=value words and judges nothing: one seal_file=<mode>:<uid>:<gid>:<bytes>
// per key file, their number, and the device of the directory and of the
// pod's /etc/hostname (a file of the kubelet on the node's root disk).
const secSealScript = `export LC_ALL=C
dir=$1/var/lib/catalyst/seal
n=0
for f in "$dir"/openbao-seal-*.key; do
	[ -e "$f" ] || continue
	n=$((n + 1))
	echo "seal_file=$(stat -c '%a:%u:%g:%s' "$f")"
done
echo "seal_files=$n"
echo "seal_dir=$(stat -c '%a:%u:%g' "$dir") seal_dev=$(stat -c '%d' "$dir") root_dev=$(stat -c '%d' /etc/hostname)"
`

type secSealCheck struct{ kubectl kubectlFunc }

type secAuditCheck struct {
	kubectl kubectlFunc
	run     func() string // the run identifier in the probe path
}

func init() {
	RegisterCheck(secSealCheck{kubectl: kube.Region.Run})
	RegisterCheck(secAuditCheck{kubectl: kube.Region.Run, run: func() string { return NewRunID(time.Now()) }})
}

func (secSealCheck) ID() string  { return secSealID }
func (secAuditCheck) ID() string { return secAuditID }

// countingRecorder numbers the steps a check records through it.
type countingRecorder struct {
	StepRecorder
	n *int
}

func (c countingRecorder) Step(id, name string, fn func(ev Evidence) error) {
	*c.n++
	c.StepRecorder.Step(id, name, fn)
}

func (c countingRecorder) Skip(id, name, reason string) {
	*c.n++
	c.StepRecorder.Skip(id, name, reason)
}

func (c secSealCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	if err := (secProbe{id: secSealID, prefix: "sec-seal", mode: "seal", kubectl: c.kubectl,
		pod: secSealPod, script: secSealScript,
		what:  "every seal key file has mode 400, owner 100:1000 and 32 bytes",
		judge: judgeSeal}).run(ctx, cc, countingRecorder{rec, &n}); err != nil {
		return err
	}
	for _, region := range cc.Regions {
		n++
		checkStep(rec, secSealID, n, "no unseal-key, root-token, init-trace or recovery-seed Secret and no Job in namespace openbao of region "+region.ID, func(ev Evidence) error {
			return c.noLeftovers(ctx, region, ev)
		})
		n++
		checkStep(rec, secSealID, n, "OpenBao of region "+region.ID+" is initialised and unsealed with a seal other than shamir", func(ev Evidence) error {
			return c.unsealed(ctx, region, ev)
		})
		n++
		checkStep(rec, secSealID, n, "a token-free PUT "+secGenerateRoot+" inside region "+region.ID+" is refused", func(ev Evidence) error {
			out, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "exec", secBaoPod, "-c", secBaoBox, "--", "bao", "write", "-f", secGenerateRoot)
			return judgeGenerateRoot(out, err, ev)
		})
		n++
		checkStep(rec, secSealID, n, "after its pod is deleted OpenBao of region "+region.ID+" is unsealed within 60 seconds", func(ev Evidence) error {
			return c.restart(ctx, region, ev)
		})
	}
	return nil
}

// judgeSeal judges what the seal probe printed. It names every fault.
func judgeSeal(v probeValues, ev Evidence) error {
	files := v["seal_file"]
	ev["seal_files"], ev["seal_dir"] = files, v.one("seal_dir")
	ev["own_mount"] = v.one("seal_dev") != "" && v.one("seal_dev") != v.one("root_dev")
	if v.one("seal_files") == "" {
		return errors.New("the probe did not finish")
	}
	if len(files) == 0 {
		return errors.New("no key file openbao-seal-*.key in /var/lib/catalyst/seal")
	}
	faults := []string{}
	for _, f := range files {
		if f != "400:100:1000:32" {
			faults = append(faults, "a key file is "+f+", want 400:100:1000:32 (mode:uid:gid:bytes)")
		}
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}

// noLeftovers asks for the four Secrets by name and for the Jobs of the namespace.
func (c secSealCheck) noLeftovers(ctx context.Context, region kube.Region, ev Evidence) error {
	args := append([]string{"-n", secBaoNS, "get", "secret"}, secSealSecrets...)
	out, err := c.kubectl(region, ctx, nil, append(args, "--ignore-not-found", "-o", "name")...)
	if err != nil {
		return err
	}
	jobs, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "get", "jobs", "-o", "name")
	if err != nil {
		return err
	}
	secrets, found, scheduled := strings.Fields(string(out)), []string{}, []string{}
	for _, j := range strings.Fields(string(jobs)) {
		if secSealScheduledJob.MatchString(j) {
			scheduled = append(scheduled, j)
		} else {
			found = append(found, j)
		}
	}
	ev["secrets"], ev["jobs"], ev["scheduled_jobs"] = secrets, found, scheduled
	faults := []string{}
	if len(secrets) > 0 {
		faults = append(faults, "Secrets exist: "+strings.Join(secrets, ", "))
	}
	if len(found) > 0 {
		faults = append(faults, "Jobs exist in namespace openbao: "+strings.Join(found, ", "))
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}

// baoStatus is what `bao status -format=json` prints, the fields the checks read.
type baoStatus struct {
	Type        string `json:"type"`
	Initialized bool   `json:"initialized"`
	Sealed      bool   `json:"sealed"`
	N           int    `json:"n"`
}

// status runs `bao status` in openbao-0. It exits 2 while sealed, so its
// output is read whatever the exit code.
func (c secSealCheck) status(ctx context.Context, region kube.Region) (baoStatus, error) {
	var s baoStatus
	out, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "exec", secBaoPod, "-c", secBaoBox, "--", "bao", "status", "-format=json")
	if jerr := json.Unmarshal(out, &s); jerr != nil {
		if err != nil {
			return s, err
		}
		return s, fmt.Errorf("bao status printed no status: %v", jerr)
	}
	return s, nil
}

func (c secSealCheck) unsealed(ctx context.Context, region kube.Region, ev Evidence) error {
	s, err := c.status(ctx, region)
	if err != nil {
		return err
	}
	ev["type"], ev["initialized"], ev["sealed"], ev["recovery_keys"] = s.Type, s.Initialized, s.Sealed, s.N
	switch {
	case s.Type == "" || s.Type == "shamir":
		return fmt.Errorf("the seal type is %q; a Shamir seal needs an unseal key that someone holds", s.Type)
	case !s.Initialized:
		return errors.New("OpenBao is not initialised")
	case s.Sealed:
		return errors.New("OpenBao is sealed")
	}
	return nil
}

// codeRe finds the HTTP status in an error of the bao CLI ("Code: 405. Errors:").
var codeRe = regexp.MustCompile(`Code: (\d{3})`)

// judgeGenerateRoot passes when the token-free attempt failed with a 4xx answer.
func judgeGenerateRoot(out []byte, err error, ev Evidence) error {
	if err == nil {
		ev["answer"] = strings.TrimSpace(string(out))
		return errors.New("a token-free PUT " + secGenerateRoot + " was accepted")
	}
	m := codeRe.FindStringSubmatch(err.Error())
	if m == nil {
		return fmt.Errorf("the attempt got no answer from OpenBao: %v", err)
	}
	ev["code"] = m[1]
	if !strings.HasPrefix(m[1], "4") {
		return fmt.Errorf("a token-free PUT %s answered %s, want a refusal (4xx)", secGenerateRoot, m[1])
	}
	return nil
}

// restart deletes openbao-0 and waits, with kubectl, for the new pod to be
// Ready, then reads its status. Every wait shares the 60 seconds.
func (c secSealCheck) restart(ctx context.Context, region kube.Region, ev Evidence) error {
	start := time.Now()
	left := func() string {
		return fmt.Sprintf("--timeout=%ds", max(1, int((secBaoRestart-time.Since(start)).Seconds())))
	}
	pod := "pod/" + secBaoPod
	if _, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "delete", pod, "--wait=false"); err != nil {
		return err
	}
	// Each timeout is what is left of the 60 seconds when that wait starts.
	for _, cond := range []string{"--for=delete", "--for=create", "--for=condition=Ready"} {
		if _, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "wait", cond, pod, left()); err != nil {
			return err
		}
	}
	s, err := c.status(ctx, region)
	took := time.Since(start)
	ev["seconds"], ev["sealed"] = int(took.Seconds()), s.Sealed
	switch {
	case err != nil:
		return err
	case s.Sealed:
		return errors.New("the new pod is Ready but sealed")
	case took > secBaoRestart:
		return fmt.Errorf("the new pod was unsealed after %d s, want 60 s at most", int(took.Seconds()))
	}
	return nil
}

func (c secAuditCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 0
	for _, region := range cc.Regions {
		n++
		checkStep(rec, secAuditID, n, "OpenBao of region "+region.ID+" declares the audit device stdout/ on standard output", func(ev Evidence) error {
			return c.declared(ctx, region, ev)
		})
		n++
		checkStep(rec, secAuditID, n, "a token-free request of the check is in the audit output of region "+region.ID+" within 60 seconds", func(ev Evidence) error {
			return c.audited(ctx, region, ev)
		})
	}
	return nil
}

func (c secAuditCheck) declared(ctx context.Context, region kube.Region, ev Evidence) error {
	hcl, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "get", "configmap", "openbao-seal-config", "-o", `jsonpath={.data.audit\.hcl}`)
	if err != nil {
		return err
	}
	// The running pod, not the StatefulSet template: with updateStrategy OnDelete
	// the pod can still carry an older spec than its template.
	args, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "get", "pod", secBaoPod, "-o", `jsonpath={.spec.containers[?(@.name=="`+secBaoBox+`")].args[0]}`)
	if err != nil {
		return err
	}
	text := string(hcl)
	ev["audit_hcl"] = strings.TrimSpace(text)
	faults := []string{}
	if !strings.Contains(text, `audit "file" "stdout" {`) || !strings.Contains(text, `file_path = "stdout"`) {
		faults = append(faults, "ConfigMap openbao-seal-config declares no file audit device stdout/ writing to stdout")
	}
	if !strings.Contains(string(args), "-config=/openbao/userconfig/openbao-seal-config/") {
		faults = append(faults, "the server does not load /openbao/userconfig/openbao-seal-config/")
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}

// auditLine is the part of an audit entry the check reads.
type auditLine struct {
	Type    string `json:"type"`
	Request struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"request"`
}

func (c secAuditCheck) audited(ctx context.Context, region kube.Region, ev Evidence) error {
	path := "secret/data/sec-g05-" + strings.ToLower(c.run())
	ev["path"] = path
	_, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "exec", secBaoPod, "-c", secBaoBox, "--", "bao", "read", path)
	if err == nil {
		return fmt.Errorf("a token-free read of %s was answered; want a refusal", path)
	}
	if m := codeRe.FindStringSubmatch(err.Error()); m == nil || m[1] != "403" {
		return fmt.Errorf("the token-free read of %s got no 403 from OpenBao: %v", path, err)
	}
	logs, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "logs", secBaoPod, "-c", secBaoBox, "--since=60s")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(logs), "\n") {
		var a auditLine
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &a) != nil {
			continue
		}
		if a.Type == "response" && a.Request.Path == path {
			ev["request_id"] = a.Request.ID
			return nil
		}
	}
	return fmt.Errorf("no audit response of %s in the log of %s of the last 60 seconds", path, secBaoPod)
}
