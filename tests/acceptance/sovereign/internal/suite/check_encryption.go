package suite

import (
	"context"
	"errors"
	"fmt"
)

// Check FND-G09, Transit encryption: traffic between the regions is
// encrypted on the data path. The check runs
// scripts/check-live-node-encryption.sh of the checkout at RepoRoot once,
// with the kubeconfig of every region, and the script's verdict is the
// check's verdict: exit code 0 passes, any other exit code fails. The script
// exits 2 when a region cannot be read, so a region that cannot be reached
// fails the check. The check does not copy the script: the script is the rule.
const encryptionID = "FND-G09"

// encryptionScript is the path of the script, relative to the checkout.
const encryptionScript = "scripts/check-live-node-encryption.sh"

type encryptionCheck struct{}

func init() { RegisterCheck(encryptionCheck{}) }

func (encryptionCheck) ID() string { return encryptionID }

func (encryptionCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	args, ids := []string{}, []string{}
	for _, region := range cc.Regions {
		args = append(args, "--kubeconfig", region.Kubeconfig)
		ids = append(ids, region.ID)
	}
	rec.Step(encryptionID+".1", "every Cilium agent of every region encrypts as the declared posture says", func(ev Evidence) error {
		ev["regions"] = ids
		code, out, err := runRepoScript(ctx, cc.RepoRoot, ev, encryptionScript, args...)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("%s exited %d: %s", encryptionScript, code, lastLine(out))
		}
		return nil
	})
	return nil
}
