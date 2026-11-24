package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The JSON Schema keywords this validator implements (draft 2020-12, the subset the
// schemas of the policy chart's model files use). A schema that uses any other keyword
// is refused: a keyword the validator does not know would otherwise hold nothing.
var knownKeywords = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true, "$defs": true, "$ref": true,
	"type": true, "enum": true, "const": true,
	"properties": true, "required": true, "additionalProperties": true, "propertyNames": true,
	"items": true, "minItems": true, "maxItems": true, "uniqueItems": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"minimum": true, "maximum": true,
	"if": true, "then": true, "else": true, "not": true, "allOf": true,
}

// validator checks one document against one schema. root is the whole schema, for $ref.
type validator struct {
	root map[string]any
}

// Validate returns one line per place where doc breaks schema, in a fixed order, and an
// error when the schema itself cannot be applied (an unknown keyword, a $ref that does
// not resolve, a keyword of the wrong type).
func Validate(schema map[string]any, doc any) ([]string, error) {
	v := validator{root: schema}
	var out []string
	if err := v.check(schema, doc, "$", &out); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func (v validator) resolve(ref string) (map[string]any, error) {
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) {
		return nil, fmt.Errorf("$ref %q: only #/$defs/<name> is supported", ref)
	}
	defs, _ := v.root["$defs"].(map[string]any)
	s, ok := defs[strings.TrimPrefix(ref, prefix)].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$ref %q does not resolve", ref)
	}
	return s, nil
}

func typeName(x any) string {
	switch t := x.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if t == math.Trunc(t) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", x)
}

func number(s map[string]any, key string) (float64, bool, error) {
	x, ok := s[key]
	if !ok {
		return 0, false, nil
	}
	f, isNum := x.(float64)
	if !isNum {
		return 0, false, fmt.Errorf("schema keyword %s is not a number", key)
	}
	return f, true, nil
}

// check appends to out one line per rule of s that doc breaks at path.
func (v validator) check(s map[string]any, doc any, path string, out *[]string) error {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !knownKeywords[k] {
			return fmt.Errorf("schema keyword %s is not supported by this validator", k)
		}
	}
	fail := func(format string, args ...any) { *out = append(*out, path+": "+fmt.Sprintf(format, args...)) }

	if ref, ok := s["$ref"]; ok {
		r, isStr := ref.(string)
		if !isStr {
			return fmt.Errorf("$ref is not a string")
		}
		target, err := v.resolve(r)
		if err != nil {
			return err
		}
		if err := v.check(target, doc, path, out); err != nil {
			return err
		}
	}
	got := typeName(doc)
	if t, ok := s["type"]; ok {
		want, isStr := t.(string)
		if !isStr {
			return fmt.Errorf("schema keyword type is not a string")
		}
		if got != want && (want != "number" || got != "integer") {
			fail("is %s, want %s", got, want)
			return nil
		}
	}
	if e, ok := s["enum"]; ok {
		list, isList := e.([]any)
		if !isList {
			return fmt.Errorf("schema keyword enum is not a list")
		}
		found := false
		for _, x := range list {
			if equal(x, doc) {
				found = true
			}
		}
		if !found {
			fail("%s is not one of %s", show(doc), show(list))
		}
	}
	if c, ok := s["const"]; ok && !equal(c, doc) {
		fail("%s is not %s", show(doc), show(c))
	}
	if err := v.checkString(s, doc, fail); err != nil {
		return err
	}
	if err := v.checkNumber(s, doc, fail); err != nil {
		return err
	}
	if err := v.checkArray(s, doc, path, out, fail); err != nil {
		return err
	}
	if err := v.checkObject(s, doc, path, out, fail); err != nil {
		return err
	}
	return v.checkCombinators(s, doc, path, out, fail)
}

func (v validator) checkString(s map[string]any, doc any, fail func(string, ...any)) error {
	str, isStr := doc.(string)
	if !isStr {
		return nil
	}
	n := float64(utf8.RuneCountInString(str))
	if lo, ok, err := number(s, "minLength"); err != nil {
		return err
	} else if ok && n < lo {
		fail("is %d characters, want at least %d", int(n), int(lo))
	}
	if hi, ok, err := number(s, "maxLength"); err != nil {
		return err
	} else if ok && n > hi {
		fail("is %d characters, want at most %d", int(n), int(hi))
	}
	if p, ok := s["pattern"]; ok {
		ps, isStr := p.(string)
		if !isStr {
			return fmt.Errorf("schema keyword pattern is not a string")
		}
		re, err := regexp.Compile(ps)
		if err != nil {
			return fmt.Errorf("schema pattern %q: %v", ps, err)
		}
		if !re.MatchString(str) {
			fail("%q does not match %s", str, ps)
		}
	}
	return nil
}

func (v validator) checkNumber(s map[string]any, doc any, fail func(string, ...any)) error {
	f, isNum := doc.(float64)
	if !isNum {
		return nil
	}
	if lo, ok, err := number(s, "minimum"); err != nil {
		return err
	} else if ok && f < lo {
		fail("%v is below the minimum %v", f, lo)
	}
	if hi, ok, err := number(s, "maximum"); err != nil {
		return err
	} else if ok && f > hi {
		fail("%v is above the maximum %v", f, hi)
	}
	return nil
}

func (v validator) checkArray(s map[string]any, doc any, path string, out *[]string, fail func(string, ...any)) error {
	list, isList := doc.([]any)
	if !isList {
		return nil
	}
	n := float64(len(list))
	if lo, ok, err := number(s, "minItems"); err != nil {
		return err
	} else if ok && n < lo {
		fail("has %d items, want at least %d", len(list), int(lo))
	}
	if hi, ok, err := number(s, "maxItems"); err != nil {
		return err
	} else if ok && n > hi {
		fail("has %d items, want at most %d", len(list), int(hi))
	}
	if u, ok := s["uniqueItems"]; ok {
		b, isBool := u.(bool)
		if !isBool {
			return fmt.Errorf("schema keyword uniqueItems is not a boolean")
		}
		for i := 0; b && i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				if equal(list[i], list[j]) {
					fail("items %d and %d are equal", i, j)
				}
			}
		}
	}
	if it, ok := s["items"]; ok {
		item, isMap := it.(map[string]any)
		if !isMap {
			return fmt.Errorf("schema keyword items is not a schema")
		}
		for i, x := range list {
			if err := v.check(item, x, fmt.Sprintf("%s[%d]", path, i), out); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v validator) checkObject(s map[string]any, doc any, path string, out *[]string, fail func(string, ...any)) error {
	obj, isObj := doc.(map[string]any)
	if !isObj {
		return nil
	}
	if r, ok := s["required"]; ok {
		list, isList := r.([]any)
		if !isList {
			return fmt.Errorf("schema keyword required is not a list")
		}
		for _, x := range list {
			name, _ := x.(string)
			if _, has := obj[name]; !has {
				fail("%s is required", name)
			}
		}
	}
	props := map[string]any{}
	if p, ok := s["properties"]; ok {
		m, isMap := p.(map[string]any)
		if !isMap {
			return fmt.Errorf("schema keyword properties is not a map")
		}
		props = m
	}
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if pn, ok := s["propertyNames"]; ok {
			ps, isMap := pn.(map[string]any)
			if !isMap {
				return fmt.Errorf("schema keyword propertyNames is not a schema")
			}
			if err := v.check(ps, k, path+" key "+k, out); err != nil {
				return err
			}
		}
		if ps, ok := props[k]; ok {
			sub, isMap := ps.(map[string]any)
			if !isMap {
				return fmt.Errorf("schema of property %s is not a schema", k)
			}
			if err := v.check(sub, obj[k], path+"."+k, out); err != nil {
				return err
			}
			continue
		}
		switch ap := s["additionalProperties"].(type) {
		case nil:
		case bool:
			if !ap {
				fail("%s is not an allowed key", k)
			}
		case map[string]any:
			if err := v.check(ap, obj[k], path+"."+k, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("schema keyword additionalProperties is neither a boolean nor a schema")
		}
	}
	return nil
}

func (v validator) holds(s any, doc any, path string) (bool, error) {
	m, isMap := s.(map[string]any)
	if !isMap {
		return false, fmt.Errorf("a schema of if, then, else, not or allOf is not a schema")
	}
	var probe []string
	if err := v.check(m, doc, path, &probe); err != nil {
		return false, err
	}
	return len(probe) == 0, nil
}

func (v validator) checkCombinators(s map[string]any, doc any, path string, out *[]string, fail func(string, ...any)) error {
	if all, ok := s["allOf"]; ok {
		list, isList := all.([]any)
		if !isList {
			return fmt.Errorf("schema keyword allOf is not a list")
		}
		for _, sub := range list {
			m, isMap := sub.(map[string]any)
			if !isMap {
				return fmt.Errorf("an item of allOf is not a schema")
			}
			if err := v.check(m, doc, path, out); err != nil {
				return err
			}
		}
	}
	if n, ok := s["not"]; ok {
		held, err := v.holds(n, doc, path)
		if err != nil {
			return err
		}
		if held {
			fail("matches a schema it must not match (%s)", show(n))
		}
	}
	cond, ok := s["if"]
	if !ok {
		return nil
	}
	held, err := v.holds(cond, doc, path)
	if err != nil {
		return err
	}
	branch := "else"
	if held {
		branch = "then"
	}
	if b, ok := s[branch]; ok {
		m, isMap := b.(map[string]any)
		if !isMap {
			return fmt.Errorf("schema keyword %s is not a schema", branch)
		}
		return v.check(m, doc, path, out)
	}
	return nil
}

func equal(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func show(x any) string {
	b, _ := json.Marshal(x)
	return string(b)
}
