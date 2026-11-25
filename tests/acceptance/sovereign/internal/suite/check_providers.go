package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CLD-G07. GET /api/v1/providers?include=all lists exactly, by id, the
// packs of /infra/providers in Deployment catalyst-system/catalyst-api whose
// profile does not set requiresInstallation, and <spec.extends>/<name> per
// ConfigMap catalyst-system/cloud-installation-<name> labelled
// catalyst.openova.io/cloud-installation=true; every region must give the
// same list, as either may answer. The clause "a create request without
// credentials passes validation for an operator-credential pack" is not
// judged here: CLD-106 reads the operator's credentials and adds that step.
const providersID = "CLD-G07"

// providersScript prints "<profile path> <1 if it requires an installation, else 0>" per pack.
const providersScript = `for f in /infra/providers/[!_]*/profile.yaml; do printf '%s ' "$f"; grep -c '^  requiresInstallation: true' "$f" || true; done`

var providersPackLine = regexp.MustCompile(`^/infra/providers/([^/]+)/profile\.yaml ([01])$`)
var providersExtends = regexp.MustCompile(`(?m)^  extends: *([a-z0-9-]+) *$`)

type providersCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(providersCheck{kubectl: kube.Region.Run}) }

func (providersCheck) ID() string { return providersID }

func (c providersCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if cc.API == nil || len(cc.Regions) == 0 {
		return errors.New("the run has no console session or no region")
	}
	rec.Step(providersID+".1", "GET /api/v1/providers?include=all lists exactly the packs of the image and the installation documents of the cluster", func(ev Evidence) error {
		var want []string
		for i, r := range cc.Regions {
			ids, err := c.expected(ctx, r)
			if err != nil {
				return err
			}
			if i == 0 {
				want, ev["expected"] = ids, ids
			} else if !reflect.DeepEqual(ids, want) {
				return fmt.Errorf("the regions disagree: [%s] has %v, [%s] has %v", cc.Regions[0].ID, want, r.ID, ids)
			}
		}
		var doc struct {
			Providers []struct{ ID, Provider, Installation string } `json:"providers"`
		}
		if _, err := cc.API.Get(ctx, "/api/v1/providers?include=all", &doc); err != nil {
			return err
		}
		got := []string{}
		for _, p := range doc.Providers {
			id := p.Provider
			if p.Installation != "" {
				id += "/" + p.Installation
			}
			if p.ID != id {
				return fmt.Errorf("the entry with id %q has provider %q and installation %q", p.ID, p.Provider, p.Installation)
			}
			got = append(got, id)
		}
		sort.Strings(got)
		ev["listed"] = got
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("the route lists %v; the image and the cluster give %v", got, want)
		}
		return nil
	})
	return nil
}

// expected returns the ids that region r gives, sorted.
func (c providersCheck) expected(ctx context.Context, r kube.Region) ([]string, error) {
	out, err := c.kubectl(r, ctx, nil, "-n", "catalyst-system", "exec", "deploy/catalyst-api", "--", "sh", "-c", providersScript)
	if err != nil {
		return nil, fmt.Errorf("[%s] the packs of the image: %v", r.ID, err)
	}
	ids := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		m := providersPackLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("[%s] the packs of the image: the line %q names no pack", r.ID, line)
		}
		if m[2] == "0" {
			ids = append(ids, m[1])
		}
	}
	out, err = c.kubectl(r, ctx, nil, "get", "configmaps", "-n", "catalyst-system", "-l", "catalyst.openova.io/cloud-installation=true", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("[%s] the installation documents: %v", r.ID, err)
	}
	var l kube.List
	if err := json.Unmarshal(out, &l); err != nil {
		return nil, fmt.Errorf("[%s] the installation ConfigMaps are not JSON: %v", r.ID, err)
	}
	for _, cm := range l.Items {
		name, named := strings.CutPrefix(cm.Name(), "cloud-installation-")
		m := providersExtends.FindStringSubmatch(cm.Str("data", "profile.yaml"))
		if !named || m == nil {
			return nil, fmt.Errorf("[%s] ConfigMap catalyst-system/%s is not named cloud-installation-<name> or its profile.yaml has no spec.extends", r.ID, cm.Name())
		}
		ids = append(ids, m[1]+"/"+name)
	}
	sort.Strings(ids)
	return ids, nil
}
