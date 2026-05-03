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
	Servers          []string `json:"servers"`
	LoadBalancers    []string `json:"load_balancers"`
	Networks         []string `json:"networks"`
	Firewalls        []string `json:"firewalls"`
	SSHKeys          []string `json:"ssh_keys"`
	S3Buckets        []string `json:"s3_buckets"`
	FirewallsRetried int      `json:"firewalls_retried"`
	Errors           []string `json:"errors"`
}

// Total returns Report's deleted-resource fields summed for the SSE log.
func (r PurgeReport) Total() int {
	return len(r.Servers) + len(r.LoadBalancers) + len(r.Networks) + len(r.Firewalls) + len(r.SSHKeys) + len(r.S3Buckets)
}

// firewallRetryAttempts caps the number of firewall-delete retries we run
// against Hetzner. The Cloud API returns 422 `resource_in_use` while a
// soft-deleted server is still detaching the firewall — server delete is
// async (returns 200 with action started) so the firewall stays attached
// for 5-30 seconds. 5 attempts at 6s/12s/24s/48s = ~90s of headroom which
// covers every observed detach in production. Issue #706.
const firewallRetryAttempts = 5

// firewallRetryInitialBackoff is the first sleep between firewall-delete
// retries. Exposed as a var so tests can shrink it without slowing CI.
var firewallRetryInitialBackoff = 6 * time.Second

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
		// Issue #706: server delete is async on Hetzner — the API returns
		// 200 "action started" but the firewall stays attached to the
		// soft-deleted server for several seconds, during which firewall
		// delete fails with 422 resource_in_use. Retry with exponential
		// backoff until 204 / 404 (already gone) or attempts exhausted.
		retried, err := deleteFirewallWithRetry(ctx, token, r.ID, progress, r.Name)
		if retried > 0 {
			report.FirewallsRetried += retried
		}
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete firewall %s (id=%d): %s", r.Name, r.ID, err.Error()))
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
	code, err := deleteResourceStatus(ctx, token, path)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("status %d", code)
	}
}

// deleteResourceStatus is the lower-level form of deleteResource that
// returns the raw HTTP status code so callers can distinguish retryable
// 422 (resource_in_use, used by Hetzner during async server detach) from
// terminal errors. Issue #706 — firewall delete needs this signal to
// drive an exponential-backoff retry while the server delete is still
// in flight.
func deleteResourceStatus(ctx context.Context, token, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		"https://api.hetzner.cloud"+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := purgeHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// deleteFirewallWithRetry deletes a firewall by id, retrying on 422
// resource_in_use with exponential backoff (6s, 12s, 24s, 48s — capped
// at firewallRetryAttempts). Returns the number of retries performed
// (i.e. attempts beyond the first) and a non-nil error only if every
// attempt returned 422 or a non-recoverable HTTP error surfaced.
//
// Hetzner's server delete is asynchronous: the API responds 200 "action
// started" while the soft-deleted server still references the firewall,
// causing firewall delete to 422 for the next 5-30 seconds. Without
// this retry the wipe handler reports "0 firewalls deleted" while the
// firewall remains live, costing money and leaking security boundary
// — verified on otech50 2026-05-03, issue #706.
func deleteFirewallWithRetry(ctx context.Context, token string, id int64, progress func(string), name string) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	path := "/v1/firewalls/" + strconv.FormatInt(id, 10)
	backoff := firewallRetryInitialBackoff
	retries := 0
	var lastCode int
	for attempt := 1; attempt <= firewallRetryAttempts; attempt++ {
		code, err := deleteResourceStatus(ctx, token, path)
		if err != nil {
			// Network error — surface immediately (no point retrying a
			// torn TCP connection in a tight loop; the upstream wipe
			// handler can rerun the whole purge).
			return retries, err
		}
		lastCode = code
		switch code {
		case http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound:
			return retries, nil
		case http.StatusUnprocessableEntity:
			// resource_in_use — the server delete is still in flight.
			// Sleep and retry with exponential backoff.
			if attempt == firewallRetryAttempts {
				return retries, fmt.Errorf("status 422 resource_in_use after %d attempts (server detach did not complete in ~%s)", firewallRetryAttempts, totalBackoffWindow())
			}
			progress(fmt.Sprintf("firewall %s still in use (attempt %d/%d) — backing off %s", name, attempt, firewallRetryAttempts, backoff))
			retries++
			select {
			case <-ctx.Done():
				return retries, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		default:
			return retries, fmt.Errorf("status %d", code)
		}
	}
	return retries, fmt.Errorf("status %d after %d attempts", lastCode, firewallRetryAttempts)
}

// totalBackoffWindow returns a human-readable summary of the maximum time
// the firewall retry loop sleeps. Used in error messages so operators
// can size patience accordingly without re-reading the code.
func totalBackoffWindow() time.Duration {
	total := time.Duration(0)
	b := firewallRetryInitialBackoff
	for i := 1; i < firewallRetryAttempts; i++ {
		total += b
		b *= 2
	}
	return total
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
