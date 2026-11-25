// Package blueprintedit decides whether a catalogue write may change a
// Blueprint. Every field outside the card (version, class, source, topology,
// labels...) has one writer: the Blueprint's own source. Check refuses a write
// that changes another path of the live object than Editable, then validates
// the result against blueprint.schema.json (contract C1), embedded as a copy
// of platform/_schemas/blueprint.schema.json that a test keeps equal.
package blueprintedit

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
	yamlv3 "gopkg.in/yaml.v3"
)

//go:embed blueprint.schema.json
var schemaJSON []byte

// Editable lists the paths a catalogue edit may change (iconLight and
// iconDark are the two variants of the card icon).
var Editable = []string{"spec.card.title", "spec.card.tagline", "spec.card.summary", "spec.card.description",
	"spec.card.icon", "spec.card.iconLight", "spec.card.iconDark", "spec.visibility"}

// ContractFieldError refuses a write that changes a path outside Editable.
type ContractFieldError struct{ Paths []string }

func (e *ContractFieldError) Error() string {
	return "contract-field-read-only: the write changes " + strings.Join(e.Paths, ", ") +
		"; a catalogue edit may change only the card fields and spec.visibility"
}

// SchemaError refuses a result that blueprint.schema.json does not accept.
type SchemaError struct{ Detail string }

func (e *SchemaError) Error() string { return "blueprint-schema-invalid: " + e.Detail }

// Check returns nil when proposed may be committed over live, both Blueprint
// YAML in the committed form. An empty live is no live object: then only the
// schema is checked. A live that is not empty and does not read as a mapping
// is an error that is neither refusal type, so Answer reports it as 502.
func Check(live, proposed []byte) error {
	var l map[string]interface{}
	if len(bytes.TrimSpace(live)) > 0 {
		var err error
		if l, err = decode(live); err != nil {
			return errors.New("the live Blueprint cannot be read: " + err.Error())
		}
	}
	p, err := decode(proposed)
	if err != nil {
		return &SchemaError{Detail: err.Error()}
	}
	if l != nil {
		var refused []string
		if diff(nil, l, p, &refused); len(refused) > 0 {
			sort.Strings(refused)
			return &ContractFieldError{Paths: refused}
		}
	}
	return validate(p)
}

// Answer writes the HTTP answer for the error of a write check and reports
// whether it wrote: 422 for a refusal of Check, 502 gitea-upstream for any
// other error (the live object could not be read), nothing for nil.
func Answer(w http.ResponseWriter, err error) bool {
	var cf *ContractFieldError
	var se *SchemaError
	code, body := http.StatusUnprocessableEntity, map[string]interface{}{"error": "gitea-upstream"}
	switch {
	case err == nil:
		return false
	case errors.As(err, &cf):
		body = map[string]interface{}{"error": "contract-field-read-only", "detail": cf.Error(), "paths": cf.Paths}
	case errors.As(err, &se):
		body["error"], body["detail"] = "blueprint-schema-invalid", se.Detail
	default:
		code, body["detail"] = http.StatusBadGateway, err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
	return true
}

// decode reads one YAML mapping as the JSON value tree that both sides of a
// comparison and the schema validator read.
func decode(raw []byte) (map[string]interface{}, error) {
	var y interface{}
	if err := yamlv3.Unmarshal(raw, &y); err != nil {
		return nil, err
	}
	js, err := json.Marshal(y)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errors.New("the document is not a mapping")
	}
	return m, nil
}

// diff appends each path outside Editable whose value differs between a and
// b; a map absent on one side compares as an empty map. A path is compared
// key by key, so a key that holds a dot ("card.title" under spec) is never
// taken for an editable path.
func diff(path []string, a, b interface{}, out *[]string) {
	am, aok := a.(map[string]interface{})
	bm, bok := b.(map[string]interface{})
	if (aok || bok) && (aok || a == nil) && (bok || b == nil) {
		for k, v := range am {
			diff(append(path[:len(path):len(path)], k), v, bm[k], out)
		}
		for k, v := range bm {
			if _, seen := am[k]; !seen {
				diff(append(path[:len(path):len(path)], k), nil, v, out)
			}
		}
		return
	}
	for _, e := range Editable {
		if within(path, strings.Split(e, ".")) {
			return
		}
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, strings.Join(path, "."))
	}
}

// within reports whether path is the editable path e or lies below it.
func within(path, e []string) bool {
	if len(path) < len(e) {
		return false
	}
	for i := range e {
		if path[i] != e[i] {
			return false
		}
	}
	return true
}

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

// validate checks doc against the embedded schema; the detail lists every
// failing leaf, sorted.
func validate(doc map[string]interface{}) error {
	schemaOnce.Do(func() {
		c := jsonschema.NewCompiler()
		c.Draft = jsonschema.Draft2020
		if schemaErr = c.AddResource("file:///blueprint.schema.json", bytes.NewReader(schemaJSON)); schemaErr == nil {
			schema, schemaErr = c.Compile("file:///blueprint.schema.json")
		}
	})
	var ve *jsonschema.ValidationError
	if schemaErr != nil {
		return &SchemaError{Detail: "the embedded schema does not compile: " + schemaErr.Error()}
	} else if err := schema.Validate(doc); !errors.As(err, &ve) {
		return err
	}
	var leaves []string
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, "at '"+e.InstanceLocation+"': "+e.Message)
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	sort.Strings(leaves)
	return &SchemaError{Detail: strings.Join(leaves, "; ")}
}
