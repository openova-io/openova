// Package cloudprofile reads the cloud profiles and the pack declarations of
// the provider packs: contract C2, whose two schemas live in
// infra/providers/_contract/. A profile is a data file, never a served
// resource (ADR-0016, decision K3).
//
// Layout read by Load and LoadPacks, below a providers directory (in this
// repository infra/providers, in the API image /infra/providers):
//
//	_contract/cloudprofile.schema.json   schema of profile.yaml
//	_contract/pack.schema.json           schema of pack.yaml
//	<name>/profile.yaml                  one per pack; metadata.name is <name>
//	<name>/pack.yaml                     one per pack; metadata.name is <name>
//
// A directory whose name starts with "_" is not a pack.
//
// An installation document has the same kind as a profile, sets spec.extends
// to a pack name and may set only the installation fields (see
// installationFields). Merge lays it over the pack profile: maps merge, lists
// and scalars replace. Installation documents are never stored in this
// repository.
//
// Every error about a document is a *FieldError that wraps one of the Err
// values below and names the field path, for example
// "spec.compute.flavors[0].vcpu". Test it with errors.Is.
//
// A Profile is read-only: Validate checks the document that Load, Parse or
// Merge read, not later changes to the struct fields.
package cloudprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"sigs.k8s.io/yaml"
)

// File and kind names of contract C2.
const (
	APIVersion        = "catalyst.openova.io/v1alpha1"
	ProfileKind       = "CloudProfile"
	PackKind          = "ProviderPack"
	ContractDir       = "_contract"
	ProfileSchemaFile = "cloudprofile.schema.json"
	PackSchemaFile    = "pack.schema.json"
	ProfileFile       = "profile.yaml"
	PackFile          = "pack.yaml"
)

// The typed errors. Each is wrapped in a *FieldError.
var (
	// ErrUnknownField: the document has a field the schema does not list.
	ErrUnknownField = errors.New("unknown field")
	// ErrMissingField: a required field is absent.
	ErrMissingField = errors.New("missing field")
	// ErrBadEnum: the value is not one of the allowed values.
	ErrBadEnum = errors.New("value not allowed")
	// ErrBadValue: the value has the wrong type, form or size, or contradicts
	// the directory it was read from.
	ErrBadValue = errors.New("bad value")
	// ErrNotInstallationField: an installation document sets a field that is
	// not an installation field.
	ErrNotInstallationField = errors.New("not an installation field")
	// ErrUnknownPack: spec.extends of an installation document names no pack
	// that was given.
	ErrUnknownPack = errors.New("unknown pack")
	// ErrIncomplete: the effective profile of a pack with
	// spec.requiresInstallation true has no region, no image or no flavor.
	ErrIncomplete = errors.New("incomplete profile")
)

// FieldError is an error about one field of a document.
type FieldError struct {
	Err    error  // one of the Err values of this package
	Path   string // field path, for example spec.compute.flavors[0].vcpu
	Detail string // what was found
}

func (e *FieldError) Error() string {
	if e.Detail == "" {
		return e.Path + ": " + e.Err.Error()
	}
	return e.Path + ": " + e.Err.Error() + ": " + e.Detail
}

func (e *FieldError) Unwrap() error { return e.Err }

// installationFields are the field paths an installation document may set
// below spec. A path covers everything below it.
var installationFields = map[string]bool{
	"spec.extends":          true,
	"spec.lifecycle":        true,
	"spec.api":              true,
	"spec.regions":          true,
	"spec.compute.image":    true,
	"spec.compute.flavors":  true,
	"spec.compute.defaults": true,
	"spec.network.apiCIDRs": true,
	"spec.registry":         true,
}

// Profile is a cloud profile, an installation document or an effective
// profile.
type Profile struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`

	doc    map[string]any
	schema *jsonschema.Schema
}

// Metadata of a profile or a pack.
type Metadata struct {
	Name string `json:"name"`
}

// Spec of a profile.
type Spec struct {
	Extends              string        `json:"extends,omitempty"`
	DisplayName          string        `json:"displayName"`
	Lifecycle            string        `json:"lifecycle"`
	ContractVersion      string        `json:"contractVersion"`
	RequiresInstallation bool          `json:"requiresInstallation,omitempty"`
	API                  *API          `json:"api,omitempty"`
	Credentials          Credentials   `json:"credentials"`
	Regions              []Region      `json:"regions"`
	Compute              Compute       `json:"compute"`
	Storage              Storage       `json:"storage"`
	ObjectStorage        ObjectStorage `json:"objectStorage"`
	Network              Network       `json:"network"`
	Seal                 *Seal         `json:"seal,omitempty"`
	Registry             *Registry     `json:"registry,omitempty"`
	Bootstrap            Bootstrap     `json:"bootstrap"`
	Capabilities         Capabilities  `json:"capabilities"`
	Day2                 Day2          `json:"day2"`
}

// API holds the endpoints of an installation.
type API struct {
	Domain      string `json:"domain,omitempty"`
	AuthURL     string `json:"authURL,omitempty"`
	InsecureTLS bool   `json:"insecureTLS,omitempty"`
}

// Credentials describes the credential map a provision needs.
type Credentials struct {
	Source string            `json:"source"`
	Fields []CredentialField `json:"fields"`
}

// CredentialField is one key of the credential map.
type CredentialField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
	Pattern  string `json:"pattern,omitempty"`
}

// Region is one region of the cloud.
type Region struct {
	Code        string   `json:"code"`
	Label       string   `json:"label"`
	Location    string   `json:"location"`
	Zones       []string `json:"zones"`
	DefaultZone string   `json:"defaultZone"`
}

// Compute holds the image, the flavors and the floors.
type Compute struct {
	Image          string          `json:"image"`
	Flavors        []Flavor        `json:"flavors"`
	Defaults       *FlavorDefaults `json:"defaults,omitempty"`
	MinWorkerCount *int            `json:"minWorkerCount,omitempty"`
	MinimumFlavor  *MinimumFlavor  `json:"minimumFlavor,omitempty"`
}

// MinWorkers is spec.compute.minWorkerCount, 1 when it is not set.
func (c Compute) MinWorkers() int {
	if c.MinWorkerCount == nil {
		return 1
	}
	return *c.MinWorkerCount
}

// Flavor is one machine size.
type Flavor struct {
	Name      string   `json:"name"`
	VCPU      int      `json:"vcpu"`
	MemoryGiB float64  `json:"memoryGiB"`
	Roles     []string `json:"roles"`
	Regions   []string `json:"regions,omitempty"`
	Price     *Price   `json:"price,omitempty"`
}

// Price is a display price; the price book stays with the billing engine.
type Price struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Per      string  `json:"per"`
}

// FlavorDefaults are the flavors a new Sovereign starts with.
type FlavorDefaults struct {
	ControlPlaneFlavor string `json:"controlPlaneFlavor"`
	WorkerFlavor       string `json:"workerFlavor"`
}

// MinimumFlavor holds the floors per role.
type MinimumFlavor struct {
	ControlPlane Floor `json:"controlPlane"`
	Worker       Floor `json:"worker"`
}

// Floor is the smallest machine a role accepts.
type Floor struct {
	VCPU      int     `json:"vcpu"`
	MemoryGiB float64 `json:"memoryGiB"`
}

// Storage names the default storage class and its CSI driver.
type Storage struct {
	DefaultClass string `json:"defaultClass"`
	Driver       string `json:"driver"`
}

// ObjectStorage holds the object-storage client settings.
type ObjectStorage struct {
	ChecksumAlgorithm string `json:"checksumAlgorithm"`
}

// Network is owned by area NET; fields join with their reader.
type Network struct {
	LoadBalancer LoadBalancer `json:"loadBalancer"`
	APICIDRs     []string     `json:"apiCIDRs,omitempty"`
}

// LoadBalancer says how a front door reaches the nodes.
type LoadBalancer struct {
	Mode string `json:"mode"`
}

// Seal is owned by area SEC (contract C8).
type Seal struct {
	Kind string `json:"kind"`
}

// Registry holds the registry mirror of an installation.
type Registry struct {
	MirrorEndpoint string `json:"mirrorEndpoint"`
}

// Bootstrap holds limits of the boot data.
type Bootstrap struct {
	UserDataLimitBytes int `json:"userDataLimitBytes"`
}

// Capabilities is the closed list of flags code may branch on.
type Capabilities struct {
	MultiRegion      bool `json:"multiRegion"`
	MultiCloudRegion bool `json:"multiCloudRegion"`
}

// Day2 lists the unit contracts the pack implements.
type Day2 struct {
	Contracts []string `json:"contracts"`
}

// Pack is a pack declaration, pack.yaml.
type Pack struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       PackSpec `json:"spec"`
}

// PackSpec is the spec of a pack declaration.
type PackSpec struct {
	ContractVersion string         `json:"contractVersion"`
	Module          string         `json:"module"`
	Overlay         string         `json:"overlay"`
	Adapter         string         `json:"adapter"`
	Charts          []string       `json:"charts,omitempty"`
	TofuProviders   []TofuProvider `json:"tofuProviders"`
	LeakTokens      []string       `json:"leakTokens"`
}

// TofuProvider is one OpenTofu provider, by fully qualified source.
type TofuProvider struct {
	Source string `json:"source"`
}

// Load reads <dir>/<name>/profile.yaml of every pack below dir, validates
// each against <dir>/_contract/cloudprofile.schema.json, and returns them by
// pack name.
func Load(dir string) (map[string]*Profile, error) {
	schema, err := compileSchema(filepath.Join(dir, ContractDir, ProfileSchemaFile))
	if err != nil {
		return nil, err
	}
	names, err := packNames(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Profile, len(names))
	for _, name := range names {
		file := filepath.Join(dir, name, ProfileFile)
		data, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return nil, err
		}
		doc, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		p, err := validated(doc, schema)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if p.Metadata.Name != name {
			return nil, fmt.Errorf("%s: %w", file, &FieldError{Err: ErrBadValue, Path: "metadata.name", Detail: fmt.Sprintf("%q, the directory is %q", p.Metadata.Name, name)})
		}
		if p.Spec.Extends != "" {
			return nil, fmt.Errorf("%s: %w", file, &FieldError{Err: ErrBadValue, Path: "spec.extends", Detail: "a pack profile extends nothing"})
		}
		out[name] = p
	}
	return out, nil
}

// LoadPacks reads <dir>/<name>/pack.yaml of every pack below dir, validates
// each against <dir>/_contract/pack.schema.json, and returns them by pack
// name. The name and the three pack roots must match the directory.
func LoadPacks(dir string) (map[string]*Pack, error) {
	schema, err := compileSchema(filepath.Join(dir, ContractDir, PackSchemaFile))
	if err != nil {
		return nil, err
	}
	names, err := packNames(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Pack, len(names))
	for _, name := range names {
		file := filepath.Join(dir, name, PackFile)
		data, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return nil, err
		}
		doc, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if err := schemaError(schema.Validate(doc)); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		var pk Pack
		if err := fromDoc(doc, &pk); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		want := map[string][2]string{
			"metadata.name": {pk.Metadata.Name, name},
			"spec.module":   {pk.Spec.Module, "infra/providers/" + name},
			"spec.overlay":  {pk.Spec.Overlay, "clusters/_template/providers/" + name},
			"spec.adapter":  {pk.Spec.Adapter, "products/catalyst/bootstrap/api/internal/providers/" + name},
		}
		for _, path := range []string{"metadata.name", "spec.module", "spec.overlay", "spec.adapter"} {
			if got := want[path]; got[0] != got[1] {
				return nil, fmt.Errorf("%s: %w", file, &FieldError{Err: ErrBadValue, Path: path, Detail: fmt.Sprintf("%q, want %q", got[0], got[1])})
			}
		}
		out[name] = &pk
	}
	return out, nil
}

// Parse decodes one profile or installation document. It does not validate:
// Load and Merge do.
func Parse(data []byte) (*Profile, error) {
	doc, err := decode(data)
	if err != nil {
		return nil, err
	}
	p := &Profile{doc: doc}
	if err := fromDoc(doc, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Validate checks the document of p against the schema it was loaded with,
// and checks that spec.regions is empty only when spec.requiresInstallation
// is true.
func Validate(p *Profile) error {
	if p == nil || p.schema == nil || p.doc == nil {
		return errors.New("cloudprofile: the profile was not read by Load or Merge")
	}
	if err := schemaError(p.schema.Validate(p.doc)); err != nil {
		return err
	}
	var spec struct {
		RequiresInstallation bool  `json:"requiresInstallation"`
		Regions              []any `json:"regions"`
	}
	if err := fromDoc(p.doc["spec"].(map[string]any), &spec); err != nil {
		return err
	}
	if !spec.RequiresInstallation && len(spec.Regions) == 0 {
		return &FieldError{Err: ErrMissingField, Path: "spec.regions", Detail: "empty, and spec.requiresInstallation is not true"}
	}
	return nil
}

// validated checks doc against schema and returns it as a Profile.
func validated(doc map[string]any, schema *jsonschema.Schema) (*Profile, error) {
	p := &Profile{doc: doc, schema: schema}
	if err := Validate(p); err != nil {
		return nil, err
	}
	if err := fromDoc(doc, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Merge lays the installation document over the pack profile and returns
// the effective profile: metadata.name is the installation's name and
// spec.extends the pack's name. Maps merge; lists and scalars replace.
func Merge(pack, installation *Profile) (*Profile, error) {
	if pack == nil || pack.schema == nil || pack.doc == nil {
		return nil, errors.New("cloudprofile: the pack profile was not read by Load")
	}
	if installation == nil || installation.doc == nil {
		return nil, errors.New("cloudprofile: the installation document was not read by Parse")
	}
	if pack.Spec.Extends != "" {
		return nil, &FieldError{Err: ErrBadValue, Path: "spec.extends", Detail: "the first argument is an installation, not a pack profile"}
	}
	if installation.APIVersion != APIVersion {
		return nil, &FieldError{Err: ErrBadEnum, Path: "apiVersion", Detail: strconv.Quote(installation.APIVersion)}
	}
	if installation.Kind != ProfileKind {
		return nil, &FieldError{Err: ErrBadEnum, Path: "kind", Detail: strconv.Quote(installation.Kind)}
	}
	if installation.Metadata.Name == "" {
		return nil, &FieldError{Err: ErrMissingField, Path: "metadata.name"}
	}
	if installation.Spec.Extends == "" {
		return nil, &FieldError{Err: ErrMissingField, Path: "spec.extends"}
	}
	if installation.Spec.Extends != pack.Metadata.Name {
		return nil, &FieldError{Err: ErrUnknownPack, Path: "spec.extends", Detail: fmt.Sprintf("%q, the pack is %q", installation.Spec.Extends, pack.Metadata.Name)}
	}
	if err := checkInstallationFields(installation.doc); err != nil {
		return nil, err
	}
	merged := deepCopy(pack.doc)
	mergeInto(merged, installation.doc)
	merged["metadata"] = map[string]any{"name": installation.Metadata.Name}
	merged["spec"].(map[string]any)["extends"] = pack.Metadata.Name
	eff, err := validated(merged, pack.schema)
	if err != nil {
		return nil, err
	}
	if eff.Spec.RequiresInstallation {
		switch {
		case len(eff.Spec.Regions) == 0:
			return nil, &FieldError{Err: ErrIncomplete, Path: "spec.regions", Detail: "no region"}
		case eff.Spec.Compute.Image == "":
			return nil, &FieldError{Err: ErrIncomplete, Path: "spec.compute.image", Detail: "no image"}
		case len(eff.Spec.Compute.Flavors) == 0:
			return nil, &FieldError{Err: ErrIncomplete, Path: "spec.compute.flavors", Detail: "no flavor"}
		}
	}
	return eff, nil
}

// checkInstallationFields refuses every field below spec that is not an
// installation field, and every metadata field but name.
func checkInstallationFields(doc map[string]any) error {
	for _, k := range sortedKeys(doc) {
		switch k {
		case "apiVersion", "kind":
		case "metadata":
			m, ok := doc[k].(map[string]any)
			if !ok {
				return &FieldError{Err: ErrBadValue, Path: k, Detail: "not a mapping"}
			}
			for _, mk := range sortedKeys(m) {
				if mk != "name" {
					return &FieldError{Err: ErrNotInstallationField, Path: "metadata." + mk}
				}
			}
		case "spec":
			m, ok := doc[k].(map[string]any)
			if !ok {
				return &FieldError{Err: ErrBadValue, Path: k, Detail: "not a mapping"}
			}
			if err := walkInstallation("spec", m); err != nil {
				return err
			}
		default:
			return &FieldError{Err: ErrUnknownField, Path: k}
		}
	}
	return nil
}

func walkInstallation(prefix string, m map[string]any) error {
	for _, k := range sortedKeys(m) {
		path := prefix + "." + k
		if installationFields[path] {
			continue
		}
		child, isMap := m[k].(map[string]any)
		if isMap && hasInstallationFieldBelow(path) {
			if err := walkInstallation(path, child); err != nil {
				return err
			}
			continue
		}
		return &FieldError{Err: ErrNotInstallationField, Path: path}
	}
	return nil
}

func hasInstallationFieldBelow(path string) bool {
	for f := range installationFields {
		if strings.HasPrefix(f, path+".") {
			return true
		}
	}
	return false
}

// packNames lists the directories below dir that are packs, sorted.
func packNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), "_") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func compileSchema(file string) (*jsonschema.Schema, error) {
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	url := "file:///" + filepath.Base(file)
	if err := c.AddResource(url, bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return s, nil
}

// decode turns YAML into the JSON value tree the schema validator reads.
func decode(data []byte) (map[string]any, error) {
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, &FieldError{Err: ErrBadValue, Path: "(document)", Detail: err.Error()}
	}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, &FieldError{Err: ErrBadValue, Path: "(document)", Detail: err.Error()}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, &FieldError{Err: ErrBadValue, Path: "(document)", Detail: "not a mapping"}
	}
	return m, nil
}

func fromDoc(doc map[string]any, into any) error {
	js, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(js, into); err != nil {
		return &FieldError{Err: ErrBadValue, Path: "(document)", Detail: err.Error()}
	}
	return nil
}

var quoted = regexp.MustCompile(`'([^']*)'`)

// schemaError turns the first failing leaf of a validation error into a
// *FieldError. Leaves are ordered by field path, then by keyword, so the
// result does not depend on map order.
func schemaError(err error) error {
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	var leaves []*jsonschema.ValidationError
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, e)
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	sort.SliceStable(leaves, func(i, j int) bool {
		if leaves[i].InstanceLocation != leaves[j].InstanceLocation {
			return leaves[i].InstanceLocation < leaves[j].InstanceLocation
		}
		return leaves[i].KeywordLocation < leaves[j].KeywordLocation
	})
	leaf := leaves[0]
	path := fieldPath(leaf.InstanceLocation)
	keyword := leaf.KeywordLocation[strings.LastIndex(leaf.KeywordLocation, "/")+1:]
	switch keyword {
	case "additionalProperties":
		return &FieldError{Err: ErrUnknownField, Path: join(path, firstQuoted(leaf.Message)), Detail: leaf.Message}
	case "required":
		return &FieldError{Err: ErrMissingField, Path: join(path, firstQuoted(leaf.Message)), Detail: leaf.Message}
	case "enum", "const":
		return &FieldError{Err: ErrBadEnum, Path: path, Detail: leaf.Message}
	default:
		return &FieldError{Err: ErrBadValue, Path: path, Detail: leaf.Message}
	}
}

func firstQuoted(s string) string {
	if m := quoted.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func join(path, name string) string {
	switch {
	case name == "":
		return path
	case path == "":
		return name
	}
	return path + "." + name
}

// fieldPath turns the JSON pointer /spec/regions/0/code into spec.regions[0].code.
func fieldPath(ptr string) string {
	var b strings.Builder
	for _, seg := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		if seg == "" {
			continue
		}
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		if _, err := strconv.Atoi(seg); err == nil {
			b.WriteString("[" + seg + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteString(".")
		}
		b.WriteString(seg)
	}
	return b.String()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if child, ok := v.(map[string]any); ok {
			out[k] = deepCopy(child)
			continue
		}
		out[k] = v
	}
	return out
}

// mergeInto lays src over dst: maps merge, everything else replaces.
func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		sm, sIsMap := v.(map[string]any)
		dm, dIsMap := dst[k].(map[string]any)
		if sIsMap && dIsMap {
			mergeInto(dm, sm)
			continue
		}
		if sIsMap {
			dst[k] = deepCopy(sm)
			continue
		}
		dst[k] = v
	}
}
