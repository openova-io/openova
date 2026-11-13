package intent

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	openapierrors "k8s.io/kube-openapi/pkg/validation/errors"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// schemaBundle holds the openAPIV3Schema of every version of every
// definition in products/catalyst/chart/crds/*.yaml and
// platform/crossplane-claims/chart/templates/crds/useraccess.yaml, keyed
// "<group>/<version>/<Kind>". Go cannot embed a file outside its module, so
// the bundle is generated from those files; TestSchemasMatchSources fails
// when it differs from them and rewrites it when INTENT_WRITE_SCHEMAS=1.
//
//go:embed crds/schemas.json
var schemaBundle []byte

var (
	schemasOnce sync.Once
	schemas     map[string]*spec.Schema
	schemasErr  error
)

func loadSchemas() (map[string]*spec.Schema, error) {
	schemasOnce.Do(func() {
		raw := map[string]json.RawMessage{}
		if err := json.Unmarshal(schemaBundle, &raw); err != nil {
			schemasErr = fmt.Errorf("intent: read the schema bundle: %w", err)
			return
		}
		out := make(map[string]*spec.Schema, len(raw))
		for key, r := range raw {
			s := &spec.Schema{}
			if err := json.Unmarshal(r, s); err != nil {
				schemasErr = fmt.Errorf("intent: read the schema %s: %w", key, err)
				return
			}
			out[key] = s
		}
		schemas = out
	})
	return schemas, schemasErr
}

// Validate checks obj against the embedded schema of its apiVersion and
// kind. It returns nil or a list of field errors, sorted by field. An
// object needs apiVersion, kind and metadata.name. A kind of a group that
// ends in "openova.io" with no embedded schema is an error; any other kind
// with no embedded schema is not checked further (the API server owns it).
func Validate(obj *unstructured.Unstructured) field.ErrorList {
	if obj == nil {
		return field.ErrorList{field.Required(field.NewPath("object"), "")}
	}
	var errs field.ErrorList
	apiVersion, kind := obj.GetAPIVersion(), obj.GetKind()
	if apiVersion == "" {
		errs = append(errs, field.Required(field.NewPath("apiVersion"), ""))
	}
	if kind == "" {
		errs = append(errs, field.Required(field.NewPath("kind"), ""))
	}
	if obj.GetName() == "" {
		errs = append(errs, field.Required(field.NewPath("metadata", "name"), ""))
	}
	if len(errs) > 0 {
		return errs
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return field.ErrorList{field.Invalid(field.NewPath("apiVersion"), apiVersion, err.Error())}
	}
	all, err := loadSchemas()
	if err != nil {
		return field.ErrorList{field.InternalError(field.NewPath("apiVersion"), err)}
	}
	s, ok := all[gv.Group+"/"+gv.Version+"/"+kind]
	if !ok {
		if strings.HasSuffix(gv.Group, "openova.io") {
			return field.ErrorList{field.Invalid(field.NewPath("apiVersion"), apiVersion, "no schema is embedded for kind "+kind)}
		}
		return nil
	}
	res := validate.NewSchemaValidator(s, nil, "", strfmt.Default).Validate(obj.Object)
	for _, e := range res.Errors {
		errs = append(errs, toFieldError(e))
	}
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Field != errs[j].Field {
			return errs[i].Field < errs[j].Field
		}
		return errs[i].Detail < errs[j].Detail
	})
	return errs
}

// toFieldError turns one error of the schema validator into a field error.
func toFieldError(e error) *field.Error {
	v, ok := e.(*openapierrors.Validation)
	if !ok {
		return field.Invalid(field.NewPath("<object>"), nil, e.Error())
	}
	name := strings.TrimPrefix(v.Name, ".")
	if name == "" {
		name = "<object>"
	}
	p := field.NewPath(name)
	if v.Code() == openapierrors.RequiredFailCode {
		return field.Required(p, v.Error())
	}
	return field.Invalid(p, v.Value, v.Error())
}
