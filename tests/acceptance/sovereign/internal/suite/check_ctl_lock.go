package suite

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CTL-G04, the lock holds. The check runs scripts/intent-lock-report.py
// of the checkout at RepoRoot for every region and keeps each report of the
// intent lock as evidence; the script is the rule of the report. A lock with no
// stage at deny refuses nothing, and the check fails. For each kind at deny:
//   - its stage is at zero: no manager but Flux owns spec of one of its
//     objects, and its audit count is 0 or null;
//   - in every region that serves it, the lock refuses a create of a copy of a
//     live object of it (a real create: a dry run is no proof). The copy and
//     its namespace are named ctl-lock-<run>; the check deletes both;
//   - in every region that runs Deployment catalyst-system/catalyst-api, its
//     service account may not create, update, patch or delete it.
const (
	ctlLockID      = "CTL-G04"
	ctlLockScript  = "scripts/intent-lock-report.py"
	ctlLockPolicy  = "catalyst-intent-lock"
	ctlLockRefusal = "is owned by Git" // a part of the message of the policy
)

type ctlLockCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(ctlLockCheck{kubectl: kube.Region.Run}) }

func (ctlLockCheck) ID() string { return ctlLockID }

// lockStage is one stage of the report.
type lockStage struct {
	Stage     string          `json:"stage"`
	Action    string          `json:"action"`
	Resources []string        `json:"resources"`
	Managers  map[string]int  `json:"nonFluxSpecManagers"`
	Audit     json.RawMessage `json:"auditCount"` // a number, or null
}

// report runs the script for one region. raw is the whole report.
func (c ctlLockCheck) report(ctx context.Context, root string, region kube.Region) (raw any, stages []lockStage, err error) {
	cmd := exec.CommandContext(ctx, "python3")
	cmd.Args = append(cmd.Args, filepath.Join(root, filepath.FromSlash(ctlLockScript)), "--kubeconfig", region.Kubeconfig)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("%s for region %s: %v: %s", ctlLockScript, region.ID, err, lastLine(errb.String()))
	}
	var rep struct {
		Stages []lockStage `json:"stages"`
	}
	if err = json.Unmarshal(out.Bytes(), &raw); err == nil {
		err = json.Unmarshal(out.Bytes(), &rep)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s for region %s printed no report: %v", ctlLockScript, region.ID, err)
	}
	return raw, rep.Stages, nil
}

func (c ctlLockCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	stages := map[string][]lockStage{} // region id -> its report
	if !checkStep(rec, ctlLockID, 1, "the lock report of every region is read", func(ev Evidence) error {
		reports := Evidence{}
		ev["reports"] = reports
		for _, region := range cc.Regions {
			raw, s, err := c.report(ctx, cc.RepoRoot, region)
			if err != nil {
				return err
			}
			reports[region.ID], stages[region.ID] = raw, s
			if len(s) == 0 {
				return fmt.Errorf("region %s has no binding of the policy %s: the lock is not installed", region.ID, ctlLockPolicy)
			}
		}
		return nil
	}) {
		return nil
	}
	denied, kinds := map[string][]string{}, []string{} // region id -> its kinds at deny; every kind at deny
	if !checkStep(rec, ctlLockID, 2, "a stage is at deny, and every stage at deny is at zero", func(ev Evidence) error {
		faults := []string{}
		for _, region := range cc.Regions {
			for _, s := range stages[region.ID] {
				if s.Action != "deny" {
					continue
				}
				denied[region.ID], kinds = append(denied[region.ID], s.Resources...), append(kinds, s.Resources...)
				if audit := string(s.Audit); len(s.Managers) > 0 || (audit != "0" && audit != "null") {
					faults = append(faults, fmt.Sprintf("region %s, stage %s: managers other than Flux own spec %v, audit count %s",
						region.ID, s.Stage, s.Managers, audit))
				}
			}
		}
		slices.Sort(kinds)
		kinds = slices.Compact(kinds)
		ev["denied"] = kinds
		if len(kinds) == 0 {
			return errors.New("no stage is at deny in any region: the lock refuses nothing yet")
		}
		if len(faults) > 0 {
			return errors.New(strings.Join(faults, "; "))
		}
		return nil
	}) {
		return nil
	}
	checkStep(rec, ctlLockID, 3, "a create of each kind at deny is refused by the lock", func(ev Evidence) error {
		return c.probeCreates(ctx, cc.Regions, denied, kinds, ev)
	})
	checkStep(rec, ctlLockID, 4, "the account of the API may not write a kind at deny", func(ev Evidence) error {
		return c.askAPIAccount(ctx, cc.Regions, denied, ev)
	})
	return nil
}

// probeCreates tries, in every region that serves a kind at deny, to create a
// copy of a live object of it, taken from the first region that has one.
func (c ctlLockCheck) probeCreates(ctx context.Context, regions []kube.Region, denied map[string][]string, kinds []string, ev Evidence) error {
	name := "ctl-lock-" + strings.ToLower(NewRunID(time.Now()))
	sources, serving := map[string]kube.Object{}, map[string][]kube.Region{}
	for _, region := range regions {
		for _, kind := range denied[region.ID] {
			out, err := c.kubectl(region, ctx, nil, "get", kind, "-A", "-o", "json")
			if err != nil && strings.Contains(err.Error(), notServed) {
				continue
			}
			var list kube.List
			if err == nil {
				err = json.Unmarshal(out, &list)
			}
			if err != nil {
				return fmt.Errorf("read %s in region %s: %w", kind, region.ID, err)
			}
			serving[kind] = append(serving[kind], region)
			if _, ok := sources[kind]; !ok && len(list.Items) > 0 {
				sources[kind] = list.Items[0]
			}
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	refused, faults, made := []string{}, []string{}, map[string]kube.Region{}
	defer func() {
		for _, region := range made {
			_, _ = c.kubectl(region, cleanup, nil, "delete", "namespace", name, "--wait=false", "--ignore-not-found")
		}
	}()
	for _, kind := range kinds {
		source, ok := sources[kind]
		if !ok {
			faults = append(faults, "no region that serves "+kind+" at deny has an object of it to copy: no refusal can be shown")
			continue
		}
		body, meta, del := map[string]any{}, map[string]any{"name": name}, []string{"delete", kind, name, "--wait=false", "--ignore-not-found"}
		for k, v := range source {
			if k != "metadata" && k != "status" {
				body[k] = v
			}
		}
		if source.Namespace() != "" {
			meta["namespace"], del = name, append(del, "-n", name)
		}
		body["metadata"] = meta
		manifest, err := json.Marshal(body)
		if err != nil {
			return err
		}
		for _, region := range serving[kind] {
			if _, done := made[region.ID]; source.Namespace() != "" && !done {
				if _, err := c.kubectl(region, ctx, nil, "create", "namespace", name); err != nil {
					return err
				}
				made[region.ID] = region
			}
			at := kind + " in region " + region.ID
			_, err := c.kubectl(region, ctx, bytes.NewReader(manifest), "create", "-f", "-")
			switch {
			case err == nil:
				_, _ = c.kubectl(region, cleanup, nil, del...)
				faults = append(faults, at+": the copy was created; the lock did not refuse it")
			case strings.Contains(err.Error(), ctlLockPolicy) && strings.Contains(err.Error(), ctlLockRefusal):
				refused = append(refused, at)
			default:
				faults = append(faults, at+": the create failed, but not by the lock: "+err.Error())
			}
		}
	}
	ev["copy"], ev["refused"] = name, refused
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}

// askAPIAccount asks, in every region that runs the API, whether its service
// account may write a kind at deny there.
func (c ctlLockCheck) askAPIAccount(ctx context.Context, regions []kube.Region, denied map[string][]string, ev Evidence) error {
	asked, faults := []string{}, []string{}
	for _, region := range regions {
		out, err := c.kubectl(region, ctx, nil, "get", "deployment", "catalyst-api", "-n", "catalyst-system", "-o", "json")
		if err != nil && strings.Contains(err.Error(), "NotFound") {
			continue
		}
		var api kube.Object
		if err == nil {
			err = json.Unmarshal(out, &api)
		}
		if err != nil {
			return fmt.Errorf("read Deployment catalyst-system/catalyst-api in region %s: %w", region.ID, err)
		}
		user := "system:serviceaccount:catalyst-system:" + cmp.Or(api.Str("spec", "template", "spec", "serviceAccountName"), "default")
		asked = append(asked, region.ID+": "+user)
		for _, kind := range denied[region.ID] {
			for _, verb := range []string{"create", "update", "patch", "delete"} {
				out, err := c.kubectl(region, ctx, nil, "auth", "can-i", verb, kind, "--all-namespaces", "--as", user)
				switch answer := strings.TrimSpace(string(out)); answer {
				case "no":
				case "yes":
					faults = append(faults, fmt.Sprintf("region %s: %s may %s %s", region.ID, user, verb, kind))
				default:
					return fmt.Errorf("region %s: kubectl auth can-i %s %s answered %q: %v", region.ID, verb, kind, answer, err)
				}
			}
		}
	}
	ev["asked"] = asked
	if len(asked) == 0 {
		return errors.New("no region runs Deployment catalyst-system/catalyst-api: the account of the API was not found")
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}
