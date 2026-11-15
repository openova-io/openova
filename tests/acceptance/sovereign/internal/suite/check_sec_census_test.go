package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// censusFake answers kubectl for check SEC-G11.
type censusFake struct {
	orgs    string // the Organization namespaces, as jsonpath prints them
	secrets string // the Secrets, as the jsonpath of the check prints them
	fail    string // the question that fails, "" for none
}

func (f *censusFake) kubectl(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	if f.fail != "" && strings.HasPrefix(line, f.fail) {
		return nil, errors.New("[" + r.ID + "] kubectl " + line + ": Unable to connect to the server")
	}
	switch line {
	case "get namespaces -l openova.io/organization -o jsonpath={.items[*].metadata.name}":
		return []byte(f.orgs), nil
	case "get secrets --all-namespaces -o " + secCensusList:
		return []byte(f.secrets), nil
	}
	return nil, errors.New("unexpected kubectl " + line)
}

// censusRoot is a checkout whose census script copies its input to seen.tsv
// and prints the content of answer.json, or exits with the code in exit.txt.
func censusRoot(t *testing.T, answer string) string {
	t.Helper()
	root := t.TempDir()
	script := `#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
test "$1" = --census
cat "$2" >> "$root/seen.tsv"
if [ -f "$root/exit.txt" ]; then echo "ERROR secret-object-register: the register is not valid" >&2; exit "$(cat "$root/exit.txt")"; fi
cat "$root/answer.json"
`
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"scripts/check-secret-object-register.sh": script, "answer.json": answer + "\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const censusSecrets = "flux-system\tghcr-pull\tkubernetes.io/dockerconfigjson\n" +
	"acme\tdb\tOpaque\n" +
	"kube-system\tx-token\tkubernetes.io/service-account-token\n"

func runCensus(t *testing.T, f *censusFake, root string) *runner {
	t.Helper()
	return runCheck(t, "sec-census", secCensusCheck{kubectl: f.kubectl}, &CheckContext{RepoRoot: root})
}

func TestSecCensusPasses(t *testing.T) {
	root := censusRoot(t, `{"allowedUnmatched": 3, "judged": 2, "skipped": 1, "matched": 0, "unmatched": ["flux-system/ghcr-pull", "kube-system/a"], "pastReview": ["pull-login"]}`)
	r := runCensus(t, &censusFake{orgs: "acme other", secrets: censusSecrets}, root)
	if got := r.run.Checks[0]; got.Status != Pass {
		t.Fatalf("want PASS, got %+v and failed steps %v", got, failedSteps(r))
	}
	if n := len(r.run.Steps); n != 2 {
		t.Fatalf("%d steps, want one per region", n)
	}
	seen, err := os.ReadFile(filepath.Clean(filepath.Join(root, "seen.tsv")))
	if err != nil {
		t.Fatal(err)
	}
	want := "flux-system\tghcr-pull\tkubernetes.io/dockerconfigjson\nkube-system\tx-token\tkubernetes.io/service-account-token\n"
	if string(seen) != want+want {
		t.Errorf("the script was given %q: the Secrets of the Organization namespace acme must be left out", seen)
	}
	b, err := json.Marshal(r.run.Steps[0].Evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"region":"region-a"`, `"secrets_read":3`, `"secrets_in_organization_namespaces":1`, `"judged":2`,
		`"unmatched_count":2`, `"unmatched":["flux-system/ghcr-pull","kube-system/a"]`, `"allowed_unmatched":3`, `"past_review":["pull-login"]`,
		`"script":"scripts/check-secret-object-register.sh"`} {
		if !strings.Contains(string(b), part) {
			t.Errorf("evidence %s does not hold %s", b, part)
		}
	}
	if r.run.Steps[0].ID != "SEC-G11.1" || r.run.Steps[1].ID != "SEC-G11.2" {
		t.Errorf("step ids %s %s", r.run.Steps[0].ID, r.run.Steps[1].ID)
	}
}

func TestSecCensusFails(t *testing.T) {
	good := `{"allowedUnmatched": 3, "judged": 2, "skipped": 1, "matched": 0, "unmatched": ["a/b"], "pastReview": []}`
	for _, tc := range []struct {
		name, answer, exit string
		fake               censusFake
		want               string
	}{
		{"more unmatched than allowed", `{"allowedUnmatched": 0, "judged": 2, "skipped": 0, "matched": 0, "unmatched": ["a/b", "c/d"], "pastReview": []}`, "",
			censusFake{secrets: censusSecrets}, "2 Secrets of the platform namespaces match no register entry; the register allows 0"},
		{"no Secret judged", `{"allowedUnmatched": 5, "judged": 0, "skipped": 3, "matched": 0, "unmatched": [], "pastReview": []}`, "",
			censusFake{secrets: censusSecrets}, "nothing was measured"},
		{"the script fails", good, "2", censusFake{secrets: censusSecrets}, "--census exited 2: ERROR secret-object-register: the register is not valid"},
		{"the script prints no JSON", "census done", "", censusFake{secrets: censusSecrets}, "printed no result line"},
		{"the script prints no limit", `{"judged": 2, "unmatched": []}`, "", censusFake{secrets: censusSecrets}, "printed no allowedUnmatched"},
		{"the Secrets cannot be listed", good, "", censusFake{secrets: censusSecrets, fail: "get secrets"}, "kubectl get secrets"},
		{"the namespaces cannot be listed", good, "", censusFake{secrets: censusSecrets, fail: "get namespaces"}, "kubectl get namespaces"},
		{"a line without three fields", good, "", censusFake{secrets: "flux-system ghcr-pull Opaque\n"}, "not a namespace, a name and a type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := censusRoot(t, tc.answer)
			if tc.exit != "" {
				if err := os.WriteFile(filepath.Join(root, "exit.txt"), []byte(tc.exit), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f := tc.fake
			r := runCensus(t, &f, root)
			failed := failedSteps(r)
			if r.run.Checks[0].Status != Fail || len(failed) != 2 || !strings.Contains(failed[0], tc.want) {
				t.Errorf("want FAIL in both regions with %q, got %+v and failed steps %q", tc.want, r.run.Checks[0], failed)
			}
		})
	}
}

func TestSecCensusNeedsRegionsAndCheckout(t *testing.T) {
	c := secCensusCheck{kubectl: (&censusFake{}).kubectl}
	if err := c.Run(context.Background(), &CheckContext{RepoRoot: "x"}, nil); err == nil || !strings.Contains(err.Error(), "no region") {
		t.Errorf("no region: got %v", err)
	}
	if err := c.Run(context.Background(), &CheckContext{Regions: []kube.Region{{ID: "a"}}}, nil); err == nil || !strings.Contains(err.Error(), "--repo-root") {
		t.Errorf("no checkout: got %v", err)
	}
}

func TestSecCensusRegistered(t *testing.T) {
	if _, ok := registry[secCensusID]; !ok || secCensusID != "SEC-G11" {
		t.Fatalf("check %s is not registered", secCensusID)
	}
}
