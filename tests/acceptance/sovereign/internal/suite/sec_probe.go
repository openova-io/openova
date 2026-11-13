package suite

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// The node probe of gate checks SEC-G01 and SEC-G02. In every region it
// creates a namespace of its own, starts one Pod on every control-plane node,
// runs secProbeScript in each Pod with `kubectl exec`, and deletes the
// namespace, also when a step failed. The Pod mounts /var/lib and
// /etc/rancher/k3s of its node read-only and runs as root with no capability.
const (
	secProbeImage   = "harbor.openova.io/proxy-dockerhub/library/alpine:3.20"
	secProbeWait    = "180s" // the longest wait for the probe Pods of a region to be Ready
	secControlPlane = "node-role.kubernetes.io/control-plane=true"
)

// secProbePod is a probe Pod as JSON: its name, namespace, node and image.
const secProbePod = `{"apiVersion": "v1", "kind": "Pod",
  "metadata": {"name": %q, "namespace": %q, "labels": {"app.kubernetes.io/name": "sec-probe"}},
  "spec": {"nodeName": %q, "restartPolicy": "Never", "terminationGracePeriodSeconds": 1,
    "automountServiceAccountToken": false, "enableServiceLinks": false, "tolerations": [{"operator": "Exists"}],
    "containers": [{"name": "probe", "image": %q, "command": ["sleep", "900"],
      "securityContext": {"runAsUser": 0, "runAsNonRoot": false, "allowPrivilegeEscalation": false,
        "readOnlyRootFilesystem": true, "capabilities": {"drop": ["ALL"]}},
      "resources": {"requests": {"cpu": "50m", "memory": "32Mi"}, "limits": {"memory": "128Mi"}},
      "volumeMounts": [{"name": "var-lib", "mountPath": "/host/var/lib", "readOnly": true},
        {"name": "k3s-config", "mountPath": "/host/etc/rancher/k3s", "readOnly": true}]}],
    "volumes": [{"name": "var-lib", "hostPath": {"path": "/var/lib", "type": "Directory"}},
      {"name": "k3s-config", "hostPath": {"path": "/etc/rancher/k3s", "type": "Directory"}}]}}`

// secProbeScript runs in a probe Pod as `sh -s -- /host <mode> <arguments>`,
// with the shell and tools of the image, and alike with sh, tr, grep, cat and
// stat of a CI runner. It prints name=value words; it judges nothing and
// writes nothing. Mode etcd (SEC-G01) takes the etcd key of the canary Secret
// and its value; mode node (SEC-G02) takes nothing.
const secProbeScript = `export LC_ALL=C
root=$1
server=$root/var/lib/rancher/k3s/server
# records FILE: the file with every NUL byte made a line end and every line end
# a space. In the etcd file a key and the start of its value are then one line.
records() { tr '\000\n' '\n ' < "$1"; }
if [ "$2" = etcd ]; then
	db=$server/db/etcd/member/snap/db
	[ -r "$db" ] || { echo db=unreadable; exit 0; }
	echo "db_canary=$(records "$db" | grep -c -F -e "$4")" "db_key=$(records "$db" | grep -c -F -e "$3")" \
		"db_key_secretbox=$(records "$db" | grep -F -e "$3" | grep -c -F -e 'k8s:enc:secretbox:v1:')"
	files=0 canary=0 key=0
	for f in "$server"/db/etcd/member/wal/*.wal; do
		[ -r "$f" ] || continue
		files=$((files + 1))
		canary=$((canary + $(records "$f" | grep -c -F -e "$4")))
		key=$((key + $(records "$f" | grep -c -F -e "$3")))
	done
	echo "wal_files=$files wal_canary=$canary wal_key=$key"
	exit 0
fi
# The token file holds K10<hash of the CA>::server:<token>, or the token alone.
if [ -r "$server/token" ]; then
	part=$(cat "$server/token")
	part=${part##*::server:}
	class=other
	[ -n "$part" ] || class=empty
	printf '%s\n' "$part" | grep -q -x -E '[0-9a-f]{64}' && class=hex64
	echo "token=$class"
else
	echo token=unreadable
fi
mode=missing
[ -e "$root/etc/rancher/k3s/k3s.yaml" ] && mode=$(stat -c %a "$root/etc/rancher/k3s/k3s.yaml")
echo "kubeconfig_mode=$mode"
files=0
for f in "$root"/var/lib/catalyst/*-secret.yaml "$root/var/lib/catalyst/flux-bootstrap.yaml"; do
	[ -e "$f" ] || continue
	files=$((files + 1))
	echo "bootstrap_file=${f#"$root"}"
done
echo "bootstrap_files=$files"
`

// probeValues holds the name=value words of the script, every value of a name in order.
type probeValues map[string][]string

func parseProbe(out []byte) probeValues {
	v := probeValues{}
	for _, word := range strings.Fields(string(out)) {
		if name, value, found := strings.Cut(word, "="); found {
			v[name] = append(v[name], value)
		}
	}
	return v
}

// one returns the last value of name, or "" when the script printed none.
func (v probeValues) one(name string) string {
	if n := len(v[name]); n > 0 {
		return v[name][n-1]
	}
	return ""
}

// secProbe is what a check gives the probe: its identifier, the prefix of the
// namespace (<prefix>-<run>), the mode of the script, what the step of a node
// asserts, and setup, which may be nil: it creates what the script looks for
// in the namespace and returns the arguments of the script after the mode.
type secProbe struct {
	id, prefix, mode, what string
	kubectl                kubectlFunc
	setup                  func(ctx context.Context, region kube.Region, ns string) ([]string, error)
	judge                  func(v probeValues, ev Evidence) error
}

// run records per region one step that starts the probe Pods, one step per
// control-plane node, and one step that deletes the namespace if it was created.
func (p secProbe) run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 0
	for _, region := range cc.Regions {
		ns := p.prefix + "-" + strings.ToLower(NewRunID(time.Now()))
		var nodes, args []string
		created := false
		n++
		started := checkStep(rec, p.id, n, "a probe Pod is Ready on every control-plane node of region "+region.ID, func(ev Evidence) error {
			out, err := p.kubectl(region, ctx, nil, "get", "nodes", "-l", secControlPlane, "-o", "jsonpath={.items[*].metadata.name}")
			if err != nil {
				return err
			}
			nodes = strings.Fields(string(out))
			ev["namespace"], ev["nodes"] = ns, nodes
			if len(nodes) == 0 {
				return fmt.Errorf("region %s has no node with the label %s", region.ID, secControlPlane)
			}
			if _, err := p.kubectl(region, ctx, nil, "create", "namespace", ns); err != nil {
				return err
			}
			created = true
			if p.setup != nil {
				if args, err = p.setup(ctx, region, ns); err != nil {
					return err
				}
			}
			for i, node := range nodes {
				pod := fmt.Sprintf(secProbePod, "probe-"+strconv.Itoa(i), ns, node, secProbeImage)
				if _, err := p.kubectl(region, ctx, strings.NewReader(pod), "-n", ns, "create", "-f", "-"); err != nil {
					return err
				}
			}
			_, err = p.kubectl(region, ctx, nil, "-n", ns, "wait", "--for=condition=Ready", "pod", "--all", "--timeout="+secProbeWait)
			return err
		})
		if !started {
			nodes = nil // no step per node when the Pods are not Ready
		}
		for i, node := range nodes {
			n++
			checkStep(rec, p.id, n, p.what+" on node "+node+" of region "+region.ID, func(ev Evidence) error {
				ev["node"] = node
				script := append([]string{"-n", ns, "exec", "-i", "probe-" + strconv.Itoa(i), "-c", "probe", "--", "sh", "-s", "--", "/host", p.mode}, args...)
				out, err := p.kubectl(region, ctx, strings.NewReader(secProbeScript), script...)
				if err != nil {
					return err
				}
				return p.judge(parseProbe(out), ev)
			})
		}
		if created {
			n++
			checkStep(rec, p.id, n, "the namespace "+ns+" is deleted", func(Evidence) error {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
				defer cancel()
				_, err := p.kubectl(region, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
				return err
			})
		}
	}
	return nil
}
