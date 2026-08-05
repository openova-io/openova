// endpoint_org_domain.go — the `{OrgDomain}` hostname token (#5389).
//
// # What was broken
//
// Every per-Org Blueprint declared its front door as
// `<app>.{{.OrgSlug}}.{{.SovereignFQDN}}`, which resolves to e.g.
// `neo4j.uatco.hw292.omani.works`. Measured live on hw292 (dep
// 1c56518035a83e03, cutoverComplete=true) 2026-08-06, the count of live
// HTTPRoute hostnames matching `*.<org>.<SovereignFQDN>` was ZERO. Per-Org
// apps are served on the Organization's POOL domain — agenity.uatco.omani.homes,
// wordpress.uatco.omani.homes, console.uatco.omani.homes — because the org
// GitOps overlay derives every per-Org host from
// `console.<subdomain>.<parentDomain>` (organization_gitops.go:~820:
// `owHost := strings.Replace(host, "console.", "openclaw.", 1)`).
//
// So the console's Open button emitted a well-formed URL with no HTTPRoute,
// no DNS record and no certificate behind it. That is UAT rows 110/112/114:
// "launch does not land the user in the app".
//
// # Why no Blueprint could be written correctly before this file
//
// evaluateHostnameTemplate's vocabulary was exactly {SovereignFQDN},
// {OrgSlug}, {AppName}. The Organization's pool domain was never plumbed into
// endpoint resolution, so there was no token that could NAME the host the app
// is actually served on. Repointing the blueprints alone was impossible — the
// missing capability had to land first.
//
// # The token
//
// `{OrgDomain}` (and the `{{.OrgDomain}}` / `{{ .OrgDomain }}` Go-template
// aliases the exemplar blueprints use) resolves to the Organization's own
// domain suffix, derived from the SAME three Organization CR fields every
// other per-Org surface in this package reads — spec.slug,
// spec.tenantPublic.parentDomain, spec.tenantPublic.subdomain — via
// orgConsoleTLSRecordFromOrgCR / tenantRegistrationFromOrgCR:
//
//	pool Org (tenantPublic.parentDomain set): <subdomain|slug>.<parentDomain>
//	                                          → uatco.omani.homes
//	single-domain Org (no parentDomain):      <slug>.<SovereignFQDN>
//	                                          → uatco.hw292.omani.works
//
// The single-domain fallback is what makes `{OrgDomain}` a strict, safe
// REPLACEMENT for the old `{OrgSlug}.{SovereignFQDN}` composition rather than
// a second dialect: an Org with no pool parent rides the Sovereign apex
// wildcard, which is precisely the host the old composition produced. No
// existing behaviour regresses; the pool case stops being wrong.
//
// No new config knob is introduced — Inviolable Principle #4. The truth-source
// is the Organization CR the org-controller and the funnel already stamp.
package handler

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// orgDomainFromOrgCR derives the Organization's domain suffix from a single
// Organization CR, mirroring orgConsoleTLSRecordFromOrgCR +
// tenantRegistrationFromOrgCR field-for-field so all three agree on the host
// shape. Returns "" when the CR carries no pool parent (the caller applies the
// Sovereign-apex fallback) — never a partially substituted string.
func orgDomainFromOrgCR(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	parentDomain := strings.ToLower(strings.TrimSpace(
		nestedString(obj.Object, "spec", "tenantPublic", "parentDomain")))
	if parentDomain == "" {
		return ""
	}
	subdomain := strings.ToLower(strings.TrimSpace(
		nestedString(obj.Object, "spec", "tenantPublic", "subdomain")))
	if subdomain == "" {
		subdomain = strings.ToLower(strings.TrimSpace(
			nestedString(obj.Object, "spec", "slug")))
	}
	if subdomain == "" {
		subdomain = strings.ToLower(strings.TrimSpace(obj.GetName()))
	}
	if subdomain == "" {
		return ""
	}
	return subdomain + "." + parentDomain
}

// orgCRMatchesSlug reports whether an Organization CR is the one identified by
// `slug`. The Application's `catalyst.openova.io/organization` label carries
// the slug, but legacy Application CRs fall back to the NAMESPACE name
// (extractOrgFromApp) — which for a host-namespace Org is the slug and for a
// vcluster-tier Org is `org-<tenant-id>`. Match on spec.slug, the CR name, and
// the `openova.io/tenant-id` label so every one of those addresses resolves.
func orgCRMatchesSlug(obj *unstructured.Unstructured, slug string) bool {
	if obj == nil || slug == "" {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(slug))
	cands := []string{
		nestedString(obj.Object, "spec", "slug"),
		obj.GetName(),
		obj.GetLabels()["openova.io/tenant-id"],
	}
	for _, c := range cands {
		if strings.ToLower(strings.TrimSpace(c)) == want {
			return true
		}
	}
	// `org-<tenant-id>` / `org-<slug>` namespace form.
	if trimmed := strings.TrimPrefix(want, "org-"); trimmed != want && trimmed != "" {
		for _, c := range cands {
			if strings.ToLower(strings.TrimSpace(c)) == trimmed {
				return true
			}
		}
	}
	return false
}

// resolveOrgDomain answers the `{OrgDomain}` token for an Organization slug.
//
//   - slug == ""            → "" (a Sovereign-singleton app; its template must
//     not reference {OrgDomain} at all)
//   - Org CR with a pool parentDomain → <subdomain|slug>.<parentDomain>
//   - anything else (CR absent, API error, single-domain Org) →
//     <slug>.<sovereignFQDN>, the Sovereign apex-wildcard host that IS what the
//     pre-#5389 `{OrgSlug}.{SovereignFQDN}` composition produced.
//
// The fallback is deliberately never empty when a slug is known: an empty
// OrgDomain would make the resolver fail loud (see resolveHostnameTemplate) and
// dark the Open button for every single-domain Org, trading one broken link for
// a missing control.
func (h *Handler) resolveOrgDomain(ctx context.Context, client dynamic.Interface, slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return ""
	}
	fallback := ""
	if fqdn := strings.TrimSpace(h.endpointSovereignFQDN()); fqdn != "" {
		fallback = slug + "." + strings.ToLower(fqdn)
	}
	if client == nil {
		return fallback
	}
	list, err := client.Resource(OrganizationGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		if h.log != nil {
			h.log.Warn("endpoint: Organization list failed while resolving {OrgDomain}; "+
				"falling back to the Sovereign apex host",
				"org", slug, "fallback", fallback, "error", err.Error())
		}
		return fallback
	}
	for i := range list.Items {
		if !orgCRMatchesSlug(&list.Items[i], slug) {
			continue
		}
		if d := orgDomainFromOrgCR(&list.Items[i]); d != "" {
			return d
		}
		// Org exists but is single-domain — apex wildcard is correct.
		return fallback
	}
	return fallback
}
