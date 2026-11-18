// Package conformance holds TestProviderPacks: the test that every provider
// pack below infra/providers meets module contract version 1
// (infra/providers/_contract/contract.json, ADR-0016). It is the Go half of
// gate check CLD-G03; the other half is `tofu test` in every module, whose
// tests/contract.tftest.hcl plans the module with the contract's fixtures.
//
// The test reads files only. It starts no OpenTofu, needs no credential and
// reaches no network. scripts/check-provider-contract.sh runs it, and its
// --self-test breaks a copy of infra/providers once per rule to prove that
// each rule can fail (the copy is named by CONFORMANCE_PROVIDERS_DIR).
package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
)

const (
	// contractMarker is the line of a module's tests/contract.tftest.hcl after
	// which the file is the shared body, byte for byte.
	contractMarker = "# --- contract body ---\n"
	// sharedTemplate is the tail of the path of the one control-plane
	// template every module renders.
	sharedTemplate = "_shared/cloudinit-control-plane.tftpl"
	// defaultRegistry is the registry of an OpenTofu provider source that
	// names none.
	defaultRegistry = "registry.opentofu.org"
	// The Kubernetes node port range. A front door reaches a node on a host
	// port (443, 80, 53); a module never targets a port of this range.
	nodePortMin, nodePortMax = 30000, 32767
)

// portAttributes are the attribute names that carry a port in a
// load-balancer target, a listener, a health check or a firewall rule.
var portAttributes = map[string]bool{
	"port": true, "destination_port": true, "listen_port": true, "protocol_port": true,
	"port_range_min": true, "port_range_max": true, "internal": true, "external": true,
}

// repoRoot is the root of this repository, seven directories above this
// package.
func repoRoot() string {
	return filepath.Join("..", "..", "..", "..", "..", "..", "..")
}

// providersDir is infra/providers of this repository, or the directory in
// CONFORMANCE_PROVIDERS_DIR.
func providersDir() string {
	if d := os.Getenv("CONFORMANCE_PROVIDERS_DIR"); d != "" {
		return d
	}
	return filepath.Join(repoRoot(), "infra", "providers")
}

// contractFile is the part of _contract/contract.json this test reads.
type contractFile struct {
	Version   string `json:"version"`
	Variables []struct {
		Name      string          `json:"name"`
		Type      string          `json:"type"`
		Required  bool            `json:"required"`
		Sensitive bool            `json:"sensitive"`
		Default   json.RawMessage `json:"default"`
	} `json:"variables"`
	Outputs []struct {
		Name string `json:"name"`
	} `json:"outputs"`
	TemplateInputs []string `json:"template_inputs"`
}

// module is what the test needs of one OpenTofu module: the blocks of every
// *.tf file of one directory.
type module struct {
	dir       string
	variables map[string]*hclsyntax.Block
	outputs   map[string]*hclsyntax.Block
	resources []*hclsyntax.Block
	providers map[string]bool // fully qualified required_providers sources
	bodies    []*hclsyntax.Body
}

// parseModule reads every *.tf file of dir. ok is false when dir has none.
func parseModule(dir string) (m *module, ok bool, err error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, false, err
	}
	if len(files) == 0 {
		return nil, false, nil
	}
	sort.Strings(files)
	m = &module{dir: dir, variables: map[string]*hclsyntax.Block{}, outputs: map[string]*hclsyntax.Block{}, providers: map[string]bool{}}
	parser := hclparse.NewParser()
	for _, file := range files {
		f, diags := parser.ParseHCLFile(file)
		if diags.HasErrors() {
			return nil, false, fmt.Errorf("%s: %s", file, diags.Error())
		}
		body, isSyntax := f.Body.(*hclsyntax.Body)
		if !isSyntax {
			return nil, false, fmt.Errorf("%s: not native HCL syntax", file)
		}
		m.bodies = append(m.bodies, body)
		for _, b := range body.Blocks {
			switch {
			case b.Type == "variable" && len(b.Labels) == 1:
				m.variables[b.Labels[0]] = b
			case b.Type == "output" && len(b.Labels) == 1:
				m.outputs[b.Labels[0]] = b
			case b.Type == "resource" && len(b.Labels) == 2:
				m.resources = append(m.resources, b)
			case b.Type == "terraform":
				if err := m.addRequiredProviders(b); err != nil {
					return nil, false, fmt.Errorf("%s: %w", file, err)
				}
			}
		}
	}
	return m, true, nil
}

// addRequiredProviders records the source of every entry of the
// required_providers block inside a terraform block.
func (m *module) addRequiredProviders(terraform *hclsyntax.Block) error {
	for _, b := range terraform.Body.Blocks {
		if b.Type != "required_providers" {
			continue
		}
		for name, attr := range b.Body.Attributes {
			val, diags := attr.Expr.Value(nil)
			if diags.HasErrors() || !val.Type().IsObjectType() || !val.Type().HasAttribute("source") {
				return fmt.Errorf("required_providers.%s: no literal source", name)
			}
			m.providers[qualifiedSource(val.GetAttr("source").AsString())] = true
		}
	}
	return nil
}

// qualifiedSource is a provider source with its registry: a source of two
// parts (namespace/name) gets the default registry.
func qualifiedSource(source string) string {
	if strings.Count(source, "/") == 1 {
		return defaultRegistry + "/" + source
	}
	return source
}

// canonicalType renders a type constraint and its optional-attribute defaults
// in one form, so that two spellings of one type compare equal: object
// attributes are sorted, and an optional attribute carries its default.
func canonicalType(ty cty.Type, defaults *typeexpr.Defaults) string {
	switch {
	case ty.IsObjectType():
		names := make([]string, 0, len(ty.AttributeTypes()))
		for name := range ty.AttributeTypes() {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			var child *typeexpr.Defaults
			if defaults != nil {
				child = defaults.Children[name]
			}
			part := name + "=" + canonicalType(ty.AttributeType(name), child)
			if ty.AttributeOptional(name) {
				part += " optional"
				if defaults != nil {
					if v, has := defaults.DefaultValues[name]; has {
						raw, err := ctyjson.Marshal(v, v.Type())
						if err != nil {
							raw = []byte("unprintable")
						}
						part += " default " + string(raw)
					}
				}
			}
			parts = append(parts, part)
		}
		return "object{" + strings.Join(parts, ", ") + "}"
	case ty.IsListType(), ty.IsSetType(), ty.IsMapType():
		var child *typeexpr.Defaults
		if defaults != nil {
			child = defaults.Children[""]
		}
		kind := "list"
		if ty.IsSetType() {
			kind = "set"
		} else if ty.IsMapType() {
			kind = "map"
		}
		return kind + "(" + canonicalType(ty.ElementType(), child) + ")"
	default:
		return ty.FriendlyNameForConstraint()
	}
}

// typeOf parses a type constraint expression.
func typeOf(expr hcl.Expression) (cty.Type, string, error) {
	ty, defaults, diags := typeexpr.TypeConstraintWithDefaults(expr)
	if diags.HasErrors() {
		return cty.NilType, "", fmt.Errorf("%s", diags.Error())
	}
	return ty, canonicalType(ty, defaults), nil
}

// checkVariables compares the module's variables with the contract's: every
// contract variable is declared with the same type and the same sensitive
// flag; a required one has no default; an optional one has a default, and
// the contract's default where the contract sets one.
func checkVariables(c *contractFile, m *module) []string {
	var problems []string
	for _, want := range c.Variables {
		block, declared := m.variables[want.Name]
		if !declared {
			problems = append(problems, fmt.Sprintf("variable %q is not declared", want.Name))
			continue
		}
		wantExpr, diags := hclsyntax.ParseExpression([]byte(want.Type), "contract.json", hcl.InitialPos)
		if diags.HasErrors() {
			problems = append(problems, fmt.Sprintf("variable %q: the contract's type does not parse: %s", want.Name, diags.Error()))
			continue
		}
		wantType, wantCanonical, err := typeOf(wantExpr)
		if err != nil {
			problems = append(problems, fmt.Sprintf("variable %q: the contract's type: %v", want.Name, err))
			continue
		}
		typeAttr, hasType := block.Body.Attributes["type"]
		if !hasType {
			problems = append(problems, fmt.Sprintf("variable %q has no type; the contract says %s", want.Name, want.Type))
			continue
		}
		_, gotCanonical, err := typeOf(typeAttr.Expr)
		if err != nil {
			problems = append(problems, fmt.Sprintf("variable %q: type: %v", want.Name, err))
			continue
		}
		if gotCanonical != wantCanonical {
			problems = append(problems, fmt.Sprintf("variable %q has the type %s; the contract says %s", want.Name, gotCanonical, wantCanonical))
		}

		sensitive := false
		if attr, has := block.Body.Attributes["sensitive"]; has {
			val, diags := attr.Expr.Value(nil)
			if diags.HasErrors() || val.Type() != cty.Bool {
				problems = append(problems, fmt.Sprintf("variable %q: sensitive is not a literal true or false", want.Name))
				continue
			}
			sensitive = val.True()
		}
		if sensitive != want.Sensitive {
			problems = append(problems, fmt.Sprintf("variable %q has sensitive = %t; the contract says %t", want.Name, sensitive, want.Sensitive))
		}

		defaultAttr, hasDefault := block.Body.Attributes["default"]
		contractDefault := strings.TrimSpace(string(want.Default))
		switch {
		case want.Required && hasDefault:
			problems = append(problems, fmt.Sprintf("variable %q has a default; the contract says it is required", want.Name))
		case !want.Required && !hasDefault:
			problems = append(problems, fmt.Sprintf("variable %q has no default; the contract says it is optional", want.Name))
		case !want.Required && contractDefault != "" && contractDefault != "null":
			wantVal, err := ctyjson.Unmarshal(want.Default, wantType)
			if err != nil {
				problems = append(problems, fmt.Sprintf("variable %q: the contract's default is not of its type: %v", want.Name, err))
				continue
			}
			gotVal, diags := defaultAttr.Expr.Value(nil)
			if diags.HasErrors() {
				problems = append(problems, fmt.Sprintf("variable %q: default is not a literal value", want.Name))
				continue
			}
			gotVal, err = convert.Convert(gotVal, wantType)
			if err != nil || !gotVal.RawEquals(wantVal) {
				problems = append(problems, fmt.Sprintf("variable %q has another default than the contract's %s", want.Name, contractDefault))
			}
		}
	}
	return problems
}

// checkOutputs: every contract output is declared.
func checkOutputs(c *contractFile, m *module) []string {
	var problems []string
	for _, want := range c.Outputs {
		if _, declared := m.outputs[want.Name]; !declared {
			problems = append(problems, fmt.Sprintf("output %q is not declared", want.Name))
		}
	}
	return problems
}

// checkTofuProviders: the provider sources pack.yaml names are exactly those
// the module declares, with those of every unit below day2/ and of the seal
// hook in seal/ when the pack has them.
func checkTofuProviders(pack *cloudprofile.Pack, m *module) ([]string, error) {
	declared := map[string]bool{}
	for source := range m.providers {
		declared[source] = true
	}
	units, err := filepath.Glob(filepath.Join(m.dir, "day2", "*"))
	if err != nil {
		return nil, err
	}
	for _, dir := range append(units, filepath.Join(m.dir, "seal")) {
		unit, ok, err := parseModule(dir)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for source := range unit.providers {
			declared[source] = true
		}
	}
	named := map[string]bool{}
	for _, p := range pack.Spec.TofuProviders {
		named[qualifiedSource(p.Source)] = true
	}
	var problems []string
	for source := range declared {
		if !named[source] {
			problems = append(problems, fmt.Sprintf("the module declares the provider %s; pack.yaml spec.tofuProviders does not name it", source))
		}
	}
	for source := range named {
		if !declared[source] {
			problems = append(problems, fmt.Sprintf("pack.yaml spec.tofuProviders names %s; no required_providers block declares it", source))
		}
	}
	sort.Strings(problems)
	return problems, nil
}

// checkBootData: boot data never replaces a node. Every resource that sets
// user_data lists it in lifecycle.ignore_changes.
func checkBootData(m *module) []string {
	var problems []string
	for _, r := range m.resources {
		if _, sets := r.Body.Attributes["user_data"]; !sets {
			continue
		}
		ignored := false
		for _, b := range r.Body.Blocks {
			if b.Type != "lifecycle" {
				continue
			}
			attr, has := b.Body.Attributes["ignore_changes"]
			if !has {
				continue
			}
			if hcl.ExprAsKeyword(attr.Expr) == "all" {
				ignored = true
				continue
			}
			list, diags := hcl.ExprList(attr.Expr)
			if diags.HasErrors() {
				continue
			}
			for _, item := range list {
				if traversal, diags := hcl.AbsTraversalForExpr(item); !diags.HasErrors() && traversal.RootName() == "user_data" {
					ignored = true
				}
			}
		}
		if !ignored {
			problems = append(problems, fmt.Sprintf("resource %s.%s sets user_data and does not list it in lifecycle.ignore_changes", r.Labels[0], r.Labels[1]))
		}
	}
	return problems
}

// nodePortsIn returns the ports of the node port range a literal names: a
// number, a quoted number, or a quoted range "low-high".
func nodePortsIn(expr hclsyntax.Expression) (hit string, found bool) {
	val, diags := expr.Value(nil)
	if diags.HasErrors() || !val.IsKnown() || val.IsNull() {
		return "", false
	}
	text := ""
	switch val.Type() {
	case cty.Number:
		text = val.AsBigFloat().Text('f', -1)
	case cty.String:
		text = val.AsString()
	default:
		return "", false
	}
	low, high, isRange := strings.Cut(text, "-")
	lowPort, err := strconv.Atoi(strings.TrimSpace(low))
	if err != nil {
		return "", false
	}
	highPort := lowPort
	if isRange {
		if highPort, err = strconv.Atoi(strings.TrimSpace(high)); err != nil {
			return "", false
		}
	}
	if lowPort <= nodePortMax && highPort >= nodePortMin {
		return text, true
	}
	return "", false
}

// checkNoNodePort: no resource of the module carries a port of the node
// port range, at any depth of its nested blocks.
func checkNoNodePort(m *module) []string {
	var problems []string
	var walk func(owner string, body *hclsyntax.Body)
	walk = func(owner string, body *hclsyntax.Body) {
		names := make([]string, 0, len(body.Attributes))
		for name := range body.Attributes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !portAttributes[name] {
				continue
			}
			if hit, found := nodePortsIn(body.Attributes[name].Expr); found {
				problems = append(problems, fmt.Sprintf("resource %s has %s = %s, inside the node port range %d to %d", owner, name, hit, nodePortMin, nodePortMax))
			}
		}
		for _, b := range body.Blocks {
			walk(owner, b.Body)
		}
	}
	for _, r := range m.resources {
		walk(r.Labels[0]+"."+r.Labels[1], r.Body)
	}
	return problems
}

// checkContractTest: tests/contract.tftest.hcl holds the module's own header,
// the marker line once, and then the shared body byte for byte.
func checkContractTest(moduleDir string, body []byte) []string {
	file := filepath.Join(moduleDir, "tests", "contract.tftest.hcl")
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return []string{fmt.Sprintf("tests/contract.tftest.hcl cannot be read: %v", err)}
	}
	text := string(data)
	if strings.Count(text, contractMarker) != 1 {
		return []string{fmt.Sprintf("tests/contract.tftest.hcl holds the marker line %q %d times, not once", strings.TrimSpace(contractMarker), strings.Count(text, contractMarker))}
	}
	_, after, _ := strings.Cut(text, contractMarker)
	if after != string(body) {
		return []string{"tests/contract.tftest.hcl differs after the marker line from _contract/contract.tftest.body.hcl"}
	}
	return nil
}

// templateArguments returns the argument names of every call that renders
// the shared control-plane template, one set per call.
func templateArguments(m *module) ([]map[string]bool, error) {
	var calls []map[string]bool
	var failure error
	for _, body := range m.bodies {
		_ = hclsyntax.VisitAll(body, func(node hclsyntax.Node) hcl.Diagnostics {
			call, isCall := node.(*hclsyntax.FunctionCallExpr)
			if !isCall || call.Name != "templatefile" || len(call.Args) != 2 {
				return nil
			}
			if !strings.Contains(string(call.Args[0].Range().SliceBytes(mustRead(call.Args[0].Range().Filename))), sharedTemplate) {
				return nil
			}
			object, isObject := call.Args[1].(*hclsyntax.ObjectConsExpr)
			if !isObject {
				failure = fmt.Errorf("%s: the arguments of the shared template are not an object literal", call.Range())
				return nil
			}
			names := map[string]bool{}
			for _, item := range object.Items {
				key, diags := item.KeyExpr.Value(nil)
				if diags.HasErrors() || key.Type() != cty.String {
					failure = fmt.Errorf("%s: an argument name of the shared template is not a literal", item.KeyExpr.Range())
					return nil
				}
				names[key.AsString()] = true
			}
			calls = append(calls, names)
			return nil
		})
	}
	return calls, failure
}

// mustRead reads a file that was parsed a moment ago.
func mustRead(file string) []byte {
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil
	}
	return data
}

// report fails t with one line per problem.
func report(t *testing.T, problems []string) {
	t.Helper()
	for _, p := range problems {
		t.Error(p)
	}
}

// TestProviderPacks checks every pack below providersDir() against module
// contract version 1.
func TestProviderPacks(t *testing.T) {
	dir := providersDir()
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, cloudprofile.ContractDir, "contract.json")))
	if err != nil {
		t.Fatal(err)
	}
	var contract contractFile
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("contract.json: %v", err)
	}
	if contract.Version != "1" || len(contract.Variables) == 0 || len(contract.Outputs) == 0 || len(contract.TemplateInputs) == 0 {
		t.Fatalf("contract.json: version %q with %d variables, %d outputs and %d template inputs", contract.Version, len(contract.Variables), len(contract.Outputs), len(contract.TemplateInputs))
	}
	body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, cloudprofile.ContractDir, "contract.tftest.body.hcl")))
	if err != nil {
		t.Fatal(err)
	}
	// Load and LoadPacks validate every profile.yaml and pack.yaml against
	// their schema and compare each name with its directory.
	profiles, err := cloudprofile.Load(dir)
	if err != nil {
		t.Fatalf("profiles: %v", err)
	}
	packs, err := cloudprofile.LoadPacks(dir)
	if err != nil {
		t.Fatalf("packs: %v", err)
	}
	if len(packs) == 0 || len(packs) != len(profiles) {
		t.Fatalf("%d packs and %d profiles below %s", len(packs), len(profiles), dir)
	}
	names := make([]string, 0, len(packs))
	for name := range packs {
		names = append(names, name)
	}
	sort.Strings(names)

	modules := map[string]*module{}
	for _, name := range names {
		m, ok, err := parseModule(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !ok {
			t.Fatalf("%s: the pack has no *.tf file", name)
		}
		modules[name] = m
	}

	for _, name := range names {
		pack, m := packs[name], modules[name]
		t.Run(name, func(t *testing.T) {
			if pack.Spec.ContractVersion != contract.Version {
				t.Errorf("pack.yaml spec.contractVersion is %q; the contract is version %q", pack.Spec.ContractVersion, contract.Version)
			}
			t.Run("variables", func(t *testing.T) { report(t, checkVariables(&contract, m)) })
			t.Run("outputs", func(t *testing.T) { report(t, checkOutputs(&contract, m)) })
			t.Run("tofu providers", func(t *testing.T) {
				problems, err := checkTofuProviders(pack, m)
				if err != nil {
					t.Fatal(err)
				}
				report(t, problems)
			})
			t.Run("boot data", func(t *testing.T) { report(t, checkBootData(m)) })
			t.Run("no node port", func(t *testing.T) { report(t, checkNoNodePort(m)) })
			t.Run("contract test", func(t *testing.T) { report(t, checkContractTest(m.dir, body)) })

			// The three sub-tests below wait for the package that makes
			// them true for every pack. That package removes the Skip line.
			t.Run("adapter", func(t *testing.T) {
				t.Skip("until CLD-102")
				if _, err := os.Stat(filepath.Join(repoRoot(), filepath.FromSlash(pack.Spec.Adapter))); err != nil {
					t.Errorf("the adapter root of pack.yaml: %v", err)
				}
				if _, err := providers.Get(name); err != nil {
					t.Errorf("no adapter is registered: %v", err)
				}
			})
			t.Run("template inputs", func(t *testing.T) {
				t.Skip("until CLD-132")
				calls, err := templateArguments(m)
				if err != nil {
					t.Fatal(err)
				}
				if len(calls) == 0 {
					t.Fatalf("the module does not render %s", sharedTemplate)
				}
				// The arguments every module passes: the contract's
				// template inputs and the neutral arguments. A module
				// passes exactly those, in every call.
				first, err := templateArguments(modules[names[0]])
				if err != nil || len(first) == 0 {
					t.Fatalf("%s: no call of the shared template to compare with: %v", names[0], err)
				}
				for i, got := range calls {
					for _, input := range contract.TemplateInputs {
						if !got[input] {
							t.Errorf("call %d of the shared template does not pass the contract input %q", i+1, input)
						}
					}
					for arg := range got {
						if !first[0][arg] {
							t.Errorf("call %d of the shared template passes %q, which %s does not pass", i+1, arg, names[0])
						}
					}
					for arg := range first[0] {
						if !got[arg] {
							t.Errorf("call %d of the shared template does not pass %q, which %s passes", i+1, arg, names[0])
						}
					}
				}
			})
			t.Run("overlay", func(t *testing.T) {
				t.Skip("until CLD-144")
				if _, err := os.Stat(filepath.Join(repoRoot(), filepath.FromSlash(pack.Spec.Overlay))); err != nil {
					t.Errorf("the overlay root of pack.yaml: %v", err)
				}
			})
		})
	}
}
