package suite

import "strings"

// The hand-over chain (platform/self-sovereign-cutover) changes the subject of
// some checks while it runs. When the gate's wait read the chain (--chain, the
// state it read in region A's ConfigMap catalyst/self-sovereign-cutover-status;
// scripts/gate-parent-job.sh, live_wait) and the chain is not complete, a run
// in the cloud judges every other check and skips these by the mechanism of a
// check's needs (runChecks): each needs the flag cutover-complete, which the
// run sets only when that state is complete (runCaps). A run the gate did not
// read the chain for (--chain empty, a run by hand) judges them as before.
//
// The flag comes from --chain and never from --capabilities: a suite built
// before it copies --capabilities into the record unchanged, and the closed
// list of scripts/acceptance-gate.py validate-record refuses the flag; such a
// suite ignores ACCEPT_CHAIN. The need lives here and not in the check's entry
// of docs/ledger/gates.yaml: a change of `needs` there needs
// `Work-Package: PLAN` (rule 7 of scripts/acceptance-gate.py). A SKIP for it is
// not a SKIP for unmet needs: a required check skipped so fails the run, as
// pass rule 5 of the validator reads such a record.
const CutoverComplete = "cutover-complete"

// ChainComplete is the state --chain carries when the chain completed.
const ChainComplete = "complete"

// cutoverSubjects names, for each check whose subject the chain changes, why.
// CTL-G04 is not one: its subject, the policy catalyst-intent-lock, is a
// template of the chart bp-catalyst-platform
// (products/catalyst/chart/templates/intent-lock/), which no step of the chain
// installs.
var cutoverSubjects = map[string]string{
	"FND-G01": "steps 04 to 06 move the registries and sources its pins and digests are compared with",
	"FND-G02": "steps 07 and 08 of the chain write objects it reads",
	"INF-G01": "the reboot agent is held until step 08 releases it",
	"NET-G08": "the hand-over writes the network registry it compares",
}

// checkNeeds is what check e needs in a run in runIn given the chain's state:
// its needs of the manifest, and cutover-complete when the chain changes its
// subject, the run is in the cloud (the gate's, the only kind whose record
// counts) and the gate's wait read the chain.
func checkNeeds(e ManifestCheck, runIn RunIn, chain string) []string {
	if _, ok := cutoverSubjects[e.ID]; ok && runIn == RunInCloud && chain != "" {
		return append(append([]string{}, e.Needs...), CutoverComplete)
	}
	return e.Needs
}

// skipReason is the reason of a check that is not run because the run does not
// declare missing. chain is the state the gate's wait read.
func skipReason(id string, missing []string, chain string) string {
	reason := "the run does not declare " + strings.Join(missing, ", ")
	for _, n := range missing {
		if n == CutoverComplete {
			reason += " (the hand-over chain: " + chain + "; " + cutoverSubjects[id] + ")"
		}
	}
	return reason
}

// runCaps is the flags the checks see: the capabilities the run declares, and
// cutover-complete only when the chain's state is complete (a declared
// cutover-complete is not read; the record lists it as given).
func runCaps(capabilities []string, chain string) map[string]bool {
	caps := map[string]bool{}
	for _, c := range capabilities {
		caps[c] = true
	}
	delete(caps, CutoverComplete)
	if chain == ChainComplete {
		caps[CutoverComplete] = true
	}
	return caps
}

// chainSays names the state of the hand-over chain that the gate's wait read,
// for step D0.11 when ready_at cannot be read.
func chainSays(chain string) string {
	if chain == "" {
		return ""
	}
	return "; the hand-over chain, as the gate's wait read it: " + chain
}
