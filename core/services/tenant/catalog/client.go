// Package catalog is a minimal read-only HTTP client the tenant service uses
// to look up apps and plans when validating day-2 installs. The catalog
// service is the authoritative source for resource footprints (RAM/CPU/disk
// per app) and plan capacity limits.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// App mirrors the subset of the catalog App document this service cares about.
type App struct {
	ID           string   `json:"id"`
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Dependencies []string `json:"dependencies"`
	Shareable    bool     `json:"shareable"`
	Kind         string   `json:"kind"`
	System       bool     `json:"system"`
	// Deployable=false means the catalog lists the app but the provisioning
	// template isn't wired yet. InstallApp rejects with 400. See issue #102.
	Deployable   bool     `json:"deployable"`
	RamMB        int      `json:"ram_mb"`
	CpuMilli     int      `json:"cpu_milli"`
	DiskGB       int      `json:"disk_gb"`
}

// Plan mirrors the subset of the catalog Plan document this service cares
// about. CPU/Memory/Storage are human-formatted strings ("4 vCPU", "8 GB",
// "50 GB") — use ParsedLimits() to get integer caps for capacity math.
type Plan struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	CPU      string `json:"cpu"`
	Memory   string `json:"memory"`
	Storage  string `json:"storage"`
	PriceOMR int    `json:"price_omr"`
}

// Limits is the numeric capacity of a plan. 0 in any field means "unmetered"
// (e.g. the flexi plan that advertises "On demand").
type Limits struct {
	CpuMilli int
	RamMB    int
	DiskGB   int
}

// ParsedLimits extracts the numeric capacity from the CPU/Memory/Storage
// strings. It is deliberately lenient — unknown formats return 0 for that
// axis, which the caller treats as unmetered.
func (p Plan) ParsedLimits() Limits {
	return Limits{
		CpuMilli: parseVCPUToMilli(p.CPU),
		RamMB:    parseGBToMB(p.Memory),
		DiskGB:   parseStorageToGB(p.Storage),
	}
}

func parseVCPUToMilli(s string) int {
	// Accepts "2 vCPU", "4vcpu", "On demand"...
	n := extractLeadingInt(s)
	if n == 0 {
		return 0
	}
	return n * 1000
}

func parseGBToMB(s string) int {
	n := extractLeadingInt(s)
	if n == 0 {
		return 0
	}
	return n * 1024
}

func parseStorageToGB(s string) int { return extractLeadingInt(s) }

func extractLeadingInt(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// Client talks to the catalog service over HTTP. BaseURL should point at the
// catalog service root, e.g. "http://catalog.sme.svc.cluster.local:8082".
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New constructs a catalog client with a sensible default timeout.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 5 * time.Second},
	}
}

// ListApps returns the full app catalog. Cheap enough to call per-install
// because the catalog is small (~30 docs) and the hot path already waits on
// several other network calls.
func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/catalog/apps", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog: list apps: status %d", res.StatusCode)
	}
	var out []App
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetPlan fetches a plan by id. Returns nil if not found. The catalog service
// doesn't yet expose GET /catalog/plans/{id}, so this walks ListPlans — the
// plan set is ~5 documents, so this is fine.
func (c *Client) GetPlan(ctx context.Context, id string) (*Plan, error) {
	plans, err := c.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		if plans[i].ID == id || plans[i].Slug == id {
			return &plans[i], nil
		}
	}
	return nil, nil
}

// ListPlans returns every plan.
func (c *Client) ListPlans(ctx context.Context) ([]Plan, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/catalog/plans", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog: list plans: status %d", res.StatusCode)
	}
	var out []Plan
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
