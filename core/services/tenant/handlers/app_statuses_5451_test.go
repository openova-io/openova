package handlers

import (
	"net/http"
	"testing"

	"github.com/openova-io/openova/core/services/tenant/catalog"
)

// #5451 — the per-Org console badged four dead applications INSTALLED with live
// Open buttons because every signal it consulted (the Org's Apps list, the
// deploy step's status) reports what was *dispatched*, not what serves. These
// guards cover the join that supplies the missing half.

func TestSelectOpenableApps_ExcludesBackingServices(t *testing.T) {
	byID := map[string]*catalog.App{
		"a-listmonk": {ID: "a-listmonk", Slug: "listmonk", Kind: "app"},
		"a-postgres": {ID: "a-postgres", Slug: "postgres", Kind: "service"},
		"a-system":   {ID: "a-system", Slug: "internal", System: true},
		"a-umami":    {ID: "a-umami", Slug: "umami", Kind: "app"},
	}
	// Includes an unknown id and a duplicate — both must be dropped without
	// disturbing the rest.
	got := selectOpenableApps(
		[]string{"a-listmonk", "a-postgres", "a-system", "a-umami", "a-ghost", "a-listmonk"},
		byID,
	)

	var slugs []string
	for _, a := range got {
		slugs = append(slugs, a.Slug)
	}
	if len(slugs) != 2 || slugs[0] != "listmonk" || slugs[1] != "umami" {
		t.Fatalf("want exactly [listmonk umami], got %v", slugs)
	}
}

// The load-bearing one. An application the runtime could not be reached for
// must NOT come back looking healthy — resolving missing information to green
// is the precise defect this endpoint exists to end.
func TestBuildAppStatusRows_MissingStatusIsUnknownNotHealthy(t *testing.T) {
	installed := []*catalog.App{
		{ID: "a-listmonk", Slug: "listmonk"},
		{ID: "a-umami", Slug: "umami"},
	}
	// Only umami has live state; listmonk is absent from the map.
	statuses := map[string]podStatus{
		"umami": {Slug: "umami", PodStatus: "Running", ReadyReplicas: 1, TotalReplicas: 1},
	}

	rows := buildAppStatusRows(installed, statuses)
	if len(rows) != 2 {
		t.Fatalf("want a row per installed app, got %d", len(rows))
	}

	byID := map[string]AppStatus{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	if got := byID["a-listmonk"].PodStatus; got != "unknown" {
		t.Errorf("absent runtime state must report \"unknown\", got %q", got)
	}
	// Guard the actual hazard rather than just the string: whatever value an
	// unreachable runtime yields, it must never be one the console reads as
	// serving. If someone later "helpfully" defaults this to Running, this
	// fails.
	if got := byID["a-listmonk"].PodStatus; got == "Running" {
		t.Errorf("absent runtime state must never report as Running")
	}
	if byID["a-listmonk"].ReadyReplicas != 0 {
		t.Errorf("absent runtime state must not claim ready replicas")
	}

	u := byID["a-umami"]
	if u.PodStatus != "Running" || u.ReadyReplicas != 1 || u.TotalReplicas != 1 {
		t.Errorf("live state must pass through verbatim, got %+v", u)
	}
}

// The hw290 shape exactly: pods exist and are Running, but none passed the
// Ready gate — which is what produces zero Service endpoints and the 503
// "no healthy upstream" behind the Open button. The row must carry the zero
// through so the console can act on it.
func TestBuildAppStatusRows_RunningButZeroReadyIsPreserved(t *testing.T) {
	rows := buildAppStatusRows(
		[]*catalog.App{{ID: "a-kuma", Slug: "uptime-kuma"}},
		map[string]podStatus{
			"uptime-kuma": {Slug: "uptime-kuma", PodStatus: "Running", ReadyReplicas: 0, TotalReplicas: 1},
		},
	)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].ReadyReplicas != 0 || rows[0].TotalReplicas != 1 {
		t.Fatalf("ready/total must survive the join, got %d/%d", rows[0].ReadyReplicas, rows[0].TotalReplicas)
	}
}

// The route has to exist for any of the above to reach a caller.
func TestAppStatusesRouteIsRegistered(t *testing.T) {
	mux, ok := (&Handler{}).Routes().(*http.ServeMux)
	if !ok {
		t.Fatalf("Routes() concrete type is not *http.ServeMux")
	}

	req, err := http.NewRequest("GET", "/organizations/o1/app-statuses", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, pattern := mux.Handler(req)
	if pattern != "GET /organizations/{id}/app-statuses" {
		t.Fatalf("app-statuses route not registered; matched %q", pattern)
	}
}
