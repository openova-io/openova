// endpoint_hostname_tokens_5389_test.go — every Blueprint's
// hostnameTemplate must be resolvable by the REAL resolver (#5389).
//
// # What this catches that nothing else did
//
// `resolveHostnameTemplate` substitutes a CLOSED set of tokens
// (SovereignFQDN / OrgSlug / AppName / OrgDomain, in both the single-curly
// and Go-template spellings). Any other token is left literal. Before #5389
// the function also failed OPEN — the unresolved literal was lowercased and
// emitted as a URL — so a Blueprint could ship a hostnameTemplate naming a
// token the engine has never implemented and the only symptom was a
// well-formed, dead launch URL.
//
// `platform/sandbox` did exactly that: `sandbox-{{.InstanceID}}.{{.OrgDomain}}`.
// `InstanceID` is documented on the Application CRD and is real
// (`Application.spec.instanceId`), and it IS implemented for `namingTemplate`
// — but it was never plumbed into hostname resolution. Documented-and-real
// is not the same as implemented-here, and that gap is invisible to a
// reviewer reading either file alone.
//
// # Why the oracle is the resolver itself
//
// This test does not hard-code a list of legal tokens. A hand-maintained
// list would drift from the replacer the moment someone adds a token, and a
// drifted allow-list fails open exactly like the bug it is guarding. Instead
// it feeds every on-disk hostnameTemplate through `resolveHostnameTemplate`
// with non-empty sentinel values and requires it to succeed — so the set of
// legal tokens is, by construction, whatever the shipped resolver actually
// substitutes. Add a token to the replacer and this test accepts it with no
// edit; remove one and this test starts failing on every Blueprint using it.
package handler

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRootFor5389Tokens walks up from this file to the monorepo root.
func repoRootFor5389Tokens(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "platform")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "products")); err == nil {
				return dir
			}
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not locate monorepo root (no platform/ + products/ ancestor)")
	return ""
}

// blueprintEndpointDoc is the sliver of a Blueprint this test reads.
type blueprintEndpointDoc struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Endpoints []struct {
			Name             string `yaml:"name"`
			HostnameTemplate string `yaml:"hostnameTemplate"`
		} `yaml:"endpoints"`
	} `yaml:"spec"`
}

// collectHostnameTemplates returns every (blueprintFile, endpoint, template)
// triple declared under platform/ and products/.
func collectHostnameTemplates(t *testing.T, root string) []struct{ File, Endpoint, Template string } {
	t.Helper()
	var out []struct{ File, Endpoint, Template string }
	for _, tree := range []string{"platform", "products"} {
		matches, err := filepath.Glob(filepath.Join(root, tree, "*", "blueprint.yaml"))
		if err != nil {
			t.Fatalf("glob %s: %v", tree, err)
		}
		for _, f := range matches {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			var doc blueprintEndpointDoc
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				// A Blueprint this test cannot parse is reported, never skipped
				// silently — a silent skip is how coverage quietly goes to zero.
				t.Errorf("%s: unparseable blueprint.yaml: %v", f, err)
				continue
			}
			rel, _ := filepath.Rel(root, f)
			for _, ep := range doc.Spec.Endpoints {
				if strings.TrimSpace(ep.HostnameTemplate) == "" {
					continue
				}
				out = append(out, struct{ File, Endpoint, Template string }{rel, ep.Name, ep.HostnameTemplate})
			}
		}
	}
	return out
}

// sentinel values — every field non-empty, so a failure can only mean an
// UNSUBSTITUTED token, never an empty substitution.
var tokenProbeVars = hostnameVars{
	SovereignFQDN: "t99.example.test",
	OrgSlug:       "probeorg",
	AppName:       "probeapp",
	OrgDomain:     "probeorg.example.homes",
}

// TestEveryBlueprintHostnameTemplateResolves_5389 is the guard.
func TestEveryBlueprintHostnameTemplateResolves_5389(t *testing.T) {
	root := repoRootFor5389Tokens(t)
	all := collectHostnameTemplates(t, root)

	// Anti-vacuity floor: this repo ships dozens of hostnameTemplates across
	// both trees. If the walker suddenly sees almost none, the glob or the
	// parse broke and a green run would prove nothing.
	const minTemplates = 25
	if len(all) < minTemplates {
		t.Fatalf("VACUITY: only %d hostnameTemplate(s) collected, expected >= %d — "+
			"the walker or the YAML shape broke, so a pass here is meaningless",
			len(all), minTemplates)
	}

	for _, e := range all {
		if _, err := resolveHostnameTemplate(e.Template, tokenProbeVars); err != nil {
			t.Errorf("%s endpoint %q: hostnameTemplate %q does not resolve: %v\n"+
				"  Every field of the probe is non-empty, so this is an UNIMPLEMENTED "+
				"token, not an empty value.\n"+
				"  resolveHostnameTemplate substitutes only SovereignFQDN / OrgSlug / "+
				"AppName / OrgDomain.\n"+
				"  A token that is real elsewhere (e.g. InstanceID on namingTemplate) "+
				"is still unimplemented HERE.\n"+
				"  Note the multiInstance namingTemplate already makes the Application "+
				"CR name instance-unique, so {{.AppName}} usually covers what "+
				"{{.InstanceID}} was reaching for.",
				e.File, e.Endpoint, e.Template, err)
		}
	}
}

// TestHostnameTokenProbe_IsDiscriminating_5389 proves the probe above can
// actually fail, in both directions. Without this, a resolver that returned
// nil unconditionally would make the guard silently vacuous.
func TestHostnameTokenProbe_IsDiscriminating_5389(t *testing.T) {
	// Negative: an unimplemented token MUST be rejected. This is the literal
	// pre-fix platform/sandbox value.
	if _, err := resolveHostnameTemplate("sandbox-{{.InstanceID}}.{{.OrgDomain}}", tokenProbeVars); err == nil {
		t.Error("DISCRIMINATION: an unimplemented {{.InstanceID}} token resolved without error — " +
			"the guard cannot detect the very defect it exists for")
	}

	// Positive: every implemented token, both spellings, MUST resolve.
	for _, ok := range []string{
		"{AppName}.{OrgDomain}",
		"{{.AppName}}.{{.OrgDomain}}",
		"{{ .AppName }}.{{ .OrgDomain }}",
		"auth.{{.SovereignFQDN}}",
		"{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}",
	} {
		if _, err := resolveHostnameTemplate(ok, tokenProbeVars); err != nil {
			t.Errorf("DISCRIMINATION: implemented template %q failed to resolve: %v — "+
				"the guard is too strict and would reject valid Blueprints", ok, err)
		}
	}
}
