package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// backupFake answers the one question of check SEC-G10 with a drill record,
// or with nothing when record is empty, as `--ignore-not-found` does.
type backupFake struct{ record string }

func (f backupFake) kubectl(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	if line := strings.Join(args, " "); line != "-n openbao get configmap openbao-restore-drill --ignore-not-found -o json" {
		return nil, errors.New("[" + r.ID + "] unexpected kubectl " + line)
	}
	return []byte(f.record), nil
}

const (
	backupNow    = "2026-11-25T18:20:00Z"
	backupDigest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

// backupRecord is ConfigMap openbao-restore-drill as kubectl prints it.
func backupRecord(snapshotTime, written, read, ttl string) string {
	return fmt.Sprintf(`{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "openbao-restore-drill", "namespace": "openbao"},
  "data": {"snapshot": "openbao/hw-a/20261125T181700Z.snap", "snapshotTime": %q, "written": %q, "read": %q,
    "tokenTTLSeconds": %q, "tokenRenewable": "false", "finished": "2026-11-25T18:18:40Z", "error": ""}}`, snapshotTime, written, read, ttl)
}

func TestSecBackupCheck(t *testing.T) {
	now, err := time.Parse(time.RFC3339, backupNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, record, want string // want "" is PASS
	}{
		{"a drill younger than 7 hours whose canary matches passes", backupRecord("2026-11-25T18:17:00Z", backupDigest, backupDigest, "900"), ""},
		{"a drill older than 7 hours fails", backupRecord("2026-11-25T11:17:00Z", backupDigest, backupDigest, "900"),
			`the restored snapshot "openbao/hw-a/20261125T181700Z.snap" is 7h3m0s old, more than 7 hours`},
		{"a canary read back unlike the one written fails", backupRecord("2026-11-25T18:17:00Z", backupDigest, strings.Repeat("0", 64), "900"),
			"the canary read back from the restored snapshot is not the one written"},
		{"a drill token living longer than 15 minutes fails", backupRecord("2026-11-25T18:17:00Z", backupDigest, backupDigest, "901"),
			`the drill token lived "901" seconds: not measured or more than 15 minutes`},
		{"no drill record fails as not measured", "", "namespace openbao holds no ConfigMap openbao-restore-drill: no restore drill has run, nothing was measured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := secBackupCheck{kubectl: backupFake{record: tc.record}.kubectl, now: func() time.Time { return now }}
			r := runCheck(t, "sec-backup", c, &CheckContext{})
			failed := failedSteps(r)
			if n := len(r.run.Steps); n != 2 {
				t.Fatalf("%d steps, want one per region", n)
			}
			if tc.want == "" {
				if r.run.Checks[0].Status != Pass || len(failed) != 0 {
					t.Fatalf("want PASS, got %+v and failed steps %q", r.run.Checks[0], failed)
				}
				if ev := r.run.Steps[0].Evidence; ev["read"] != backupDigest || ev["token_ttl_seconds"] != "900" || ev["region"] != "region-a" {
					t.Errorf("evidence %v does not hold the record", ev)
				}
				return
			}
			if r.run.Checks[0].Status != Fail || len(failed) != 2 || !strings.Contains(failed[0], tc.want) {
				t.Errorf("want FAIL in both regions with %q, got %+v and failed steps %q", tc.want, r.run.Checks[0], failed)
			}
		})
	}
}

// TestSecBackupCheckEmptyRecord: a record without digests, time or lifetime
// (a drill that stopped before its canary) fails on every one of them.
func TestSecBackupCheckEmptyRecord(t *testing.T) {
	record := `{"data": {"error": "login as openbao-config-admin on auth/kubernetes refused"}}`
	c := secBackupCheck{kubectl: backupFake{record: record}.kubectl, now: time.Now}
	failed := failedSteps(runCheck(t, "sec-backup", c, &CheckContext{}))
	for _, part := range []string{"the drill failed: login as openbao-config-admin", "the drill record holds no snapshot time",
		"is not the one written", `lived "" seconds`, "was renewable"} {
		if len(failed) != 2 || !strings.Contains(failed[0], part) {
			t.Errorf("failed steps %q do not hold %q", failed, part)
		}
	}
	if _, ok := registry[secBackupID]; !ok || secBackupID != "SEC-G10" {
		t.Errorf("check %s is not registered", secBackupID)
	}
}
