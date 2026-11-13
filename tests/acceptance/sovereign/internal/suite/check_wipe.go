package suite

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Check FND-G13, Clean wipe: the wipe of the deployment before this one left
// nothing behind. The suite does not count the leftovers itself. The run that
// follows a wipe is given two values: --previous-deployment-id, the
// deployment that was wiped, and --previous-wipe-orphans, the number of its
// resources that a listing of the cloud account still found after the wipe.
// The check passes when the previous deployment is named, is not the
// deployment under test, and left 0 orphans. Without the two values it fails;
// it never assumes 0. The manifest gives the check `needs: [previous-wipe]`,
// so a run that does not follow a wipe records it SKIP and does not run it.
const wipeID = "FND-G13"

type wipeCheck struct{}

func init() { RegisterCheck(wipeCheck{}) }

func (wipeCheck) ID() string { return wipeID }

func (wipeCheck) Run(_ context.Context, cc *CheckContext, rec StepRecorder) error {
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
	return nil
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
