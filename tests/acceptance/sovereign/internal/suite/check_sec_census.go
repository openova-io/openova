package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SEC-G11, Secret census: the Secret objects of the platform namespaces
// that match no entry of the exception register are at most the register's
// allowedUnmatched. In every region the check records one step:
//
//  1. it lists the namespaces labelled openova.io/organization; their
//     Secrets belong to the users of an Organization and are not counted;
//  2. it lists every Secret of the region with `kubectl get secrets
//     --all-namespaces` and a jsonpath that prints the namespace, the name
//     and the type of each, and never a value;
//  3. it runs `scripts/check-secret-object-register.sh --census <file>` of the
//     checkout at RepoRoot on the Secrets of the other namespaces. The script
//     holds the matching rule: it skips the types
//     kubernetes.io/service-account-token, helm.sh/release.v1 and
//     bootstrap.kubernetes.io/token, matches each other Secret against the
//     register at RepoRoot, and prints one JSON line;
//  4. the step fails when no Secret was judged (nothing was measured), or
//     when more Secrets are unmatched than allowedUnmatched. The evidence
//     holds the number and the names of the unmatched Secrets and the ids of
//     the class U and T entries whose reviewBy date is past.
//
// The check only reads: `kubectl get` and a script of the checkout.
const secCensusID = "SEC-G11"

// secCensusScript is the path of the script that holds the matching rule.
const secCensusScript = "scripts/check-secret-object-register.sh"

// secCensusList is the jsonpath of step 2: one line per Secret, three fields
// separated by a tab.
const secCensusList = `jsonpath={range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\t"}{.type}{"\n"}{end}`

// secCensusResult is the JSON line the script prints.
type secCensusResult struct {
	AllowedUnmatched *int     `json:"allowedUnmatched"`
	Judged           int      `json:"judged"`
	Skipped          int      `json:"skipped"`
	Matched          int      `json:"matched"`
	Unmatched        []string `json:"unmatched"`
	PastReview       []string `json:"pastReview"`
}

type secCensusCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(secCensusCheck{kubectl: kube.Region.Run}) }

func (secCensusCheck) ID() string { return secCensusID }

func (c secCensusCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	if cc.RepoRoot == "" {
		return errors.New("the run has no --repo-root, so the register cannot be read")
	}
	for i, region := range cc.Regions {
		checkStep(rec, secCensusID, i+1, "the Secrets of the platform namespaces of region "+region.ID+" that match no register entry are at most allowedUnmatched",
			func(ev Evidence) error { return c.region(ctx, cc.RepoRoot, region, ev) })
	}
	return nil
}

func (c secCensusCheck) region(ctx context.Context, root string, region kube.Region, ev Evidence) error {
	ev["region"] = region.ID
	out, err := c.kubectl(region, ctx, nil, "get", "namespaces", "-l", kube.OrganizationLabel, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return err
	}
	orgs := map[string]bool{}
	for _, name := range strings.Fields(string(out)) {
		orgs[name] = true
	}
	out, err = c.kubectl(region, ctx, nil, "get", "secrets", "--all-namespaces", "-o", secCensusList)
	if err != nil {
		return err
	}
	var list strings.Builder
	read, inOrgs := 0, 0
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("kubectl printed a line that is not a namespace, a name and a type: %q", line)
		}
		read++
		if orgs[parts[0]] {
			inOrgs++
			continue
		}
		list.WriteString(line + "\n")
	}
	ev["secrets_read"], ev["secrets_in_organization_namespaces"] = read, inOrgs
	file, err := os.CreateTemp("", "sec-census-*.tsv")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.WriteString(list.String()); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	code, text, err := runRepoScript(ctx, root, ev, secCensusScript, "--census", file.Name())
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s --census exited %d: %s", secCensusScript, code, lastLine(text))
	}
	var res secCensusResult
	if err := json.Unmarshal([]byte(lastLine(text)), &res); err != nil {
		return fmt.Errorf("%s --census printed no result line: %v", secCensusScript, err)
	}
	if res.AllowedUnmatched == nil {
		return fmt.Errorf("%s --census printed no allowedUnmatched", secCensusScript)
	}
	if res.Unmatched == nil {
		res.Unmatched = []string{}
	}
	if res.PastReview == nil {
		res.PastReview = []string{}
	}
	ev["judged"], ev["skipped_types"], ev["matched"] = res.Judged, res.Skipped, res.Matched
	ev["unmatched_count"], ev["unmatched"], ev["allowed_unmatched"] = len(res.Unmatched), res.Unmatched, *res.AllowedUnmatched
	ev["past_review"] = res.PastReview
	if res.Judged == 0 {
		return errors.New("no Secret of a platform namespace was judged, so nothing was measured")
	}
	if len(res.Unmatched) > *res.AllowedUnmatched {
		return fmt.Errorf("%d Secrets of the platform namespaces match no register entry; the register allows %d", len(res.Unmatched), *res.AllowedUnmatched)
	}
	return nil
}
