package suite

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Cluster classes (ADR-0037). A class file <name>.yaml declares what a cluster
// of its class can do: one boolean per capability flag. A class that names no
// provider lives in platform/cluster-classes/; the class of one provider lives
// in its pack, infra/providers/<provider>/cluster-classes/ (ClassFiles). Check
// NET-G17 (check_cluster_class.go) reads the class of the regions of the run
// with ReadClass and probes every flag that the class declares true.
//
// The suite is a module of the standard library only, so ReadClass reads the
// subset of YAML that a class file may use and refuses every other line, so
// that what it reads is what any YAML reader reads: "key: value" at the left
// margin; one map, "capabilities:", whose lines "flag: value" are indented by
// exactly two spaces; blank lines; and lines whose first character other than a
// space is #. A value is true or false, or text that starts with a letter, is
// not a word that YAML 1.1 reads as a boolean or as null, and holds neither
// ": " nor " #". A key is given once.
//
// The schema platform/cluster-classes/schema.json then judges the document.
// ReadClassSchema applies the keywords type, required, properties,
// additionalProperties (false only), enum, const and pattern, and the
// annotations $schema, $id, title and description; a schema that uses any
// other keyword is refused, so the schema can never hold a rule that the
// reader does not apply.
const (
	classDir      = "platform/cluster-classes"
	classPackGlob = "infra/providers/*/cluster-classes/*.yaml"
	classSchema   = "platform/cluster-classes/schema.json"
	classMap      = "capabilities" // the one key whose value is a map
)

// Class is one class file.
type Class struct {
	Name, Kind, Provider, Distribution, Notes string
	MayHostControlPlane                       bool
	Capabilities                              map[string]bool
}

// ClassSchema is the schema of the class files. Flags are the capability
// flags in the order of the schema's required list of capabilities.
type ClassSchema struct {
	root  *schemaNode
	Flags []string
}

type schemaNode struct {
	Type                 string                 `json:"type"`
	Required             []string               `json:"required"`
	Properties           map[string]*schemaNode `json:"properties"`
	AdditionalProperties *bool                  `json:"additionalProperties"`
	Enum                 []any                  `json:"enum"`
	Const                any                    `json:"const"`
	Pattern              string                 `json:"pattern"`
	pattern              *regexp.Regexp
}

// schemaKeywords are the keywords that ReadClassSchema accepts.
var schemaKeywords = []string{"$schema", "$id", "title", "description", "type", "required", "properties",
	"additionalProperties", "enum", "const", "pattern"}

// ReadClassSchema reads the schema of the class files from the checkout at root.
func ReadClassSchema(root string) (*ClassSchema, error) {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, filepath.FromSlash(classSchema))))
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s is not JSON: %v", classSchema, err)
	}
	if err := checkSchemaKeywords("", raw); err != nil {
		return nil, fmt.Errorf("%s: %v", classSchema, err)
	}
	var root0 schemaNode
	if err := json.Unmarshal(data, &root0); err != nil {
		return nil, fmt.Errorf("%s: %v", classSchema, err)
	}
	if err := root0.compile(""); err != nil {
		return nil, fmt.Errorf("%s: %v", classSchema, err)
	}
	caps := root0.Properties[classMap]
	if root0.Type != "object" || caps == nil || caps.Type != "object" || len(caps.Required) == 0 {
		return nil, fmt.Errorf("%s: the root and its property %s must be objects, and %s must require its flags", classSchema, classMap, classMap)
	}
	listed := make([]string, 0, len(caps.Properties))
	for k := range caps.Properties {
		listed = append(listed, k)
	}
	want := slices.Clone(caps.Required)
	sort.Strings(listed)
	sort.Strings(want)
	if !slices.Equal(listed, want) {
		return nil, fmt.Errorf("%s: the flags that %s requires (%v) are not exactly the flags it describes (%v)", classSchema, classMap, caps.Required, listed)
	}
	return &ClassSchema{root: &root0, Flags: slices.Clone(caps.Required)}, nil
}

// checkSchemaKeywords refuses a keyword that the reader does not apply.
func checkSchemaKeywords(at string, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%s is not an object", schemaPath(at))
	}
	for k, sub := range m {
		if !slices.Contains(schemaKeywords, k) {
			return fmt.Errorf("%s uses the keyword %q, which the reader does not apply", schemaPath(at), k)
		}
		if k != "properties" {
			continue
		}
		props, ok := sub.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.properties is not an object", schemaPath(at))
		}
		for name, p := range props {
			if err := checkSchemaKeywords(at+"."+name, p); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaPath(at string) string {
	if at == "" {
		return "the root"
	}
	return strings.TrimPrefix(at, ".")
}

func (n *schemaNode) compile(at string) error {
	if n.AdditionalProperties != nil && *n.AdditionalProperties {
		return fmt.Errorf("%s: additionalProperties may only be false", schemaPath(at))
	}
	if n.Pattern != "" {
		re, err := regexp.Compile(n.Pattern)
		if err != nil {
			return fmt.Errorf("%s: pattern: %v", schemaPath(at), err)
		}
		n.pattern = re
	}
	for k, p := range n.Properties {
		if p == nil {
			return fmt.Errorf("%s.%s: no schema", schemaPath(at), k)
		}
		if err := p.compile(at + "." + k); err != nil {
			return err
		}
	}
	return nil
}

// validate returns one line per rule of n that v breaks, in a stable order.
func (n *schemaNode) validate(at string, v any) []string {
	out := []string{}
	switch n.Type {
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return []string{schemaPath(at) + ": want a map"}
		}
	case "string":
		if _, ok := v.(string); !ok {
			return []string{fmt.Sprintf("%s: want text, got %v", schemaPath(at), v)}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return []string{fmt.Sprintf("%s: want true or false, got %v", schemaPath(at), v)}
		}
	case "":
	default:
		return []string{fmt.Sprintf("%s: the schema's type %q is not applied by the reader", schemaPath(at), n.Type)}
	}
	same := func(e any) bool { return reflect.DeepEqual(e, v) } // == panics on two maps
	if n.Const != nil && !same(n.Const) {
		out = append(out, fmt.Sprintf("%s: must be %v, got %v", schemaPath(at), n.Const, v))
	}
	if len(n.Enum) > 0 && !slices.ContainsFunc(n.Enum, same) {
		out = append(out, fmt.Sprintf("%s: %v is not one of %v", schemaPath(at), v, n.Enum))
	}
	if s, ok := v.(string); ok && n.pattern != nil && !n.pattern.MatchString(s) {
		out = append(out, fmt.Sprintf("%s: %q does not match %s", schemaPath(at), s, n.Pattern))
	}
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	for _, k := range n.Required {
		if _, has := m[k]; !has {
			out = append(out, fmt.Sprintf("%s: %s is missing", schemaPath(at), k))
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p, known := n.Properties[k]
		switch {
		case known:
			out = append(out, p.validate(at+"."+k, m[k])...)
		case n.AdditionalProperties != nil:
			out = append(out, fmt.Sprintf("%s: %s is not a field of the schema", schemaPath(at), k))
		}
	}
	return out
}

var (
	classKeyRE  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.]*$`)
	classTextRE = regexp.MustCompile(`^[A-Za-z]`)
	// classYAMLWords are the words YAML 1.1 reads as a boolean or as null.
	classYAMLWords = []string{"y", "n", "yes", "no", "on", "off", "true", "false", "null"}
)

// parseClassYAML reads the YAML subset of a class file.
func parseClassYAML(data []byte) (map[string]any, error) {
	doc := map[string]any{}
	var inMap map[string]any
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(trimmed)
		if indent != 0 && (indent != 2 || inMap == nil) {
			return nil, fmt.Errorf("line %d: indented by %d spaces; only the lines of %s are indented, by 2", i+1, indent, classMap)
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found || !classKeyRE.MatchString(key) || (value != "" && !strings.HasPrefix(value, " ")) {
			return nil, fmt.Errorf("line %d: not a line \"key: value\": %q", i+1, line)
		}
		value = strings.TrimPrefix(value, " ")
		target := doc
		if indent == 2 {
			target = inMap
		} else {
			inMap = nil
		}
		if _, twice := target[key]; twice {
			return nil, fmt.Errorf("line %d: %s is given twice", i+1, key)
		}
		if value == "" {
			if indent != 0 || key != classMap {
				return nil, fmt.Errorf("line %d: %s has no value; only %s is a map", i+1, key, classMap)
			}
			inMap = map[string]any{}
			doc[key] = inMap
			continue
		}
		v, err := classScalar(value)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %v", i+1, key, err)
		}
		target[key] = v
	}
	return doc, nil
}

// classScalar is the value of one scalar of the subset.
func classScalar(s string) (any, error) {
	switch {
	case s == "true":
		return true, nil
	case s == "false":
		return false, nil
	case strings.TrimSpace(s) != s:
		return nil, fmt.Errorf("the value %q starts or ends with a space", s)
	case !classTextRE.MatchString(s) || slices.Contains(classYAMLWords, strings.ToLower(s)):
		return nil, fmt.Errorf("the value %q is neither true, false nor text that starts with a letter", s)
	case strings.Contains(s, ": ") || strings.Contains(s, " #"):
		return nil, fmt.Errorf("the value %q holds \": \" or \" #\", which YAML reads otherwise", s)
	}
	return s, nil
}

// ReadClass reads the class file of name from the checkout at root and judges
// it by the schema. A file whose name field is not its file name, or whose
// provider is not the provider of its folder, is refused.
func ReadClass(root string, schema *ClassSchema, name string) (*Class, error) {
	files, err := ClassFiles(root)
	if err != nil {
		return nil, err
	}
	file, ok := files[name]
	if !ok {
		return nil, fmt.Errorf("no class file %s.yaml in %s or %s", name, classDir, classPackGlob)
	}
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, filepath.FromSlash(file))))
	if err != nil {
		return nil, err
	}
	doc, err := parseClassYAML(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", file, err)
	}
	if bad := schema.root.validate("", doc); len(bad) > 0 {
		return nil, fmt.Errorf("%s breaks %s: %s", file, classSchema, strings.Join(bad, "; "))
	}
	c := &Class{Capabilities: map[string]bool{}}
	c.Name, _ = doc["name"].(string)
	c.Kind, _ = doc["kind"].(string)
	c.Provider, _ = doc["provider"].(string)
	c.Distribution, _ = doc["distribution"].(string)
	c.Notes, _ = doc["notes"].(string)
	c.MayHostControlPlane, _ = doc["mayHostControlPlane"].(bool)
	caps, ok := doc[classMap].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: %s is not a map of flags", file, classMap)
	}
	for k, v := range caps {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s: %s.%s is %v, want true or false", file, classMap, k, v)
		}
		c.Capabilities[k] = b
	}
	if c.Name != name {
		return nil, fmt.Errorf("%s: its name is %q, want the file name %q", file, c.Name, name)
	}
	if want := classFolderProvider(file); c.Provider != want {
		return nil, fmt.Errorf("%s: its provider is %q, want %q, the provider of its folder", file, c.Provider, want)
	}
	return c, nil
}

// classFolderProvider is the provider that a class file of the folder of file
// must declare: any in classDir, else the provider pack the folder belongs to.
func classFolderProvider(file string) string {
	if strings.HasPrefix(file, classDir+"/") {
		return "any"
	}
	return strings.Split(file, "/")[2] // infra/providers/<provider>/cluster-classes/<name>.yaml
}

// ClassFiles maps the name of every class file of the checkout at root to its
// path. A class that names no provider lives in classDir; the class of one
// provider lives in that provider's pack (classPackGlob), since a file outside
// the plug-in paths names no provider. A name used twice is refused.
func ClassFiles(root string) (map[string]string, error) {
	out := map[string]string{}
	for _, pattern := range []string{classDir + "/*.yaml", classPackGlob} {
		found, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			rel, err := filepath.Rel(root, f)
			if err != nil {
				return nil, err
			}
			rel = filepath.ToSlash(rel)
			name := strings.TrimSuffix(path.Base(rel), ".yaml")
			if first, twice := out[name]; twice {
				return nil, fmt.Errorf("the class %s has two files, %s and %s", name, first, rel)
			}
			out[name] = rel
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no class file in " + classDir + " or " + classPackGlob)
	}
	return out, nil
}

// ClassNames are the names of the class files of the checkout at root, sorted.
func ClassNames(root string) ([]string, error) {
	files, err := ClassFiles(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for name := range files {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}
