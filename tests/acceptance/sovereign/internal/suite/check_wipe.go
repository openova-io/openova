package suite

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Check FND-G13, Clean wipe: the wipe of the deployment before this one left
// nothing behind, and the quota headroom was measured before this deployment
// was fired. The suite does not count the cloud account itself. The run that
// follows a wipe is given three values: --previous-deployment-id, the
// deployment that was wiped; --previous-wipe-orphans, the number of its
// resources that a listing of the cloud account still found after the wipe;
// and --preflight, what the flight checklist counted per kind of resource
// before the fire.
//
// Step FND-G13.1 passes when the previous deployment is named, is not the
// deployment under test, and left 0 orphans. Step FND-G13.2 passes when
// --preflight was given and all of this holds:
//   - every kind of wipeKinds has a count of leftovers, and no kind has a
//     count above 0;
//   - every kind of quotaKinds has a used and an allowed count, and no kind
//     has more used than allowed;
//   - the numbers were read before the environment was created (the earliest
//     creation of the namespace kube-system in the regions).
//
// A value that was not given, a number that was not measured ("-") and a
// creation time that cannot be read fail the step; nothing is assumed. The
// manifest gives the check `needs: [previous-wipe]`, so a run that does not
// follow a wipe records it SKIP and does not run it.
const wipeID = "FND-G13"

// wipeKinds are the kinds that the manifest entry of the check names. The
// first seven are counted by listing the cloud account (the kinds of
// providers.LeftoverKind); dns_records are the records of the wiped
// deployment in its parent zone.
var wipeKinds = []string{"servers", "volumes", "addresses", "load_balancers", "networks", "security_groups", "buckets", "dns_records"}

// quotaKinds are the kinds whose headroom the pre-flight gate
// (scripts/prov-preflight.sh) always measures and refuses a fire on.
var quotaKinds = []string{"networks", "volumes"}

type wipeCheck struct{}

func init() { RegisterCheck(wipeCheck{}) }

func (wipeCheck) ID() string { return wipeID }

func (wipeCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	rec.Step(wipeID+".1", "the wipe of the previous deployment left no orphan", func(ev Evidence) error {
		d := cc.Deploy
		if d == nil || d.PreviousDeploymentID == "" {
			return errors.New("the run did not name the previous deployment (--previous-deployment-id)")
		}
		ev["deployment_id"], ev["previous_deployment_id"] = d.ID, d.PreviousDeploymentID
		if d.PreviousWipeOrphans < 0 {
			return fmt.Errorf("the run did not say how many orphans the wipe of deployment %s left (--previous-wipe-orphans)", d.PreviousDeploymentID)
		}
		ev["previous_wipe_orphans"] = d.PreviousWipeOrphans
		if d.PreviousDeploymentID == d.ID {
			return fmt.Errorf("the previous deployment %s is the deployment under test", d.ID)
		}
		if d.PreviousWipeOrphans != 0 {
			return fmt.Errorf("the wipe of deployment %s left %d orphans", d.PreviousDeploymentID, d.PreviousWipeOrphans)
		}
		return nil
	})
	rec.Step(wipeID+".2", "the quota headroom was measured before the fire, and every kind was counted with nothing left", func(ev Evidence) error {
		if cc.Deploy == nil || cc.Deploy.Preflight == nil {
			return errors.New("the run did not give the numbers of the flight checklist (--preflight)")
		}
		pre := cc.Deploy.Preflight
		ev["measured_at"] = pre.MeasuredAt.Format(time.RFC3339)
		problems := preflightProblems(pre, ev)
		var born time.Time
		for _, reg := range cc.Regions {
			t, err := kubeSystemCreated(ctx, reg)
			if err != nil {
				problems = append(problems, "the creation of the environment was not read: "+err.Error())
				born = time.Time{}
				break
			}
			if born.IsZero() || t.Before(born) {
				born = t
			}
		}
		if !born.IsZero() {
			ev["environment_created_at"] = born.Format(time.RFC3339)
			if !pre.MeasuredAt.Before(born) {
				problems = append(problems, fmt.Sprintf("the numbers were read at %s, not before the environment was created at %s",
					pre.MeasuredAt.Format(time.RFC3339), born.Format(time.RFC3339)))
			}
		}
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "; "))
		}
		return nil
	})
	return nil
}

// preflightProblems judges the numbers of the flight checklist. It fills ev
// with the count of leftovers of every kind, their sum, and the free count
// (allowed minus used) of every kind that has both, and returns one line for
// each rule that does not hold.
func preflightProblems(pre *Preflight, ev Evidence) []string {
	problems, byKind := []string{}, map[string]PreflightKind{}
	left, free, total := map[string]int{}, map[string]int{}, 0
	for _, k := range pre.Kinds {
		byKind[k.Kind] = k
		if k.Leftovers != nil {
			left[k.Kind] = *k.Leftovers
			total += *k.Leftovers
			if *k.Leftovers > 0 {
				problems = append(problems, fmt.Sprintf("%s: the wipe left %d", k.Kind, *k.Leftovers))
			}
		}
		if k.Used != nil && k.Allowed != nil {
			free[k.Kind] = *k.Allowed - *k.Used
			if *k.Used > *k.Allowed {
				problems = append(problems, fmt.Sprintf("%s: %d used of %d allowed", k.Kind, *k.Used, *k.Allowed))
			}
		}
	}
	ev["leftovers"], ev["leftover_count"], ev["free"] = left, total, free
	for _, kind := range wipeKinds {
		if byKind[kind].Leftovers == nil {
			problems = append(problems, kind+": the leftovers were not counted")
		}
	}
	for _, kind := range quotaKinds {
		if byKind[kind].Used == nil || byKind[kind].Allowed == nil {
			problems = append(problems, kind+": the used and the allowed count were not both measured")
		}
	}
	return problems
}

// parsePreviousWipe reads the values of --previous-deployment-id and
// --previous-wipe-orphans. Both are given, or neither. When neither is given
// it returns "" and -1: the run did not say. The number of orphans is a whole
// number of 0 or more.
func parsePreviousWipe(id, orphans string) (string, int, error) {
	id, orphans = strings.TrimSpace(id), strings.TrimSpace(orphans)
	switch {
	case id == "" && orphans == "":
		return "", -1, nil
	case id == "":
		return "", -1, errors.New("--previous-wipe-orphans is given without --previous-deployment-id")
	case orphans == "":
		return "", -1, errors.New("--previous-deployment-id is given without --previous-wipe-orphans")
	}
	n, err := strconv.Atoi(orphans)
	if err != nil || n < 0 {
		return "", -1, fmt.Errorf("--previous-wipe-orphans: %q is not a whole number of 0 or more", orphans)
	}
	return id, n, nil
}

var preflightKindName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// parsePreflight reads the value of --preflight:
//
//	<UTC time>;<kind>=<used>/<allowed>/<left>,<kind>=<used>/<allowed>/<left>
//
// The time is when the numbers were read, such as 2026-11-20T05:55:00Z. A
// kind is a lower-case name such as networks, and is given once. Each of the
// three numbers is a whole number of 0 or more, or "-" when it was not
// measured: how many the account held, how many it may hold, and how many
// the wiped deployment left. An empty value returns nil: the run did not say.
func parsePreflight(v string) (*Preflight, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	bad := func(format string, a ...any) (*Preflight, error) {
		return nil, fmt.Errorf("--preflight: "+format, a...)
	}
	at, list, found := strings.Cut(v, ";")
	if !found {
		return bad("%q is not <UTC time>;<kind>=<used>/<allowed>/<left>,...", v)
	}
	when, err := time.Parse("2006-01-02T15:04:05Z", strings.TrimSpace(at))
	if err != nil {
		return bad("%q is not a UTC time such as 2026-11-20T05:55:00Z", strings.TrimSpace(at))
	}
	pre := &Preflight{MeasuredAt: when, Kinds: []PreflightKind{}}
	seen := map[string]bool{}
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		kind, numbers, found := strings.Cut(item, "=")
		parts := strings.Split(numbers, "/")
		if !found || !preflightKindName.MatchString(kind) || len(parts) != 3 {
			return bad("%q is not <kind>=<used>/<allowed>/<left>", item)
		}
		if seen[kind] {
			return bad("the kind %s is given twice", kind)
		}
		seen[kind] = true
		entry := PreflightKind{Kind: kind}
		for i, field := range []**int{&entry.Used, &entry.Allowed, &entry.Leftovers} {
			if parts[i] == "-" {
				continue
			}
			n, err := strconv.Atoi(parts[i])
			if err != nil || n < 0 || strconv.Itoa(n) != parts[i] {
				return bad("%q of %s is not a whole number of 0 or more, or -", parts[i], kind)
			}
			*field = &n
		}
		pre.Kinds = append(pre.Kinds, entry)
	}
	return pre, nil
}
