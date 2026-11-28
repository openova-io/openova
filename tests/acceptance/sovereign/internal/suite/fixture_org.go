package suite

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The helper of a test Organization (FND-038). A check that needs an
// Organization with a Blueprint installed calls EnsureOrganization,
// InstallBlueprint and RemoveOrganization instead of writing its own set-up.
// Each goes through the console API (cc.API), never kubectl, and waits within
// the limits of fixtureWaits until the API reports the object ready or gone.
//
// The three routes that change state, from contract C9
// (docs/contracts/C9-mutating-routes.yaml), and the body of each call:
//
//	POST   /api/v1/organizations        {"subdomain": slug, "admin_email": slug+"@acceptance.invalid", "kind": "internal", "plan_slug": "m"}, answers 202
//	POST   /catalyst/v1/apps/instances  {"blueprint", "org", "environment", "name", "topology": "singleton", "placement": {"regions": [the first region]}, "values"}, answers 201
//	DELETE /api/v1/organizations/{id}   no body, answers 2xx
//
// The reads: GET /api/v1/organizations (is the slug there, and its id),
// GET /api/v1/organizations/{id} (state done; failed ends the wait),
// GET /api/v1/refs/organizations/{slug}/environments (the first Environment),
// GET /api/v1/sovereigns/{deployment}/applications/{name}?namespace={slug}
// (Ready=True), GET /api/v1/organizations/{slug} (HTTP 404 with the error
// org-tenant-not-found: the Organization CR is gone) and
// GET /api/v1/sovereigns/{deployment}/applications (no Application is left in
// the namespace {slug}). An Application lives in the namespace of its
// Organization, which is the slug (step 5 of validateInstallRefs,
// products/catalyst/bootstrap/api/internal/handler/install_refs.go).
//
// Only an Organization whose slug starts with "gate-" is created, installed
// into or removed. A run without a console session (cc.API nil) gets an
// error: there is no other way in.

// fixturePlan is the plan of a test Organization: plan m, the plan that journey
// ISO gives the Organization in which it installs an Application (isoPlanA).
// Plan s holds no WordPress with its database.
const fixturePlan = "m"

// fixtureWaits are the longest waits of the helper and the pause between two
// reads: the waits of journey ISO.
var fixtureWaits = struct{ org, app, gone, every time.Duration }{
	org: 30 * time.Minute, app: 25 * time.Minute, gone: 15 * time.Minute, every: 10 * time.Second}

// fixtureSlugRE is a slug of the gate: "gate-" and the rest of a DNS label.
var fixtureSlugRE = regexp.MustCompile(`^gate-[a-z0-9]([a-z0-9-]{0,56}[a-z0-9])?$`)

// fixtureOrgRow is an Organization as GET /api/v1/organizations answers it.
type fixtureOrgRow struct {
	ID        string `json:"org_tenant_id"`
	Subdomain string `json:"subdomain"`
	State     string `json:"state"`
	LastError string `json:"last_error"`
}

func fixtureRefuse(cc *CheckContext, slug string) error {
	switch {
	case cc == nil || cc.API == nil:
		return errors.New("the helper of a test Organization goes through the console API, and the run has no console session")
	case !fixtureSlugRE.MatchString(slug):
		return fmt.Errorf("the slug %q is not a slug of the gate: it starts with gate- and is a DNS label", slug)
	}
	return nil
}

// fixtureFind returns the Organization of slug that is not deleted, or nil.
func fixtureFind(ctx context.Context, cc *CheckContext, slug string) (*fixtureOrgRow, error) {
	var list struct {
		Items []fixtureOrgRow `json:"items"`
	}
	if _, err := cc.API.Get(ctx, "/api/v1/organizations", &list); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if r := list.Items[i]; r.Subdomain == slug && r.State != "deleted" {
			if r.ID == "" {
				return nil, fmt.Errorf("GET /api/v1/organizations lists %s without an org_tenant_id", slug)
			}
			return &r, nil
		}
	}
	return nil, nil
}

// EnsureOrganization creates the Organization slug, unless it exists, and
// waits until its state is done. When it exists and is done, nothing is sent
// but the list.
func EnsureOrganization(ctx context.Context, cc *CheckContext, slug string) error {
	if err := fixtureRefuse(cc, slug); err != nil {
		return err
	}
	row, err := fixtureFind(ctx, cc, slug)
	if err != nil {
		return err
	}
	if row == nil {
		body := map[string]any{"subdomain": slug, "admin_email": slug + "@acceptance.invalid", "kind": "internal", "plan_slug": fixturePlan}
		res, err := cc.API.Do(ctx, http.MethodPost, "/api/v1/organizations", body)
		if err != nil {
			return err
		}
		if res.Status != http.StatusAccepted {
			return fmt.Errorf("POST /api/v1/organizations %s: HTTP %d (want 202): %s", slug, res.Status, trunc(string(res.Body), 300))
		}
		row = &fixtureOrgRow{}
		if err := res.JSON(row); err != nil {
			return err
		}
		if row.ID == "" {
			return fmt.Errorf("POST /api/v1/organizations %s answered no org_tenant_id", slug)
		}
	}
	if row.State == "done" {
		return nil
	}
	_, err = poll(ctx, fixtureWaits.org, fixtureWaits.every, func() (bool, string, error) {
		var v fixtureOrgRow
		if _, err := cc.API.Get(ctx, "/api/v1/organizations/"+url.PathEscape(row.ID), &v); err != nil {
			return false, err.Error(), nil
		}
		if v.State == "failed" {
			return false, "", fmt.Errorf("the Organization %s is failed: %s", slug, v.LastError)
		}
		return v.State == "done", "state " + v.State, nil
	})
	if err != nil {
		return fmt.Errorf("the Organization %s is not done: %w", slug, err)
	}
	return nil
}

// InstallBlueprint installs the Application name of blueprint, with values
// (nil: the chart's defaults), in the first Environment of the Organization
// org, singleton in the first region, and waits until it is Ready.
func InstallBlueprint(ctx context.Context, cc *CheckContext, org, blueprint, name string, values map[string]any) error {
	if err := fixtureRefuse(cc, org); err != nil {
		return err
	}
	switch {
	case blueprint == "" || name == "":
		return errors.New("InstallBlueprint needs a Blueprint and an Application name")
	case cc.Deploy == nil || cc.Deploy.ID == "":
		return errors.New("the run names no deployment, so the Application cannot be read")
	case len(cc.Regions) == 0:
		return errors.New("the run names no region to place the Application in")
	}
	env := ""
	_, err := poll(ctx, fixtureWaits.org, fixtureWaits.every, func() (bool, string, error) {
		var v struct {
			Environments []struct {
				Name string `json:"name"`
			} `json:"environments"`
		}
		if _, err := cc.API.Get(ctx, "/api/v1/refs/organizations/"+url.PathEscape(org)+"/environments", &v); err != nil {
			return false, err.Error(), nil
		}
		if len(v.Environments) == 0 {
			return false, org + " has no Environment yet", nil
		}
		env = v.Environments[0].Name
		return true, "", nil
	})
	if err != nil {
		return fmt.Errorf("the Organization %s has no Environment: %w", org, err)
	}
	body := map[string]any{"blueprint": blueprint, "org": org, "environment": env, "name": name, "topology": "singleton",
		"placement": map[string]any{"regions": []string{cc.Regions[0].ID}}, "values": values}
	res, err := cc.API.Do(ctx, http.MethodPost, "/catalyst/v1/apps/instances", body)
	if err != nil {
		return err
	}
	if res.Status != http.StatusCreated {
		return fmt.Errorf("POST /catalyst/v1/apps/instances %s of %s in %s: HTTP %d (want 201): %s", name, blueprint, org, res.Status, trunc(string(res.Body), 300))
	}
	path := "/api/v1/sovereigns/" + url.PathEscape(cc.Deploy.ID) + "/applications/" + url.PathEscape(name) + "?namespace=" + url.QueryEscape(org)
	_, err = poll(ctx, fixtureWaits.app, fixtureWaits.every, func() (bool, string, error) {
		var a appView
		if _, err := cc.API.Get(ctx, path, &a); err != nil {
			return false, err.Error(), nil
		}
		st, why := a.ready()
		return st == "True", "Ready=" + st + " " + why, nil
	})
	if err != nil {
		return fmt.Errorf("the Application %s in %s is not Ready: %w", name, org, err)
	}
	return nil
}

// RemoveOrganization deletes the Organization slug, when it is listed, and
// waits until the API no longer has it and no Application is left in its
// namespace.
func RemoveOrganization(ctx context.Context, cc *CheckContext, slug string) error {
	if err := fixtureRefuse(cc, slug); err != nil {
		return err
	}
	if cc.Deploy == nil || cc.Deploy.ID == "" {
		return errors.New("the run names no deployment, so the Applications of the Organization cannot be read")
	}
	row, err := fixtureFind(ctx, cc, slug)
	if err != nil {
		return err
	}
	if row != nil {
		res, err := cc.API.Do(ctx, http.MethodDelete, "/api/v1/organizations/"+url.PathEscape(row.ID), nil)
		if err != nil {
			return err
		}
		if res.Status < 200 || res.Status > 299 {
			return fmt.Errorf("DELETE /api/v1/organizations/%s (%s): HTTP %d: %s", row.ID, slug, res.Status, trunc(string(res.Body), 300))
		}
	}
	_, err = poll(ctx, fixtureWaits.gone, fixtureWaits.every, func() (bool, string, error) {
		res, err := cc.API.Do(ctx, http.MethodGet, "/api/v1/organizations/"+url.PathEscape(slug), nil)
		if err != nil {
			return false, err.Error(), nil
		}
		var nf struct {
			Error string `json:"error"`
		}
		if res.Status != http.StatusNotFound || res.JSON(&nf) != nil || nf.Error != "org-tenant-not-found" {
			return false, fmt.Sprintf("GET /api/v1/organizations/%s: HTTP %d %s", slug, res.Status, trunc(string(res.Body), 120)), nil
		}
		var list struct {
			Items []struct{ Name, Namespace string } `json:"items"`
		}
		if _, err := cc.API.Get(ctx, "/api/v1/sovereigns/"+url.PathEscape(cc.Deploy.ID)+"/applications", &list); err != nil {
			return false, err.Error(), nil
		}
		var left []string
		for _, a := range list.Items {
			if a.Namespace == slug {
				left = append(left, a.Name)
			}
		}
		return len(left) == 0, "Applications left in " + slug + ": " + strings.Join(left, ", "), nil
	})
	if err != nil {
		return fmt.Errorf("the Organization %s is not gone: %w", slug, err)
	}
	return nil
}
