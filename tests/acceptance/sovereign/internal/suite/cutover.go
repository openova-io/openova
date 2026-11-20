package suite

import "strings"

// The hand-over chain (platform/self-sovereign-cutover) changes the subject of
// some checks while it runs. A run of an install whose chain has not completed
// judges every other check, and skips these by the mechanism of a check's
// needs (runChecks): each needs the capability cutover-complete, which the gate
// declares only when region A's ConfigMap catalyst/self-sovereign-cutover-status
// says cutoverComplete true (scripts/gate-parent-job.sh, live_wait). The gate
// commits only the record of such a run; any other record is an artifact of its
// workflow run and never counts as evidence.
//
// The need lives here and not in the check's entry of docs/ledger/gates.yaml: a
// change of `needs` there needs `Work-Package: PLAN` (rule 7 of
// scripts/acceptance-gate.py), and the capability is not in the validator's
// closed list. For the same reason the record's capabilities never list it (a
// committed record is of a complete chain by construction), and a SKIP for it
// is not a SKIP for unmet needs: a required check skipped so fails the run, as
// pass rule 5 of the validator reads such a record.
const CutoverComplete = "cutover-complete"

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

// checkNeeds is what check e needs in a run in runIn: its needs of the
// manifest, and in a run in the cloud (the gate's, the only kind whose record
// counts) cutover-complete when the chain changes its subject.
func checkNeeds(e ManifestCheck, runIn RunIn) []string {
	if _, ok := cutoverSubjects[e.ID]; ok && runIn == RunInCloud {
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
			if chain == "" {
				chain = "not read by the gate"
			}
			reason += " (the hand-over chain: " + chain + "; " + cutoverSubjects[id] + ")"
		}
	}
	return reason
}

// splitCapabilities is the record's list of capabilities and the flags the
// checks see, from the comma list of --capabilities: cutover-complete is a
// flag the checks see and never an entry of the record (see CutoverComplete).
func splitCapabilities(s string) ([]string, map[string]bool) {
	record, caps := []string{}, map[string]bool{}
	for _, c := range splitList(s) {
		caps[c] = true
		if c != CutoverComplete {
			record = append(record, c)
		}
	}
	return record, caps
}

// chainSays names the state of the hand-over chain that the gate's wait read,
// for step D0.11 when ready_at cannot be read.
func chainSays(chain string) string {
	if chain == "" {
		return ""
	}
	return "; the hand-over chain, as the gate's wait read it: " + chain
}
