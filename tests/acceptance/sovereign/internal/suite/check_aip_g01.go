package suite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Check AIP-G01, No header-trusting meter: no Pod that has not ended, in any
// namespace of any region, has a container whose image reference holds the text
// meterImageText. The metering sidecar of bp-newapi took the customer and
// the Organization of a model call from two request headers that any caller
// can set, so its usage records could be charged to anyone. It is switched
// off since AIP-040.
//
// The rule matches the text anywhere in the image reference, not only the
// last path segment: the image is named services-metering-sidecar, and after a
// hand-over the registry host changes while the name does not. Every
// container of a Pod is judged: its init containers, its containers and its
// ephemeral containers. A region whose Pods hold no container fails: nothing
// was measured, and that is never a pass.
//
// Only a Pod that has not ended is judged. A Pod in phase Failed or Succeeded
// (meterEndedPhases) runs no container: an evicted Pod stays in the list in
// phase Failed until it is deleted, and it would fail the check for a sidecar
// that runs nowhere. Every other phase is judged, an empty one too: Pending
// (the container is about to run), Running, and Unknown (the node does not
// answer, so the container may still run). The containers of an ended Pod are
// not counted, so a region whose every Pod has ended fails as unmeasured.
const meterImageID = "AIP-G01"

// meterImageText is the text that no image reference of a Pod may hold.
const meterImageText = "metering-sidecar"

// meterContainerLists are the lists of a Pod spec that hold containers.
var meterContainerLists = []string{"initContainers", "containers", "ephemeralContainers"}

// meterEndedPhases are the phases of a Pod whose containers have all stopped
// for good.
var meterEndedPhases = map[string]bool{"Failed": true, "Succeeded": true}

type meterImageCheck struct{}

func init() { RegisterCheck(meterImageCheck{}) }

func (meterImageCheck) ID() string { return meterImageID }

func (meterImageCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	for i, region := range cc.Regions {
		rec.Step(meterImageID+"."+strconv.Itoa(i+1), "no container runs a metering-sidecar image in region "+region.ID, func(ev Evidence) error {
			pods, err := region.GetList(ctx, "pods", "")
			if err != nil {
				return err
			}
			images, ended, found := 0, 0, []string{}
			for _, pod := range pods {
				if meterEndedPhases[pod.Str("status", "phase")] {
					ended++
					continue
				}
				for _, list := range meterContainerLists {
					for _, c := range pod.Objects("spec", list) {
						images++
						if ref := c.Str("image"); strings.Contains(ref, meterImageText) {
							found = append(found, pod.Namespace()+"/"+pod.Name()+"/"+c.Str("name")+" "+ref)
						}
					}
				}
			}
			sort.Strings(found)
			ev["pods"], ev["ended"], ev["images"], ev["found"] = len(pods), ended, images, found
			switch {
			case images == 0:
				return fmt.Errorf("no container was found in region %s: nothing was measured", region.ID)
			case len(found) > 0:
				return fmt.Errorf("%d containers in region %s run an image whose name holds %q: %s",
					len(found), region.ID, meterImageText, strings.Join(found, "; "))
			}
			return nil
		})
	}
	return nil
}
