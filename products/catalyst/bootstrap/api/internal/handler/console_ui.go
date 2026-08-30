// console_ui.go — the Sovereign-console sidebar as a per-Sovereign MAPPING
// surface (EPIC #6723 lane C; founder 2026-08-31: "OpenOva is composed of
// applications; the left menu or sub-menus of the sovereign console can be
// connected to the respective applications, like Agenity; OpenOva should
// provide that flexibility in its admin settings to map").
//
// Three layers compose the menu the console renders:
//
//  1. Blueprint DEFAULTS — Wave 5.69 (#2370 → PR #2374) shipped the
//     Blueprint CRD field spec.consoleUI.{sidebarEntry,sidebarLabel,
//     sidebarRoute,sidebarOrder,sidebarIcon}. Every Blueprint CR that carries
//     a consoleUI block projects one entry (source=blueprint), enabled when
//     sidebarEntry=true. bp-agenity is the first consumer.
//  2. Application CANDIDATES — every installed Application CR whose Blueprint
//     exposes a user-UI endpoint (ssoEnabled || launchDefault || name=="ui",
//     the same predicate blueprintHasUserUIEndpoint uses for the Open button)
//     projects one entry (source=application) that defaults to DISABLED, so
//     the sovereign-admin can hang any running app — Grafana, Harbor, an
//     Org's Agenity — off the left rail without a Blueprint author's help.
//  3. Per-Sovereign OVERRIDES — the sovereign-admin's mapping, persisted as
//     ConfigMap catalyst-system/console-ui-sidebar (key overrides.json) on the
//     Sovereign's own cluster. An override can enable/disable an entry,
//     rename it, re-route it, re-order it, or nest it under one of the
//     hardcoded FLAT_NAV items as a sub-menu.
//
// Wire surface (all inside the RequireSession chi.Group in cmd/api/main.go):
//
//	GET /api/v1/sovereigns/{id}/console-ui/sidebar-entries
//	    → { entries: [MergedEntry…], parents: [flat-nav-id…] }
//	      The MERGED view (defaults + candidates ⊕ overrides). Every entry
//	      carries enabled/source/parent; the console hides enabled=false.
//	GET /api/v1/sovereigns/{id}/console-ui/sidebar-overrides
//	    → { entries: [Override…], parents, allowedHosts, namespace, name }
//	      The RAW stored overrides (empty list when none). sovereign-admin.
//	PUT /api/v1/sovereigns/{id}/console-ui/sidebar-overrides
//	    ← { entries: [Override…] }   validated, then written to the ConfigMap.
//	    → { entries, appliedAt, namespace, name }                sovereign-admin.
//
// Override validation (validateSidebarOverrides — table-tested, no cluster):
// id = Blueprint name or app:<application-name>; label ≤ 40 characters;
// route starts with "/" or is https:// on one of the Sovereign's own parent
// domains; order 0..100; parent ∈ sidebarParentIDs (Settings and the
// Sovereignty anchor are NOT parents — two founder rulings keep Settings a
// flat link, see SovereignSidebar.tsx).
//
// Every read of the k8s state goes through the k8scache Indexer (ADR-0001
// §5 — catalyst-api consolidates, it never fans reads out to the apiserver);
// the ConfigMap is the one exception and is read/written through the
// per-cluster dynamic client the cache already holds, because the cache
// marks ConfigMaps Sensitive and strips their data on the way out.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// SidebarEntry is the wire shape of one MERGED sidebar entry.
//
// The first five fields are the Wave 5.69b contract the console already
// consumes ({id,label,route,order,icon}); the rest are the #6723 mapping
// layer. Default* echo the un-overridden values so the Settings → Menu table
// can show provenance and offer a per-row reset without a second request.
type SidebarEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Route string `json:"route"`
	Order int    `json:"order"`
	Icon  string `json:"icon,omitempty"`

	// Source — "blueprint" (spec.consoleUI on a Blueprint CR) or
	// "application" (an installed Application CR with a user-UI endpoint).
	Source string `json:"source"`
	// Enabled — false entries are candidates the console does not render.
	Enabled bool `json:"enabled"`
	// Parent — a FLAT_NAV id (see sidebarParentIDs) when the entry renders as
	// a sub-item under that hardcoded nav item; empty = top-level.
	Parent string `json:"parent,omitempty"`
	// Overridden — true when a stored override touched this entry.
	Overridden bool `json:"overridden"`

	DefaultLabel   string `json:"defaultLabel"`
	DefaultRoute   string `json:"defaultRoute"`
	DefaultOrder   int    `json:"defaultOrder"`
	DefaultEnabled bool   `json:"defaultEnabled"`
}

// SidebarSource values.
const (
	sidebarSourceBlueprint   = "blueprint"
	sidebarSourceApplication = "application"
)

// SidebarOverride is one stored mapping decision. Empty Label/Route and a
// nil Order mean "inherit the default"; Parent empty means top-level.
type SidebarOverride struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Label   string `json:"label,omitempty"`
	Route   string `json:"route,omitempty"`
	Order   *int   `json:"order,omitempty"`
	Parent  string `json:"parent,omitempty"`
}

// SidebarOverrides is the PUT body and the ConfigMap payload.
type SidebarOverrides struct {
	Entries []SidebarOverride `json:"entries"`
}

// sidebarParentIDs — the hardcoded FLAT_NAV ids an override may nest under.
// Mirrors SovereignSidebar.tsx FLAT_NAV minus `sovereignty` (an anchor entry
// into /settings, not a page) and minus Settings (founder rulings Wave 5 +
// #4089: Settings has NO sub-nav children). The UI intersects its own
// FLAT_NAV list with what this endpoint returns, so a drift between the two
// narrows the dropdown rather than producing an unmappable parent.
var sidebarParentIDs = []string{
	"dashboard",
	"cloud",
	"apps",
	"catalog",
	"jobs",
	"compliance",
	"users",
	"organizations",
	"billing",
}

func isSidebarParentID(id string) bool {
	for _, p := range sidebarParentIDs {
		if p == id {
			return true
		}
	}
	return false
}

// Persistence coordinates. The ConfigMap lives next to the console it
// configures; catalyst-api on a Sovereign already holds configmaps RBAC in
// this namespace (sovereign_smtp_seed.go, org_console_tls.go).
const (
	sidebarOverridesNamespace = "catalyst-system"
	sidebarOverridesConfigMap = "console-ui-sidebar"
	sidebarOverridesDataKey   = "overrides.json"

	sidebarLabelMaxRunes = 40
	sidebarRouteMaxLen   = 512
	sidebarOrderMin      = 0
	sidebarOrderMax      = 100
	sidebarMaxEntries    = 200
	sidebarIDPrefixApp   = "app:"
)

// sidebarIDRe — Blueprint CR names and Application CR names are both
// RFC-1123 subdomains; the app: prefix marks the application form.
var sidebarIDRe = regexp.MustCompile(`^(app:)?[a-z0-9]([a-z0-9.-]{0,126}[a-z0-9])?$`)

// ── GET sidebar-entries (merged view) ────────────────────────────────────

// HandleConsoleUISidebarEntries — GET .../console-ui/sidebar-entries.
//
// Returns the merged view. Graceful on the mapping layer: when the
// overrides ConfigMap cannot be read (client build failure, RBAC) the
// defaults still render — the sidebar must never go blank because a
// mapping could not be loaded. The Blueprint list itself stays a hard
// dependency (502), exactly as Wave 5.69b shipped it.
func (h *Handler) HandleConsoleUISidebarEntries(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	blueprints, _, err := h.k8sCache.List(clusterID, "blueprint", labels.Everything())
	if err != nil {
		http.Error(w, fmt.Sprintf("list blueprints: %v", err), http.StatusBadGateway)
		return
	}
	// Applications are optional input: an older registry without the kind,
	// or a cluster with none installed, simply yields no candidates.
	applications, _, appErr := h.k8sCache.List(clusterID, "application", labels.Everything())
	if appErr != nil {
		h.log.Debug("console-ui: application candidates unavailable", "cluster", clusterID, "err", appErr)
		applications = nil
	}

	defaults := collectSidebarDefaults(blueprints, applications)

	overrides := SidebarOverrides{}
	if dyn, dErr := h.k8sCache.DynamicClientFor(clusterID); dErr != nil {
		h.log.Warn("console-ui: overrides client unavailable, rendering defaults", "cluster", clusterID, "err", dErr)
	} else if ov, _, rErr := readSidebarOverrides(r.Context(), dyn); rErr != nil {
		h.log.Warn("console-ui: overrides read failed, rendering defaults", "cluster", clusterID, "err", rErr)
	} else {
		overrides = ov
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": mergeSidebarEntries(defaults, overrides),
		"parents": sidebarParentIDs,
	})
}

// collectSidebarDefaults projects the un-overridden entry set: one per
// Blueprint CR carrying spec.consoleUI, one per installed Application whose
// Blueprint exposes a user UI. Deterministic order (Blueprints then
// Applications, each by name) so a stable id set feeds the merge.
func collectSidebarDefaults(blueprints, applications []*unstructured.Unstructured) []SidebarEntry {
	out := make([]SidebarEntry, 0, len(blueprints)+len(applications))
	bpByName := make(map[string]*unstructured.Unstructured, len(blueprints))

	sortedBPs := append([]*unstructured.Unstructured(nil), blueprints...)
	sort.SliceStable(sortedBPs, func(i, j int) bool {
		return nameOf(sortedBPs[i]) < nameOf(sortedBPs[j])
	})
	for _, bp := range sortedBPs {
		if bp == nil {
			continue
		}
		bpByName[bp.GetName()] = bp
		if entry, ok := projectSidebarEntry(bp); ok {
			out = append(out, entry)
		}
	}

	sortedApps := append([]*unstructured.Unstructured(nil), applications...)
	sort.SliceStable(sortedApps, func(i, j int) bool {
		a, b := sortedApps[i], sortedApps[j]
		if nameOf(a) != nameOf(b) {
			return nameOf(a) < nameOf(b)
		}
		return namespaceOf(a) < namespaceOf(b)
	})
	seen := make(map[string]struct{}, len(sortedApps))
	for _, app := range sortedApps {
		entry, ok := projectApplicationCandidate(app, bpByName)
		if !ok {
			continue
		}
		// Application names are per-Org; two Orgs installing the same app
		// name collapse onto one candidate id (first by namespace wins) so
		// the id space stays app:<name> as the mapping contract states.
		if _, dup := seen[entry.ID]; dup {
			continue
		}
		seen[entry.ID] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func nameOf(u *unstructured.Unstructured) string {
	if u == nil {
		return ""
	}
	return u.GetName()
}

func namespaceOf(u *unstructured.Unstructured) string {
	if u == nil {
		return ""
	}
	return u.GetNamespace()
}

// projectSidebarEntry extracts the SidebarEntry projection from a Blueprint
// unstructured. Returns (entry, true) when the Blueprint carries a
// spec.consoleUI block at all; Enabled mirrors sidebarEntry so an author's
// opt-out still surfaces as a DISABLED candidate the sovereign-admin may
// flip on (the #6723 mapping contract). A Blueprint without consoleUI is
// not an entry — its installed instances surface via
// projectApplicationCandidate instead.
func projectSidebarEntry(bp *unstructured.Unstructured) (SidebarEntry, bool) {
	if bp == nil {
		return SidebarEntry{}, false
	}
	consoleUI, found, err := unstructured.NestedMap(bp.Object, "spec", "consoleUI")
	if err != nil || !found || consoleUI == nil {
		return SidebarEntry{}, false
	}
	optIn, _, _ := unstructured.NestedBool(consoleUI, "sidebarEntry")
	label, _, _ := unstructured.NestedString(consoleUI, "sidebarLabel")
	route, _, _ := unstructured.NestedString(consoleUI, "sidebarRoute")
	icon, _, _ := unstructured.NestedString(consoleUI, "sidebarIcon")
	// Wave 5.69d (#2396 reviewer fix): honor CRD-applied default
	// instead of post-projection coalesce. The CRD declares
	// `sidebarOrder: { default: 50, minimum: 0 }` so the apiserver
	// populates 50 on missing fields. An explicit `0` (used to pin
	// above all hardcoded entries) was being silently rewritten to
	// 50 here — a real bug for authors who want top-pin behaviour.
	orderInt64, _, _ := unstructured.NestedInt64(consoleUI, "sidebarOrder")
	order := int(orderInt64)
	if label == "" {
		label = bp.GetName()
	}
	if route == "" {
		route = fmt.Sprintf("/apps/%s", bp.GetName())
	}
	return SidebarEntry{
		ID:             bp.GetName(),
		Label:          label,
		Route:          route,
		Order:          order,
		Icon:           icon,
		Source:         sidebarSourceBlueprint,
		Enabled:        optIn,
		DefaultLabel:   label,
		DefaultRoute:   route,
		DefaultOrder:   order,
		DefaultEnabled: optIn,
	}, true
}

// projectApplicationCandidate turns an installed Application CR into a
// DISABLED candidate entry when its Blueprint (looked up by
// spec.blueprintRef.name in the same cluster's Blueprint CRs) exposes a
// user-UI endpoint. Fail closed on every missing link — an app with no
// evidence of a UI is not a menu candidate, the same policy the Open
// button follows (userUIGatePasses, #3224).
//
// The default route is the console's own AppDetail page (/app/<name>) —
// always reachable, no hostname-template evaluation needed; the
// sovereign-admin re-routes to the app's public https:// front door from
// Settings → Menu when that is the intent.
func projectApplicationCandidate(app *unstructured.Unstructured, bpByName map[string]*unstructured.Unstructured) (SidebarEntry, bool) {
	if app == nil {
		return SidebarEntry{}, false
	}
	name := strings.TrimSpace(app.GetName())
	if name == "" {
		return SidebarEntry{}, false
	}
	bpName, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "name")
	bp := bpByName[strings.TrimSpace(bpName)]
	if bp == nil || !blueprintUnstructuredHasUserUIEndpoint(bp) {
		return SidebarEntry{}, false
	}
	icon, _, _ := unstructured.NestedString(bp.Object, "spec", "consoleUI", "sidebarIcon")
	label := name
	route := "/app/" + name
	const order = 50
	return SidebarEntry{
		ID:             sidebarIDPrefixApp + name,
		Label:          label,
		Route:          route,
		Order:          order,
		Icon:           icon,
		Source:         sidebarSourceApplication,
		Enabled:        false,
		DefaultLabel:   label,
		DefaultRoute:   route,
		DefaultOrder:   order,
		DefaultEnabled: false,
	}, true
}

// blueprintUnstructuredHasUserUIEndpoint — the unstructured twin of
// blueprintHasUserUIEndpoint (endpoint_handler.go): an endpoint is a user
// UI when ssoEnabled || launchDefault || name == "ui". Kept predicate-
// identical so the menu candidates and the Open button never disagree.
func blueprintUnstructuredHasUserUIEndpoint(bp *unstructured.Unstructured) bool {
	if bp == nil {
		return false
	}
	endpoints, found, err := unstructured.NestedSlice(bp.Object, "spec", "endpoints")
	if err != nil || !found {
		return false
	}
	for _, raw := range endpoints {
		ep, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		sso, _, _ := unstructured.NestedBool(ep, "ssoEnabled")
		launch, _, _ := unstructured.NestedBool(ep, "launchDefault")
		epName, _, _ := unstructured.NestedString(ep, "name")
		if sso || launch || strings.EqualFold(epName, "ui") {
			return true
		}
	}
	return false
}

// mergeSidebarEntries overlays the stored overrides on the default entry
// set and returns the merged, deterministically sorted list (order ASC,
// then label, then id). Overrides naming an id that no default carries —
// an uninstalled app, a Blueprint that dropped consoleUI — are ignored
// rather than rendered: the menu only ever points at something that
// exists.
func mergeSidebarEntries(defaults []SidebarEntry, ov SidebarOverrides) []SidebarEntry {
	byID := make(map[string]SidebarOverride, len(ov.Entries))
	for _, o := range ov.Entries {
		byID[strings.TrimSpace(o.ID)] = o
	}
	out := make([]SidebarEntry, 0, len(defaults))
	for _, d := range defaults {
		e := d
		e.Parent = ""
		if o, ok := byID[e.ID]; ok {
			e.Overridden = true
			e.Enabled = o.Enabled
			if l := strings.TrimSpace(o.Label); l != "" {
				e.Label = l
			}
			if rt := strings.TrimSpace(o.Route); rt != "" {
				e.Route = rt
			}
			if o.Order != nil {
				e.Order = *o.Order
			}
			if p := strings.TrimSpace(o.Parent); p != "" && isSidebarParentID(p) {
				e.Parent = p
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ── Validation ───────────────────────────────────────────────────────────

// validateSidebarOverrides returns every problem with the submitted
// mapping (empty slice = valid). allowedHosts is the Sovereign's own host
// set (its FQDN + parent-domain pool): an https:// route must land on one
// of them or a subdomain of one — the console never links a sovereign-
// admin's Users off to an arbitrary third-party host.
func validateSidebarOverrides(ov SidebarOverrides, allowedHosts []string) []string {
	var problems []string
	if len(ov.Entries) > sidebarMaxEntries {
		problems = append(problems, fmt.Sprintf("entries: at most %d overrides are accepted (got %d)", sidebarMaxEntries, len(ov.Entries)))
		return problems
	}
	seen := make(map[string]struct{}, len(ov.Entries))
	for i, o := range ov.Entries {
		prefix := fmt.Sprintf("entries[%d]", i)
		id := strings.TrimSpace(o.ID)
		switch {
		case id == "":
			problems = append(problems, prefix+".id: required")
		case !sidebarIDRe.MatchString(id):
			problems = append(problems, prefix+".id: must be a Blueprint name (e.g. bp-agenity) or app:<application-name>")
		default:
			if _, dup := seen[id]; dup {
				problems = append(problems, prefix+".id: duplicate id "+id)
			}
			seen[id] = struct{}{}
			prefix += "(" + id + ")"
		}

		if label := strings.TrimSpace(o.Label); label != "" {
			if n := utf8.RuneCountInString(label); n > sidebarLabelMaxRunes {
				problems = append(problems, fmt.Sprintf("%s.label: at most %d characters (got %d)", prefix, sidebarLabelMaxRunes, n))
			}
			if hasControlRune(label) {
				problems = append(problems, prefix+".label: control characters are not allowed")
			}
		}

		if route := strings.TrimSpace(o.Route); route != "" {
			if msg := validateSidebarRoute(route, allowedHosts); msg != "" {
				problems = append(problems, prefix+".route: "+msg)
			}
		}

		if o.Order != nil && (*o.Order < sidebarOrderMin || *o.Order > sidebarOrderMax) {
			problems = append(problems, fmt.Sprintf("%s.order: must be between %d and %d (got %d)", prefix, sidebarOrderMin, sidebarOrderMax, *o.Order))
		}

		if parent := strings.TrimSpace(o.Parent); parent != "" && !isSidebarParentID(parent) {
			problems = append(problems, fmt.Sprintf("%s.parent: %q is not a mappable menu item (allowed: %s)", prefix, parent, strings.Join(sidebarParentIDs, ", ")))
		}
	}
	return problems
}

// validateSidebarRoute — "" when valid, else the reason.
func validateSidebarRoute(route string, allowedHosts []string) string {
	if len(route) > sidebarRouteMaxLen {
		return fmt.Sprintf("at most %d characters", sidebarRouteMaxLen)
	}
	for _, r := range route {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "whitespace and control characters are not allowed"
		}
	}
	if strings.HasPrefix(route, "/") {
		if strings.HasPrefix(route, "//") {
			return "a console path must not start with //"
		}
		return ""
	}
	u, err := url.Parse(route)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return "must start with / (a console path) or be an https:// URL on one of this Sovereign's parent domains"
	}
	host := strings.ToLower(u.Hostname())
	if len(allowedHosts) == 0 {
		return "https:// routes need a configured Sovereign parent domain and none is known to this catalyst-api"
	}
	for _, allowed := range allowedHosts {
		a := strings.ToLower(strings.TrimSpace(allowed))
		if a == "" {
			continue
		}
		if host == a || strings.HasSuffix(host, "."+a) {
			return ""
		}
	}
	return fmt.Sprintf("host %q is not on one of this Sovereign's parent domains (%s)", host, strings.Join(allowedHosts, ", "))
}

func hasControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// sidebarAllowedHosts — the host set an https:// route may target: the
// Sovereign's own FQDN (env / endpoint deps / adopted deployment) plus every
// parent domain in the Org pool. Lower-cased, deduplicated, sorted.
func (h *Handler) sidebarAllowedHosts() []string {
	seen := map[string]struct{}{}
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			return
		}
		seen[v] = struct{}{}
	}
	add(h.endpointSovereignFQDN())
	add(h.lookupPrimaryDomain())
	add(h.orgTenantDeps.OTECHFQDN)
	for _, p := range h.poolDomainsForOrgCreate(h.orgTenantDeps) {
		add(p.Name)
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// ── Overrides store (ConfigMap) ──────────────────────────────────────────

// readSidebarOverrides loads the stored mapping. found=false (and a zero
// value) when the ConfigMap does not exist yet — a fresh Sovereign has no
// mapping and renders pure defaults.
func readSidebarOverrides(ctx context.Context, dyn dynamic.Interface) (SidebarOverrides, bool, error) {
	if dyn == nil {
		return SidebarOverrides{}, false, fmt.Errorf("nil dynamic client")
	}
	obj, err := dyn.Resource(cmGVR).Namespace(sidebarOverridesNamespace).Get(ctx, sidebarOverridesConfigMap, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return SidebarOverrides{}, false, nil
		}
		return SidebarOverrides{}, false, fmt.Errorf("get configmap %s/%s: %w", sidebarOverridesNamespace, sidebarOverridesConfigMap, err)
	}
	raw, _, _ := unstructured.NestedString(obj.Object, "data", sidebarOverridesDataKey)
	ov, err := decodeSidebarOverrides(raw)
	if err != nil {
		return SidebarOverrides{}, true, fmt.Errorf("decode %s: %w", sidebarOverridesDataKey, err)
	}
	return ov, true, nil
}

// decodeSidebarOverrides parses the ConfigMap payload; an empty payload is
// an empty mapping, never an error.
func decodeSidebarOverrides(raw string) (SidebarOverrides, error) {
	var ov SidebarOverrides
	if strings.TrimSpace(raw) == "" {
		return SidebarOverrides{Entries: []SidebarOverride{}}, nil
	}
	if err := json.Unmarshal([]byte(raw), &ov); err != nil {
		return SidebarOverrides{}, err
	}
	if ov.Entries == nil {
		ov.Entries = []SidebarOverride{}
	}
	return ov, nil
}

// writeSidebarOverrides persists the mapping (create-or-update). The actor
// is recorded as an annotation so the ConfigMap itself answers "who mapped
// this" without a second audit surface.
func writeSidebarOverrides(ctx context.Context, dyn dynamic.Interface, ov SidebarOverrides, actor string, now time.Time) error {
	if dyn == nil {
		return fmt.Errorf("nil dynamic client")
	}
	if ov.Entries == nil {
		ov.Entries = []SidebarOverride{}
	}
	payload, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return fmt.Errorf("encode overrides: %w", err)
	}
	annotations := map[string]interface{}{
		"catalyst.openova.io/updated-at": now.UTC().Format(time.RFC3339),
	}
	if actor = strings.TrimSpace(actor); actor != "" {
		annotations["catalyst.openova.io/updated-by"] = actor
	}

	res := dyn.Resource(cmGVR).Namespace(sidebarOverridesNamespace)
	existing, err := res.Get(ctx, sidebarOverridesConfigMap, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get configmap: %w", err)
		}
		obj := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":      sidebarOverridesConfigMap,
				"namespace": sidebarOverridesNamespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by":  "catalyst-api",
					"catalyst.openova.io/component": "console-ui",
				},
				"annotations": annotations,
			},
			"data": map[string]interface{}{
				sidebarOverridesDataKey: string(payload),
			},
		}}
		if _, err := res.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create configmap: %w", err)
		}
		return nil
	}
	if err := unstructured.SetNestedField(existing.Object, string(payload), "data", sidebarOverridesDataKey); err != nil {
		return fmt.Errorf("set data: %w", err)
	}
	merged := existing.GetAnnotations()
	if merged == nil {
		merged = map[string]string{}
	}
	for k, v := range annotations {
		merged[k] = v.(string)
	}
	existing.SetAnnotations(merged)
	if _, err := res.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update configmap: %w", err)
	}
	return nil
}

// ── GET / PUT sidebar-overrides ──────────────────────────────────────────

// requireSidebarAdmin — the mapping is a Sovereign-wide setting: an
// Org-scoped session is refused outright (its console never renders these
// entries, #4110) and the caller must clear the same tier-admin gate every
// other Sovereign mutation clears (applicationInstallCallerAuthorized).
// nil claims (no auth wired in tests) permit, mirroring
// requireResourceMutationAuth.
func (h *Handler) requireSidebarAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return true
	}
	if claimsAreOrgScoped(claims) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden",
			"code":   "403",
			"detail": "the Sovereign console menu is a sovereign-admin setting; an Organization-scoped session cannot read or change it",
		})
		return false
	}
	if !applicationInstallCallerAuthorized(claims) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden",
			"code":   "403",
			"detail": "the Sovereign console menu requires tier-admin or higher",
		})
		return false
	}
	return true
}

// sidebarCluster resolves the {id} path param to the cache's cluster id and
// the per-cluster dynamic client the overrides ConfigMap lives behind.
func (h *Handler) sidebarCluster(w http.ResponseWriter, r *http.Request) (string, dynamic.Interface, bool) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return "", nil, false
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return "", nil, false
	}
	clusterID = h.resolveChrootClusterID(clusterID)
	dyn, err := h.k8sCache.DynamicClientFor(clusterID)
	if err != nil {
		http.Error(w, fmt.Sprintf("cluster client: %v", err), http.StatusBadGateway)
		return "", nil, false
	}
	return clusterID, dyn, true
}

// HandleConsoleUISidebarOverridesGet — GET .../console-ui/sidebar-overrides.
func (h *Handler) HandleConsoleUISidebarOverridesGet(w http.ResponseWriter, r *http.Request) {
	if !h.requireSidebarAdmin(w, r) {
		return
	}
	_, dyn, ok := h.sidebarCluster(w, r)
	if !ok {
		return
	}
	ov, _, err := readSidebarOverrides(r.Context(), dyn)
	if err != nil {
		http.Error(w, fmt.Sprintf("read overrides: %v", err), http.StatusBadGateway)
		return
	}
	if ov.Entries == nil {
		ov.Entries = []SidebarOverride{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries":      ov.Entries,
		"parents":      sidebarParentIDs,
		"allowedHosts": h.sidebarAllowedHosts(),
		"namespace":    sidebarOverridesNamespace,
		"name":         sidebarOverridesConfigMap,
	})
}

// HandleConsoleUISidebarOverridesPut — PUT .../console-ui/sidebar-overrides.
//
// Response codes: 200 written · 400 body/validation (problems listed) ·
// 403 not sovereign-admin · 502 cluster client / write failure · 503 cache
// disabled.
func (h *Handler) HandleConsoleUISidebarOverridesPut(w http.ResponseWriter, r *http.Request) {
	if !h.requireSidebarAdmin(w, r) {
		return
	}
	var body SidebarOverrides
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":    "invalid-request-body",
			"code":     "400",
			"problems": []string{err.Error()},
		})
		return
	}
	if body.Entries == nil {
		body.Entries = []SidebarOverride{}
	}
	for i := range body.Entries {
		body.Entries[i].ID = strings.TrimSpace(body.Entries[i].ID)
		body.Entries[i].Label = strings.TrimSpace(body.Entries[i].Label)
		body.Entries[i].Route = strings.TrimSpace(body.Entries[i].Route)
		body.Entries[i].Parent = strings.TrimSpace(body.Entries[i].Parent)
	}
	if problems := validateSidebarOverrides(body, h.sidebarAllowedHosts()); len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":    "invalid-sidebar-overrides",
			"code":     "400",
			"problems": problems,
		})
		return
	}
	clusterID, dyn, ok := h.sidebarCluster(w, r)
	if !ok {
		return
	}
	actor := ""
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		actor = claims.Email
	}
	now := time.Now()
	if err := writeSidebarOverrides(r.Context(), dyn, body, actor, now); err != nil {
		h.log.Error("console-ui: overrides write failed", "cluster", clusterID, "err", err)
		http.Error(w, fmt.Sprintf("write overrides: %v", err), http.StatusBadGateway)
		return
	}
	h.log.Info("console-ui: sidebar overrides written",
		"cluster", clusterID, "entries", len(body.Entries), "actor", actor)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries":   body.Entries,
		"appliedAt": now.UTC().Format(time.RFC3339),
		"namespace": sidebarOverridesNamespace,
		"name":      sidebarOverridesConfigMap,
	})
}
