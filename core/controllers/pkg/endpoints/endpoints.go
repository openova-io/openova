// Package endpoints is the ONE place an Application endpoint's public
// hostname is derived (ADR-0015 D2/D7, Refs #7067). catalyst-api reports it
// (GET /catalyst/v1/apps/{uid}/endpoints) and the application-controller
// renders it into the chart (openova.endpoints → e.g. the HTTPRoute host), so
// the address the console shows and the route that serves it can never
// disagree.
package endpoints

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Vars are the substitutions a Blueprint hostnameTemplate may use.
type Vars struct {
	SovereignFQDN string
	OrgSlug       string
	AppName       string
	OrgDomain     string
}

// ErrUnresolved — the template cannot produce a valid hostname.
var ErrUnresolved = errors.New("hostnameTemplate unresolved")

// ResolveHostname substitutes {SovereignFQDN} {OrgSlug} {AppName} {OrgDomain}
// (and their Go-template spellings) and validates the result.
func ResolveHostname(tmpl string, v Vars) (string, error) {
	raw := strings.TrimSpace(tmpl)
	if raw == "" {
		return "", fmt.Errorf("%w: empty hostnameTemplate", ErrUnresolved)
	}
	rep := strings.NewReplacer(
		"{SovereignFQDN}", v.SovereignFQDN, "{OrgSlug}", v.OrgSlug, "{AppName}", v.AppName, "{OrgDomain}", v.OrgDomain,
		"{{.SovereignFQDN}}", v.SovereignFQDN, "{{ .SovereignFQDN }}", v.SovereignFQDN,
		"{{.OrgSlug}}", v.OrgSlug, "{{ .OrgSlug }}", v.OrgSlug,
		"{{.AppName}}", v.AppName, "{{ .AppName }}", v.AppName,
		"{{.OrgDomain}}", v.OrgDomain, "{{ .OrgDomain }}", v.OrgDomain,
	)
	host := strings.ToLower(strings.TrimSpace(rep.Replace(raw)))
	if host == "" {
		return "", fmt.Errorf("%w: %q resolved to the empty string", ErrUnresolved, tmpl)
	}
	if strings.ContainsAny(host, "{}") {
		return "", fmt.Errorf("%w: %q → %q still carries an unsubstituted token "+
			"(supported: {SovereignFQDN} {OrgSlug} {AppName} {OrgDomain})", ErrUnresolved, tmpl, host)
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return "", fmt.Errorf("%w: %q → %q has an empty DNS label (a token substituted to \"\")", ErrUnresolved, tmpl, host)
	}
	return host, nil
}

// OrgDomain is the domain an Organization's app hosts live under:
// `<tenantPublic.subdomain|slug>.<tenantPublic.parentDomain>` for an
// Organization with a public pool parent, else `<slug>.<sovereign-fqdn>`
// (an internal Organization such as the Sovereign's own). Returns "" only
// when neither a slug nor a Sovereign FQDN is known.
func OrgDomain(org *unstructured.Unstructured, slug, sovereignFQDN string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if org != nil {
		if s, _, _ := unstructured.NestedString(org.Object, "spec", "slug"); strings.TrimSpace(s) != "" && slug == "" {
			slug = strings.ToLower(strings.TrimSpace(s))
		}
		parent, _, _ := unstructured.NestedString(org.Object, "spec", "tenantPublic", "parentDomain")
		parent = strings.ToLower(strings.TrimSpace(parent))
		if parent != "" {
			sub, _, _ := unstructured.NestedString(org.Object, "spec", "tenantPublic", "subdomain")
			sub = strings.ToLower(strings.TrimSpace(sub))
			if sub == "" {
				sub = slug
			}
			if sub != "" {
				return sub + "." + parent
			}
		}
	}
	fqdn := strings.ToLower(strings.TrimSpace(sovereignFQDN))
	if slug == "" || fqdn == "" {
		return ""
	}
	return slug + "." + fqdn
}

// Endpoint is one resolved public endpoint of an Application.
type Endpoint struct {
	Name          string
	Hostname      string
	Port          int64
	TLS           bool
	LaunchDefault bool
}

// FromBlueprint resolves every Blueprint spec.endpoints[] entry with
// visibility public (or unset) to a hostname. Entries whose template does
// not resolve are skipped and reported in errs.
func FromBlueprint(bp *unstructured.Unstructured, v Vars) (out []Endpoint, errs []error) {
	if bp == nil {
		return nil, nil
	}
	list, _, _ := unstructured.NestedSlice(bp.Object, "spec", "endpoints")
	for _, raw := range list {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if vis, _ := m["visibility"].(string); vis != "" && vis != "public" {
			continue
		}
		tmpl, _ := m["hostnameTemplate"].(string)
		host, err := ResolveHostname(tmpl, v)
		name, _ := m["name"].(string)
		if err != nil {
			errs = append(errs, fmt.Errorf("endpoint %q: %w", name, err))
			continue
		}
		ep := Endpoint{Name: name, Hostname: host}
		switch p := m["port"].(type) {
		case int64:
			ep.Port = p
		case float64:
			ep.Port = int64(p)
		case int:
			ep.Port = int64(p)
		}
		ep.TLS, _ = m["tls"].(bool)
		ep.LaunchDefault, _ = m["launchDefault"].(bool)
		out = append(out, ep)
	}
	return out, errs
}
