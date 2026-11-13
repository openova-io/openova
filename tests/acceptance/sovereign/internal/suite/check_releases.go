package suite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check FND-G04, Releases and pods, in every region:
//   - every HelmRelease is Ready and none is stalled. A HelmRelease with
//     spec.suspend true is counted and not judged;
//   - no pod of a platform namespace has a container in CrashLoopBackOff;
//   - no container of a platform namespace has more than maxRestarts restarts.
//
// A platform namespace is a namespace without the Organization label
// (kube.OrganizationLabel). A region that cannot be read fails its steps.
const releasesID = "FND-G04"

// maxRestarts is the highest restart count of one container that passes.
const maxRestarts = 3

type releasesCheck struct{}

func init() { RegisterCheck(releasesCheck{}) }

func (releasesCheck) ID() string { return releasesID }

func (releasesCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	next := func() string { n++; return releasesID + "." + strconv.Itoa(n) }
	for _, region := range cc.Regions {
		rec.Step(next(), "every HelmRelease is Ready and none is stalled in region "+region.ID, func(ev Evidence) error {
			releases, err := region.GetList(ctx, hrResource, "")
			if err != nil {
				return err
			}
			suspended, ready, bad := 0, 0, []string{}
			for _, hr := range releases {
				if suspend, _ := hr.Get("spec", "suspend").(bool); suspend {
					suspended++
					continue
				}
				name := hr.Namespace() + "/" + hr.Name()
				status, reason, _, found := hr.Condition("Ready")
				stalled, _, _, _ := hr.Condition("Stalled")
				switch {
				case stalled == "True":
					bad = append(bad, name+" is stalled")
				case !found:
					bad = append(bad, name+" has no Ready condition")
				case status != "True":
					bad = append(bad, name+" is Ready="+status+" ("+reason+")")
				default:
					ready++
				}
			}
			sort.Strings(bad)
			ev["releases"], ev["suspended"], ev["ready"], ev["not_ready"] = len(releases), suspended, ready, bad
			if len(releases) == 0 {
				return errors.New("no HelmRelease was found: nothing was measured")
			}
			if len(bad) > 0 {
				return fmt.Errorf("%d of %d HelmReleases are not Ready: %s", len(bad), len(releases)-suspended, strings.Join(bad, "; "))
			}
			return nil
		})

		// The pods are read once and judged by the two steps that follow.
		var pods []kube.Object
		var podsErr error
		rec.Step(next(), "no pod of a platform namespace is in CrashLoopBackOff in region "+region.ID, func(ev Evidence) error {
			if pods, podsErr = region.PlatformPods(ctx); podsErr != nil {
				return podsErr
			}
			phases, crashing := map[string]int{}, []string{}
			for _, pod := range pods {
				phases[pod.Str("status", "phase")]++
				for _, c := range containerStatuses(pod) {
					if c.Str("state", "waiting", "reason") == "CrashLoopBackOff" {
						crashing = append(crashing, pod.Namespace()+"/"+pod.Name()+"/"+c.Str("name"))
					}
				}
			}
			sort.Strings(crashing)
			ev["pods"], ev["phases"], ev["crash_looping"] = len(pods), phases, crashing
			if len(pods) == 0 {
				return errors.New("no pod was found in the platform namespaces: nothing was measured")
			}
			if len(crashing) > 0 {
				return fmt.Errorf("%d containers are in CrashLoopBackOff: %s", len(crashing), strings.Join(crashing, "; "))
			}
			return nil
		})
		rec.Step(next(), "no container of a platform namespace has more than "+strconv.Itoa(maxRestarts)+" restarts in region "+region.ID, func(ev Evidence) error {
			if podsErr != nil {
				return podsErr
			}
			restarts, over := map[string]int{}, []string{}
			for _, pod := range pods {
				for _, c := range containerStatuses(pod) {
					count := c.Int("restartCount")
					if count == 0 {
						continue
					}
					name := pod.Namespace() + "/" + pod.Name() + "/" + c.Str("name")
					restarts[name] = count
					if count > maxRestarts {
						over = append(over, name+" ("+strconv.Itoa(count)+")")
					}
				}
			}
			sort.Strings(over)
			ev["pods"], ev["restarts"] = len(pods), restarts
			if len(pods) == 0 {
				return errors.New("no pod was found in the platform namespaces: nothing was measured")
			}
			if len(over) > 0 {
				return fmt.Errorf("%d containers have more than %d restarts: %s", len(over), maxRestarts, strings.Join(over, "; "))
			}
			return nil
		})
	}
	return nil
}

// containerStatuses returns the status of every init container and every
// container of a pod.
func containerStatuses(pod kube.Object) []kube.Object {
	return append(pod.Objects("status", "initContainerStatuses"), pod.Objects("status", "containerStatuses")...)
}
