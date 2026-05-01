// Package hetzner — orphan purge surface used by the wizard's Cancel & Wipe
// path (issue #318). When `tofu destroy` either fails partway or has no
// state to act against, the operator still needs a clean cloud account.
// This file enumerates and force-deletes every Hetzner resource tagged
// with the per-Sovereign label `catalyst.openova.io/sovereign=<fqdn>` so
// the next provisioning round starts from zero. The label key is shared
// with — and pinned by — the OpenTofu module at infra/hetzner/main.tf.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (OpenTofu owns Phase 0): under
// normal operation `tofu destroy` is the canonical purge path. This file
// is the recovery fallback. It is therefore allowed to call Hetzner API
// directly — but only for orphan cleanup, never for new resource
// creation. Per the same principle, all NEW resource creation flows
// through OpenTofu.

package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PurgeReport summarises what the purge actually deleted. Returned to the
// wizard so the SSE log shows the operator a concrete tally of what was
// removed (or what was already gone).
type PurgeReport struct {
	Servers       []string `json:"servers"`
	LoadBalancers []string `json:"load_balancers"`
	Networks      []string `json:"networks"`
	Firewalls     []string `json:"firewalls"`
	SSHKeys       []string `json:"ssh_keys"`
	Errors        []string `json:"errors"`
}

// Add returns Report's fields summed for the SSE log.
func (r PurgeReport) Total() int {
	return len(r.Servers) + len(r.LoadBalancers) + len(r.Networks) + len(r.Firewalls) + len(r.SSHKeys)
}

// PurgeLabelKey is the canonical Hetzner-resource label that the OpenTofu
// module at infra/hetzner/main.tf stamps on every resource it creates
// (network, firewall, ssh-key, server, load-balancer). The value is the
// Sovereign's fully-qualified domain (e.g. "omantel.omani.works"). This
// constant exists so the filter is exercised by a regression test that
// pins both sides of the contract — if either Tofu's emission OR purge.go
// drifts, the test fails.
const PurgeLabelKey = "catalyst.openova.io/sovereign"

// FilterByLabel returns the Hetzner API `label_selector` query value for
// the given key/value pair. Exposed so the regression test can pin the
// exact wire format used against the Hetzner Cloud API.
func FilterByLabel(key, value string) string {
	return key + "=" + value
}

// Purge enumerates and deletes every Hetzner resource tagged with the
// canonical label `catalyst.openova.io/sovereign=<sovereignFQDN>`. The
// OpenTofu module at infra/hetzner/main.tf is responsible for setting
// this label on every resource it creates; we filter on it here.
//
// progress is called for each successful delete with a human-readable
// message ("deleted server otech-cp-1", "deleted lb otech-lb", …) so the
// wizard can stream the cleanup live. Pass nil to silence.
//
// The purge is best-effort. A failure to delete one resource does not
// abort the others; failures land in PurgeReport.Errors. The caller
// decides whether non-zero errors are fatal.
func Purge(ctx context.Context, token, sovereignFQDN string, progress func(msg string)) (PurgeReport, error) {
	report := PurgeReport{}
	if progress == nil {
		progress = func(string) {}
	}
	if strings.TrimSpace(token) == "" {
		return report, fmt.Errorf("hetzner token is empty")
	}
	if strings.TrimSpace(sovereignFQDN) == "" {
		return report, fmt.Errorf("sovereign fqdn is empty")
	}

	labelSelector := FilterByLabel(PurgeLabelKey, sovereignFQDN)

	// Order matters: dependents first, then independents, so deletes succeed.
	//   1. Servers reference networks + firewalls + ssh-keys → delete first.
	//   2. Load balancers reference networks → delete second.
	//   3. Firewalls + networks + ssh-keys are independent → any order.
	servers, err := listResources(ctx, token, "/v1/servers", labelSelector, "servers")
	if err != nil {
		report.Errors = append(report.Errors, "list servers: "+err.Error())
	}
	for _, r := range servers {
		if err := deleteResource(ctx, token, "/v1/servers/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete server %s: %s", r.Name, err.Error()))
			continue
		}
		report.Servers = append(report.Servers, r.Name)
		progress(fmt.Sprintf("deleted server %s", r.Name))
	}

	lbs, err := listResources(ctx, token, "/v1/load_balancers", labelSelector, "load_balancers")
	if err != nil {
		report.Errors = append(report.Errors, "list load_balancers: "+err.Error())
	}
	for _, r := range lbs {
		if err := deleteResource(ctx, token, "/v1/load_balancers/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete lb %s: %s", r.Name, err.Error()))
			continue
		}
		report.LoadBalancers = append(report.LoadBalancers, r.Name)
		progress(fmt.Sprintf("deleted load balancer %s", r.Name))
	}

	firewalls, err := listResources(ctx, token, "/v1/firewalls", labelSelector, "firewalls")
	if err != nil {
		report.Errors = append(report.Errors, "list firewalls: "+err.Error())
	}
	for _, r := range firewalls {
		if err := deleteResource(ctx, token, "/v1/firewalls/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete firewall %s: %s", r.Name, err.Error()))
			continue
		}
		report.Firewalls = append(report.Firewalls, r.Name)
		progress(fmt.Sprintf("deleted firewall %s", r.Name))
	}

	networks, err := listResources(ctx, token, "/v1/networks", labelSelector, "networks")
	if err != nil {
		report.Errors = append(report.Errors, "list networks: "+err.Error())
	}
	for _, r := range networks {
		if err := deleteResource(ctx, token, "/v1/networks/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete network %s: %s", r.Name, err.Error()))
			continue
		}
		report.Networks = append(report.Networks, r.Name)
		progress(fmt.Sprintf("deleted network %s", r.Name))
	}

	sshkeys, err := listResources(ctx, token, "/v1/ssh_keys", labelSelector, "ssh_keys")
	if err != nil {
		report.Errors = append(report.Errors, "list ssh_keys: "+err.Error())
	}
	for _, r := range sshkeys {
		if err := deleteResource(ctx, token, "/v1/ssh_keys/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete ssh_key %s: %s", r.Name, err.Error()))
			continue
		}
		report.SSHKeys = append(report.SSHKeys, r.Name)
		progress(fmt.Sprintf("deleted ssh-key %s", r.Name))
	}

	return report, nil
}

// hetznerResource is the minimum shape we need from each Hetzner list
// response to drive the delete loop.
type hetznerResource struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// listResources GETs /v1/<resource> with the label selector and returns
// every entry. Hetzner pages at 25 per page by default; we follow the
// `next` link until exhausted.
func listResources(ctx context.Context, token, path, labelSelector, listKey string) ([]hetznerResource, error) {
	var out []hetznerResource
	page := 1
	for {
		q := url.Values{}
		q.Set("label_selector", labelSelector)
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "50")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"https://api.hetzner.cloud"+path+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := purgeHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		body := decodeBody(resp)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("hetzner auth failed (status %d) — token may have been rotated or scope revoked", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("hetzner list %s: unexpected status %d: %s", path, resp.StatusCode, body.errMsg())
		}

		entries, _ := body.list(listKey)
		out = append(out, entries...)
		if !body.hasNextPage() {
			break
		}
		page++
		if page > 50 {
			break // sanity bound
		}
	}
	return out, nil
}

// deleteResource issues DELETE on the given path. Hetzner returns 200/204
// on success, 404 when already gone (treated as success), 423/429 when
// retryable. We treat 404 as success and surface every other non-2xx as
// an error the caller appends to PurgeReport.Errors.
func deleteResource(ctx context.Context, token, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		"https://api.hetzner.cloud"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := purgeHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("status %d", resp.StatusCode)
	}
}

// purgeHTTPClient is separate from the package-level httpClient in
// client.go because purge operations may legitimately take longer than
// the 10s ValidateToken bound (Hetzner async server-delete jobs can
// queue under load).
var purgeHTTPClient = &http.Client{Timeout: 60 * time.Second}

// hetznerListBody is a thin facade over the JSON body so the list+next
// logic stays readable in one place.
type hetznerListBody struct {
	raw map[string]json.RawMessage
}

func decodeBody(resp *http.Response) hetznerListBody {
	body := hetznerListBody{raw: map[string]json.RawMessage{}}
	_ = json.NewDecoder(resp.Body).Decode(&body.raw)
	return body
}

func (b hetznerListBody) list(key string) ([]hetznerResource, error) {
	raw, ok := b.raw[key]
	if !ok {
		return nil, nil
	}
	var entries []hetznerResource
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (b hetznerListBody) hasNextPage() bool {
	raw, ok := b.raw["meta"]
	if !ok {
		return false
	}
	var meta struct {
		Pagination struct {
			NextPage *int `json:"next_page"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return false
	}
	return meta.Pagination.NextPage != nil
}

func (b hetznerListBody) errMsg() string {
	raw, ok := b.raw["error"]
	if !ok {
		return ""
	}
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return ""
	}
	return e.Code + ": " + e.Message
}
