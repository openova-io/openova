package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CTL-G07, audit survives a restart. An audit event that the API lists
// before its pods are replaced must be listed after it, with the same
// identifier. The check:
//  1. makes one grant through the console API, for a subject that no user has
//     and an Application that does not exist, so that it gives nobody access;
//  2. reads the audit listing: it must hold the event of that grant with an
//     identifier, and name the JetStream stream as its transport;
//  3. deletes the pods of Deployment catalyst-system/catalyst-api in every
//     region that runs them and waits until the Deployment has replaced them;
//  4. reads the listing again: it must still hold the event;
//  5. removes the grant through the console API, whatever happened before.
//
// Step 3 changes no object of the Deployment itself: it deletes pods, as a
// node drain does, so the Deployment carries no restart annotation after it.
// After the restart the gateway needs a moment to learn the new pod: step 4
// asks at most ctlAuditAttempts times and waits between two attempts, and it
// asks again only when it got no answer or a 502, 503 or 504.
const (
	ctlAuditID         = "CTL-G07"
	ctlAuditNamespace  = "catalyst-system"
	ctlAuditDeployment = "catalyst-api"
	ctlAuditSelector   = "app.kubernetes.io/name=catalyst-api"
	ctlAuditTransport  = "jetstream:CATALYST_ORG" // audit.TransportJetStream of catalyst-api
	ctlAuditGrantType  = "rbac-grant-created"
	ctlAuditScopeKey   = "openova.io/application"
	ctlAuditAttempts   = 5
	ctlAuditPause      = 3 * time.Second
)

type ctlAuditCheck struct {
	kubectl kubectlFunc
	pause   time.Duration // between two attempts of step 4
}

func init() { RegisterCheck(ctlAuditCheck{kubectl: kube.Region.Run, pause: ctlAuditPause}) }

func (ctlAuditCheck) ID() string { return ctlAuditID }

// auditPage is what the check reads of GET /api/v1/sovereigns/{id}/audit/rbac.
type auditPage struct {
	Items []struct {
		ID            string `json:"id"`
		AuditType     string `json:"auditType"`
		UserAccessRef string `json:"userAccessRef"`
	} `json:"items"`
	Transport string `json:"transport"`
}

// grantEvent returns the identifier of the event of the grant, or "".
func (p auditPage) grantEvent(userAccess string) string {
	for _, it := range p.Items {
		if it.AuditType == ctlAuditGrantType && it.UserAccessRef == userAccess && it.ID != "" {
			return it.ID
		}
	}
	return ""
}

// has reports whether the page lists the event with this identifier.
func (p auditPage) has(id string) bool {
	for _, it := range p.Items {
		if it.ID == id {
			return true
		}
	}
	return false
}

// auditListing reads the newest 500 events of the listing. status is the HTTP
// status, or 0 when there was no answer.
func auditListing(ctx context.Context, c *api.Client, base string) (page auditPage, status int, err error) {
	res, err := c.Do(ctx, http.MethodGet, base+"/audit/rbac?limit=500", nil)
	if err != nil {
		return page, 0, err
	}
	if res.Status != http.StatusOK {
		return page, res.Status, fmt.Errorf("GET %s/audit/rbac: HTTP %d %s", base, res.Status, api.Snippet(res.Body))
	}
	return page, res.Status, res.JSON(&page)
}

// apiPods returns the pods of the API in one region: UID to name, and the
// names of the pods that are not Ready.
func (c ctlAuditCheck) apiPods(ctx context.Context, region kube.Region) (pods map[string]string, notReady []string, err error) {
	out, err := c.kubectl(region, ctx, nil, "get", "pods", "-n", ctlAuditNamespace, "-l", ctlAuditSelector, "-o", "json")
	if err != nil {
		return nil, nil, err
	}
	var list kube.List
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, nil, fmt.Errorf("[%s] decode the pods of the API: %w", region.ID, err)
	}
	pods = map[string]string{}
	for _, p := range list.Items {
		name := p.Str("metadata", "name")
		pods[p.Str("metadata", "uid")] = name
		ready := false
		conditions, _ := p.Get("status", "conditions").([]any)
		for _, c := range conditions {
			if m, isMap := c.(map[string]any); isMap && m["type"] == "Ready" && m["status"] == "True" {
				ready = true
			}
		}
		if !ready || p.Str("metadata", "deletionTimestamp") != "" {
			notReady = append(notReady, name)
		}
	}
	sort.Strings(notReady)
	return pods, notReady, nil
}

// podNames returns the names of a pod map, sorted.
func podNames(pods map[string]string) []string {
	out := []string{}
	for _, name := range pods {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// restart replaces the pods of the API in every region that runs them. It
// returns an error unless every such region ends with pods that are all new
// and all Ready.
func (c ctlAuditCheck) restart(ctx context.Context, regions []kube.Region, ev Evidence) error {
	replaced := []string{}
	for _, region := range regions {
		before, _, err := c.apiPods(ctx, region)
		if err != nil {
			return err
		}
		if len(before) == 0 {
			continue // this region does not run the API
		}
		if _, err := c.kubectl(region, ctx, nil, "delete", "pod", "-n", ctlAuditNamespace, "-l", ctlAuditSelector,
			"--wait=true", "--timeout=5m"); err != nil {
			return err
		}
		if _, err := c.kubectl(region, ctx, nil, "rollout", "status", "deployment/"+ctlAuditDeployment,
			"-n", ctlAuditNamespace, "--timeout=10m"); err != nil {
			return err
		}
		after, notReady, err := c.apiPods(ctx, region)
		if err != nil {
			return err
		}
		replaced = append(replaced, fmt.Sprintf("%s: %s -> %s", region.ID,
			strings.Join(podNames(before), ","), strings.Join(podNames(after), ",")))
		ev["pods"] = replaced
		for uid, name := range after {
			if _, old := before[uid]; old {
				return fmt.Errorf("region %s: pod %s was not replaced", region.ID, name)
			}
		}
		if len(after) == 0 || len(notReady) > 0 {
			return fmt.Errorf("region %s: after the rollout %d pods of the API exist and these are not Ready: %s",
				region.ID, len(after), strings.Join(notReady, ","))
		}
	}
	if len(replaced) == 0 {
		return fmt.Errorf("no region runs a pod of Deployment %s/%s", ctlAuditNamespace, ctlAuditDeployment)
	}
	return nil
}

func (c ctlAuditCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if cc.API == nil || cc.Deploy == nil || cc.Deploy.ID == "" || len(cc.Regions) == 0 {
		return errors.New("the check needs a console session, the deployment identifier and one region")
	}
	base := "/api/v1/sovereigns/" + url.PathEscape(cc.Deploy.ID)
	subject := "ctl-g07-" + strings.ToLower(NewRunID(time.Now()))
	var grant, eventID string

	ok := checkStep(rec, ctlAuditID, 1, "a grant is made (POST /api/v1/sovereigns/{id}/rbac/assign)", func(ev Evidence) error {
		ev["subject"] = subject
		res, err := cc.API.Do(ctx, http.MethodPost, base+"/rbac/assign", map[string]any{
			"user":  map[string]string{"email": subject + "@gate.invalid", "keycloakSubject": subject},
			"tier":  "viewer",
			"scope": []map[string]string{{"key": ctlAuditScopeKey, "value": subject}},
		})
		if err != nil {
			return err
		}
		var out struct {
			Applied    string `json:"applied"`
			UserAccess struct {
				Name string `json:"name"`
			} `json:"userAccess"`
		}
		if res.Status == http.StatusCreated || res.Status == http.StatusOK {
			if err := res.JSON(&out); err != nil {
				return err
			}
		}
		ev["user_access"], ev["applied"] = out.UserAccess.Name, out.Applied
		if res.Status != http.StatusCreated || out.Applied != "created" || out.UserAccess.Name == "" {
			return fmt.Errorf("POST %s/rbac/assign: HTTP %d, applied %q, UserAccess %q; want 201, created and a name: %s",
				base, res.Status, out.Applied, out.UserAccess.Name, api.Snippet(res.Body))
		}
		grant = out.UserAccess.Name // only a grant this run created is removed in step 5
		return nil
	})
	ok = ok && checkStep(rec, ctlAuditID, 2, "the listing holds the event of the grant and names the stream as its transport (GET /api/v1/sovereigns/{id}/audit/rbac)", func(ev Evidence) error {
		page, _, err := auditListing(ctx, cc.API, base)
		if err != nil {
			return err
		}
		eventID = page.grantEvent(grant)
		ev["transport"], ev["event_id_before"] = page.Transport, eventID
		if eventID == "" {
			return fmt.Errorf("the listing holds no %s event of UserAccess %s with an id (%d items)", ctlAuditGrantType, grant, len(page.Items))
		}
		if page.Transport != ctlAuditTransport {
			return fmt.Errorf("the transport of the audit is %q, want %q: the events are not recorded on the stream", page.Transport, ctlAuditTransport)
		}
		return nil
	})
	ok = ok && checkStep(rec, ctlAuditID, 3, "the pods of Deployment "+ctlAuditNamespace+"/"+ctlAuditDeployment+" are deleted and replaced", func(ev Evidence) error {
		return c.restart(ctx, cc.Regions, ev)
	})
	if ok {
		checkStep(rec, ctlAuditID, 4, "the listing still holds the event after the restart", func(ev Evidence) error {
			var page auditPage
			for attempt := 1; ; attempt++ {
				var status int
				var err error
				page, status, err = auditListing(ctx, cc.API, base)
				ev["attempts"] = attempt
				if err == nil {
					break
				}
				again := status == 0 || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
				if !again || attempt == ctlAuditAttempts {
					return err
				}
				timer := time.NewTimer(c.pause)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				}
			}
			ev["transport"], ev["event_id_after"] = page.Transport, ""
			if page.has(eventID) {
				ev["event_id_after"] = eventID
			} else {
				return fmt.Errorf("event %s was listed before the restart and is not listed after it (%d items, transport %q)",
					eventID, len(page.Items), page.Transport)
			}
			return nil
		})
	}
	if grant != "" {
		checkStep(rec, ctlAuditID, 5, "the grant is removed (DELETE /api/v1/deployments/{id}/admin/user-access/{name})", func(ev Evidence) error {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			path := "/api/v1/deployments/" + url.PathEscape(cc.Deploy.ID) + "/admin/user-access/" + url.PathEscape(grant)
			res, err := cc.API.Do(cleanup, http.MethodDelete, path, nil)
			if err != nil {
				return err
			}
			ev["user_access"] = grant
			if res.Status != http.StatusNoContent {
				return fmt.Errorf("DELETE %s: HTTP %d %s; the UserAccess %s may be left behind", path, res.Status, api.Snippet(res.Body), grant)
			}
			return nil
		})
	}
	return nil
}
