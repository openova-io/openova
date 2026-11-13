package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// probeFake answers kubectl for the two SEC checks. Every exec runs the real
// probe script with sh against a new folder, which stands in for /host and
// holds the host files that host writes.
type probeFake struct {
	t                       *testing.T
	nodes                   string // the answer to get nodes
	waitErr                 error  // the error of kubectl wait
	host                    func(t *testing.T, root string, args []string)
	created, deleted, kinds []string // namespaces created and deleted; kinds created with create -f -
}

func (f *probeFake) kubectl(_ kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	verb := args[0]
	if verb == "-n" {
		verb = args[2]
	}
	switch {
	case verb == "get":
		return []byte(f.nodes), nil
	case verb == "create" && args[1] == "namespace":
		f.created = append(f.created, args[2])
	case verb == "create":
		var o kube.Object
		b, _ := io.ReadAll(stdin)
		if err := json.Unmarshal(b, &o); err != nil || o.Namespace() != args[1] {
			f.t.Errorf("create -f - got an object outside its namespace %s: %s", args[1], b)
		}
		if c := o.Objects("spec", "containers"); o.Str("kind") == "Pod" && (o.Str("spec", "nodeName") == "" || c[0].Str("image") != secProbeImage ||
			c[0].Objects("volumeMounts")[0].Get("readOnly") != true || c[0].Objects("volumeMounts")[1].Get("readOnly") != true) {
			f.t.Errorf("the probe Pod is not pinned to a node, runs another image or mounts a host path writable: %s", b)
		}
		f.kinds = append(f.kinds, o.Str("kind"))
	case verb == "wait":
		return nil, f.waitErr
	case verb == "exec":
		at := 0
		for args[at] != "/host" {
			at++
		}
		root := f.t.TempDir()
		f.host(f.t, root, args[at+2:])
		cmd := exec.Command("sh")
		cmd.Args = append(append(cmd.Args, "-s", "--", root), args[at+1:]...)
		cmd.Stdin = stdin
		return cmd.Output()
	case verb == "delete":
		f.deleted = append(f.deleted, args[2])
	}
	return nil, nil
}

// hostFile writes one file of a fixture with its mode.
func hostFile(t *testing.T, root, path string, mode os.FileMode, data []byte) {
	p := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// etcdRecord is a key and its value as the etcd file stores them: the key,
// two revisions (each holding the byte of a line end) and a version, then
// the value. No NUL byte stands between the key and the value. Both are
// shorter than 128 bytes, so each length is one byte.
func etcdRecord(key string, value []byte) []byte {
	b := append([]byte{0x0a, byte(len(key))}, key...)
	return append(append(b, 0x10, 0x8a, 0x0a, 0x18, 0x8a, 0x0a, 0x20, 0x01, 0x2a, byte(len(value))), value...)
}

// cipher is an encrypted value: its prefix, then bytes with NUL bytes and line ends.
func cipher(prefix string) []byte {
	return append([]byte(prefix), bytes.Repeat([]byte{0x00, 0x0a, 0x9f, 0x41, 0xe3}, 20)...)
}

// etcdHost writes the etcd files of one fixture. args are the key and the canary.
func etcdHost(stored string) func(*testing.T, string, []string) {
	return func(t *testing.T, root string, args []string) {
		key, value, member := args[0], cipher("k8s:enc:secretbox:v1:key1:"), "var/lib/rancher/k3s/server/db/etcd/member/"
		clear, logged := append([]byte("k8s\x00\n\x0c\n\x02v1\x12\x06Secret\x12\x10\x0a\x06canary\x12"), args[1]...), value
		switch stored {
		case "clear":
			value, logged = clear, clear
		case "clear in the log":
			logged = clear
		case "aescbc":
			value, logged = cipher("k8s:enc:aescbc:v1:key1:"), cipher("k8s:enc:aescbc:v1:key1:")
		}
		pad, other := make([]byte, 64), etcdRecord("/registry/secrets/kube-system/other", cipher("k8s:enc:secretbox:v1:key1:"))
		db := append(append(append(append([]byte{}, pad...), other...), pad...), etcdRecord(key, value)...)
		// The write-ahead log holds the compare of the create (the key, then a NUL byte) and its put.
		wal := append(append(append([]byte{}, pad...), 0x1a, byte(len(key))), key...)
		wal = append(append(append(append(wal, 0x30, 0x00, 0x0a, byte(len(key))), key...), 0x12, byte(len(logged))), logged...)
		switch stored {
		case "no key":
			db, wal = other, other
		case "no wal":
			wal = nil
		}
		if stored != "no db" {
			hostFile(t, root, member+"snap/db", 0o600, append(db, pad...))
		}
		if wal != nil {
			hostFile(t, root, member+"wal/0000000000000000-0000000000000000.wal", 0o600, append(wal, pad...))
		}
	}
}

// nodeHost writes the token, the kubeconfig and the bootstrap files of one fixture.
func nodeHost(token string, mode os.FileMode, files ...string) func(*testing.T, string, []string) {
	return func(t *testing.T, root string, _ []string) {
		if token != "" {
			hostFile(t, root, "var/lib/rancher/k3s/server/token", 0o600, []byte(token+"\n"))
		}
		hostFile(t, root, "etc/rancher/k3s/k3s.yaml", mode, []byte("apiVersion: v1\n"))
		for _, f := range append(files, "cilium-values.yaml") {
			hostFile(t, root, "var/lib/catalyst/"+f, 0o600, nil)
		}
	}
}

// probeRun runs one SEC check on two regions the way the runner does. It
// fails the test unless the check passed (want is empty) or failed with a
// reason that holds want, and unless every namespace it created was deleted.
func probeRun(t *testing.T, c Check, f *probeFake, want string) *runner {
	t.Helper()
	cc := &CheckContext{Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateExpectedFail)}}, []Check{c}, cc, true)
	if res := r.run.Checks[0]; (want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, want) {
		t.Errorf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, want)
	}
	if strings.Join(f.created, " ") != strings.Join(f.deleted, " ") {
		t.Errorf("namespaces created %v, deleted %v", f.created, f.deleted)
	}
	return r
}

func TestSecEtcd(t *testing.T) {
	for stored, want := range map[string]string{
		"secretbox":        "",
		"clear":            "the value of the canary Secret is in the clear (etcd file: 1, write-ahead log: 1)",
		"clear in the log": "the value of the canary Secret is in the clear (etcd file: 0, write-ahead log: 1)",
		"aescbc":           "1 of 1 records of the canary Secret in the etcd file are not secretbox ciphertext",
		"no db":            "the etcd file member/snap/db cannot be read",
		"no key":           "the etcd file holds no record of the canary Secret: nothing was measured",
		"no wal":           "none of 0 write-ahead log files holds the canary Secret: nothing was measured",
	} {
		t.Run(stored, func(t *testing.T) {
			f := &probeFake{t: t, nodes: "cp-1 cp-2", host: etcdHost(stored)}
			r := probeRun(t, secEtcdCheck{kubectl: f.kubectl}, f, want)
			if len(r.run.Steps) != 8 || len(f.created) != 2 || strings.Join(f.kinds, " ") != "Secret Pod Pod Secret Pod Pod" {
				t.Errorf("want 8 steps, 2 namespaces and per region one Secret and two Pods, got %d steps, %v, %v", len(r.run.Steps), f.created, f.kinds)
			}
			if ev := r.run.Steps[1].Evidence; stored == "secretbox" && (ev["db_key"] != 1 || ev["wal_key"] != 2) {
				t.Errorf("want the key once in the etcd file and twice in the write-ahead log: %v", ev)
			}
		})
	}
}

func TestSecNode(t *testing.T) {
	random, derived := "K10abc::server:"+strings.Repeat("Zq7x", 16), "K10abc::server:"+strings.Repeat("0f9e", 16)
	for name, c := range map[string]struct {
		host func(*testing.T, string, []string)
		want string
	}{
		"hardened":        {nodeHost(random, 0o600), ""},
		"derived token":   {nodeHost(derived, 0o600), "the server part of the k3s token is 64 lower-case hexadecimal characters"},
		"derived, bare":   {nodeHost(strings.Repeat("0f9e", 16), 0o600), "the server part of the k3s token is 64 lower-case hexadecimal characters"},
		"empty token":     {nodeHost("K10abc::server:", 0o600), "the k3s token file holds no token (empty)"},
		"no token":        {nodeHost("", 0o600), "the k3s token file holds no token (unreadable)"},
		"open kubeconfig": {nodeHost(random, 0o644), "/etc/rancher/k3s/k3s.yaml has mode 644, want 600"},
		"bootstrap files": {nodeHost(random, 0o600, "ghcr-pull-secret.yaml", "flux-bootstrap.yaml"),
			"bootstrap files remain: /var/lib/catalyst/ghcr-pull-secret.yaml, /var/lib/catalyst/flux-bootstrap.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &probeFake{t: t, nodes: "cp-1 cp-2", host: c.host}
			if r := probeRun(t, secNodeCheck{kubectl: f.kubectl}, f, c.want); len(r.run.Steps) != 8 || strings.Join(f.kinds, " ") != "Pod Pod Pod Pod" {
				t.Errorf("want 8 steps and two Pods per region, got %d steps and %v", len(r.run.Steps), f.kinds)
			}
		})
	}
}

func TestSecProbeSetup(t *testing.T) {
	f := &probeFake{t: t, nodes: "", host: nodeHost("", 0o600)}
	if r := probeRun(t, secNodeCheck{kubectl: f.kubectl}, f, "region region-a has no node with the label "+secControlPlane); len(f.created) != 0 || len(r.run.Steps) != 2 {
		t.Errorf("no control-plane node: want no namespace and one step per region, got %v and %d steps", f.created, len(r.run.Steps))
	}
	f = &probeFake{t: t, nodes: "cp-1", waitErr: errors.New("timed out waiting for the condition"), host: nodeHost("", 0o600)}
	if r := probeRun(t, secEtcdCheck{kubectl: f.kubectl}, f, "timed out waiting for the condition"); len(f.deleted) != 2 || len(r.run.Steps) != 4 {
		t.Errorf("Pods that are not Ready: want no step per node and both namespaces deleted, got %v and %d steps", f.deleted, len(r.run.Steps))
	}
	etcd, isEtcd := registry[secEtcdID].(secEtcdCheck)
	node, isNode := registry[secNodeID].(secNodeCheck)
	if !isEtcd || !isNode || etcd.kubectl == nil || node.kubectl == nil {
		t.Errorf("the registry must hold %s and %s, each with kube.Region.Run", secEtcdID, secNodeID)
	}
}
