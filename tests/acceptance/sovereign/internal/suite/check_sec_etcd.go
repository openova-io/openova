package suite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SEC-G01, etcd at rest: a Secret is stored in etcd as secretbox
// ciphertext only. In every region the check creates the Secret
// `etcd-canary` in a namespace `sec-etcd-<run>` of its own. Its one value is
// the canary: `CANARY-` and 40 random hexadecimal characters. On every
// control-plane node the probe (sec_probe.go) then reads the etcd file
// `/var/lib/rancher/k3s/server/db/etcd/member/snap/db` and every file
// `.../member/wal/*.wal`. The step of a node fails when the canary is in one
// of them in the clear, when a record of the canary's key in the etcd file
// does not hold `k8s:enc:secretbox:v1:`, and when the etcd file or every log
// file lacks the key: then nothing was measured. The check looks for the
// canary only: many Secret values, a CA certificate for one, are also stored
// in the clear in objects that are not Secrets.
const secEtcdID = "SEC-G01"

type secEtcdCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(secEtcdCheck{kubectl: kube.Region.Run}) }

func (secEtcdCheck) ID() string { return secEtcdID }

func (c secEtcdCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	return secProbe{id: secEtcdID, prefix: "sec-etcd", mode: "etcd", kubectl: c.kubectl,
		what:  "the etcd files hold the canary Secret as secretbox ciphertext only",
		setup: c.canary, judge: judgeEtcd}.run(ctx, cc, rec)
}

// canary creates the Secret etcd-canary in ns and returns its etcd key and its value.
func (c secEtcdCheck) canary(ctx context.Context, region kube.Region, ns string) ([]string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	value := "CANARY-" + hex.EncodeToString(b[:])
	secret := fmt.Sprintf(`{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "etcd-canary", "namespace": %q}, "stringData": {"canary": %q}}`, ns, value)
	_, err := c.kubectl(region, ctx, strings.NewReader(secret), "-n", ns, "create", "-f", "-")
	return []string{"/registry/secrets/" + ns + "/etcd-canary", value}, err
}

// judgeEtcd judges what the probe printed in the mode etcd.
func judgeEtcd(v probeValues, ev Evidence) error {
	if v.one("db") == "unreadable" {
		ev["db"] = "unreadable"
		return errors.New("the etcd file member/snap/db cannot be read")
	}
	n := map[string]int{}
	for _, name := range []string{"db_canary", "db_key", "db_key_secretbox", "wal_files", "wal_canary", "wal_key"} {
		x, err := strconv.Atoi(v.one(name))
		if err != nil {
			return fmt.Errorf("the probe printed no number %s", name)
		}
		n[name], ev[name] = x, x
	}
	switch {
	case n["db_key"] == 0:
		return errors.New("the etcd file holds no record of the canary Secret: nothing was measured")
	case n["wal_key"] == 0:
		return fmt.Errorf("none of %d write-ahead log files holds the canary Secret: nothing was measured", n["wal_files"])
	case n["db_canary"]+n["wal_canary"] > 0:
		return fmt.Errorf("the value of the canary Secret is in the clear (etcd file: %d, write-ahead log: %d)", n["db_canary"], n["wal_canary"])
	case n["db_key_secretbox"] != n["db_key"]:
		return fmt.Errorf("%d of %d records of the canary Secret in the etcd file are not secretbox ciphertext", n["db_key"]-n["db_key_secretbox"], n["db_key"])
	}
	return nil
}
