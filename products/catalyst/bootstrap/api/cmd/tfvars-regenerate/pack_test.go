package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runAsCommand makes the test binary the command itself: TestMain runs run
// with the binary's arguments instead of the tests (TestZeroChangePlanPack).
const runAsCommand = "TFVARS_REGENERATE_RUN_AS_COMMAND"

func TestMain(m *testing.M) {
	if os.Getenv(runAsCommand) == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// TestZeroChangePlanPack: gate check CLD-G12 (scripts/check-zero-change-plan.sh)
// runs this command in the parent's engine Pod with -providers set to the
// directory that the check packs from the commit under test, and nothing of
// the repository beside it. The test packs that directory with the check
// itself (pack-providers), runs the command as a process of its own from an
// empty directory outside the repository, and requires the round trip of
// TestRoundTrip: exit code 0, no file that is not found, the kept file written
// again unchanged. It fails when the command reads a file below the providers
// directory that the check does not pack: gate run 44857989751 failed so,
// "open .../providers/_contract/contract.json: no such file or directory".
func TestZeroChangePlanPack(t *testing.T) {
	pinEnv(t)
	repo, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	packs := filepath.Join(t.TempDir(), "providers")
	pack := exec.Command("bash", filepath.Join(repo, "scripts", "check-zero-change-plan.sh"), "pack-providers", repo, packs) //nolint:gosec // the check of this repository
	if out, err := pack.CombinedOutput(); err != nil {
		t.Fatalf("the check could not pack the providers directory: %v\n%s", err, out)
	}
	if _, err := os.Stat(packs); err != nil {
		t.Fatalf("the check packed no providers directory: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"huawei", "hetzner"} {
		t.Run(provider, func(t *testing.T) {
			recordPath, work := deployment(t, request(provider))
			want := readFile(t, filepath.Join(work, tfvarsName))
			cmd := exec.Command(self, "-record", recordPath, "-dir", work, "-providers", packs) //nolint:gosec // this test binary, as the command
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), runAsCommand+"=1", "TMPDIR="+t.TempDir())
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if strings.Contains(stderr.String(), "no such file") {
				t.Errorf("the command reads a file that the check does not pack: %s", stderr.String())
			}
			if err != nil {
				t.Fatalf("the command on the packed directory alone: %v, stderr: %s", err, stderr.String())
			}
			if got := readFile(t, filepath.Join(work, tfvarsName)); !bytes.Equal(got, want) {
				t.Errorf("the file written again differs from the kept file; reported: %q", stdout.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("reported differences for an unchanged deployment: %q", stdout.String())
			}
		})
	}
}
