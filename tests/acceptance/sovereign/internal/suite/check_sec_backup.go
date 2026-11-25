package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SEC-G10, the OpenBao restore drill, added by SEC-181. In every region
// CronJob openbao-restore-drill of bp-openbao (templates/restore-drill.yaml)
// runs before each backup: with a token minted by the configuration identity
// through token role sec-g10-drill it writes a fresh canary, restores this
// region's first snapshot that is newer than the write from the bucket into a
// scratch OpenBao, and reads the canary back. It records the result in
// ConfigMap openbao/openbao-restore-drill: snapshot (the object key),
// snapshotTime, written and read (the SHA-256 digests of the value written
// and of the value read back), tokenTTLSeconds, tokenRenewable, finished and
// error. The check reads that ConfigMap with `kubectl get` and creates no
// object. The step of a region fails when there is no record (no drill has
// run: nothing was measured), when the drill recorded an error, when the
// restored snapshot is older than 7 hours, when the digest read back is not
// the 64-character digest written, or when the drill token lived longer than
// 15 minutes or was renewable.
const secBackupID = "SEC-G10"

const (
	secBackupRecord = "openbao-restore-drill"
	secBackupMaxAge = 7 * time.Hour
	secBackupMaxTTL = 15 * 60 // seconds
)

var secBackupDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type secBackupCheck struct {
	kubectl kubectlFunc
	now     func() time.Time
}

func init() { RegisterCheck(secBackupCheck{kubectl: kube.Region.Run, now: time.Now}) }

func (secBackupCheck) ID() string { return secBackupID }

func (c secBackupCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	for i, region := range cc.Regions {
		checkStep(rec, secBackupID, i+1, "an OpenBao snapshot of region "+region.ID+" younger than 7 hours was restored and returned the canary written before it",
			func(ev Evidence) error { return c.region(ctx, region, ev) })
	}
	return nil
}

func (c secBackupCheck) region(ctx context.Context, region kube.Region, ev Evidence) error {
	ev["region"] = region.ID
	out, err := c.kubectl(region, ctx, nil, "-n", secBaoNS, "get", "configmap", secBackupRecord, "--ignore-not-found", "-o", "json")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("namespace %s holds no ConfigMap %s: no restore drill has run, nothing was measured", secBaoNS, secBackupRecord)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &cm); err != nil {
		return fmt.Errorf("ConfigMap %s is not JSON: %v", secBackupRecord, err)
	}
	d := cm.Data
	for k, name := range map[string]string{"snapshot": "snapshot", "snapshotTime": "snapshot_time", "written": "written", "read": "read",
		"tokenTTLSeconds": "token_ttl_seconds", "tokenRenewable": "token_renewable", "finished": "finished", "error": "error"} {
		ev[name] = d[k]
	}
	faults := []string{}
	if d["error"] != "" {
		faults = append(faults, "the drill failed: "+d["error"])
	}
	if t, err := time.Parse(time.RFC3339, d["snapshotTime"]); err != nil {
		faults = append(faults, fmt.Sprintf("the drill record holds no snapshot time (%q)", d["snapshotTime"]))
	} else if age := c.now().Sub(t); age > secBackupMaxAge {
		ev["age_minutes"] = int(age.Minutes())
		faults = append(faults, fmt.Sprintf("the restored snapshot %q is %s old, more than 7 hours", d["snapshot"], age.Round(time.Minute)))
	}
	if !secBackupDigest.MatchString(d["written"]) || d["read"] != d["written"] {
		faults = append(faults, fmt.Sprintf("the canary read back from the restored snapshot is not the one written (written %q, read %q)", d["written"], d["read"]))
	}
	if ttl, err := strconv.Atoi(d["tokenTTLSeconds"]); err != nil || ttl <= 0 || ttl > secBackupMaxTTL {
		faults = append(faults, fmt.Sprintf("the drill token lived %q seconds: not measured or more than 15 minutes", d["tokenTTLSeconds"]))
	}
	if d["tokenRenewable"] != "false" {
		faults = append(faults, fmt.Sprintf("the drill token was renewable (%q)", d["tokenRenewable"]))
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}
