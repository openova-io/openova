// Package profile loads and checks a standards profile of the open-banking product.
//
// A profile is one folder under products/fingate/profiles/<id>/. It holds profile.yaml and
// the OpenAPI documents that profile.yaml names, vendored from the publisher unchanged.
// Load reads profile.yaml, rejects an unknown or a missing field, checks every value,
// checks each document against its SHA-256, counts the operations of each document and
// asserts that served plus called plus the operations under a notOffered prefix equals
// the operation count of the document.
package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Schema is the only value of the field schema.
const Schema = "openova.io/fingate-profile/v1"

// FileName is the name of the profile file inside a profile folder.
const FileName = "profile.yaml"

// SignatureHeader is the request header of a detached signature.
const SignatureHeader = "x-jws-signature"

// Profile is the content of profile.yaml, with the operations read from its documents.
type Profile struct {
	Schema               string                       `yaml:"schema"`
	ID                   string                       `yaml:"id"`
	BasePath             string                       `yaml:"basePath"`
	NotImplementedStatus int                          `yaml:"notImplementedStatus"`
	Documents            []Document                   `yaml:"documents"`
	NotOffered           []string                     `yaml:"notOffered"`
	Security             Security                     `yaml:"security"`
	ConsentStatus        map[string]map[string]string `yaml:"consentStatus"`
	IdempotencyHours     int                          `yaml:"idempotencyHours"`
}

// Document is one OpenAPI document of a profile.
type Document struct {
	API        string `yaml:"api"`
	File       string `yaml:"file"`
	SHA256     string `yaml:"sha256"`
	Operations int    `yaml:"operations"`
	Served     int    `yaml:"served"`
	Called     int    `yaml:"called"`

	// Ops holds the operations of the document, sorted by path and method. Load fills it.
	Ops []Operation `yaml:"-"`
}

// Operation is one operation of a document.
type Operation struct {
	Method string // upper case, for example POST
	Path   string // the key of the document's paths object
	Signed bool   // the operation declares the header x-jws-signature as required
}

// Security holds the security rules of a profile.
type Security struct {
	FAPI               string   `yaml:"fapi"`
	ClientAuth         []string `yaml:"clientAuth"`
	SigningAlgorithms  []string `yaml:"signingAlgorithms"`
	ConsentCarrier     string   `yaml:"consentCarrier"`
	AccessTokenSeconds int      `yaml:"accessTokenSeconds"`
}

var (
	hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	wireCode  = regexp.MustCompile(`^[A-Z]{4}$`)

	methods = map[string]bool{"get": true, "put": true, "post": true, "delete": true,
		"options": true, "head": true, "patch": true, "trace": true}
	clientAuths     = map[string]bool{"tls_client_auth": true, "private_key_jwt": true}
	algorithms      = map[string]bool{"PS256": true}
	consentCarriers = map[string]bool{"intent-claim": true}
	families        = []string{"account-access", "funds-confirmation", "payment", "vrp"}
	states          = map[string]bool{"awaiting": true, "authorised": true, "rejected": true,
		"revoked": true, "expired": true, "consumed": true}
)

// LoadDir loads the profile in the folder dir. The folder name is the profile identifier.
func LoadDir(dir string) (*Profile, error) {
	return Load(os.DirFS(dir), filepath.Base(filepath.Clean(dir)))
}

// Load loads the profile whose folder is fsys and whose identifier is id.
func Load(fsys fs.FS, id string) (*Profile, error) {
	raw, err := fs.ReadFile(fsys, FileName)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if err := required(&node); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	var p Profile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if err := p.check(id); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	for i := range p.Documents {
		if err := p.readDocument(fsys, &p.Documents[i]); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

// NotOfferedPath reports whether a document path lies under a notOffered prefix: it equals
// the prefix, or it starts with the prefix followed by "/".
func (p *Profile) NotOfferedPath(docPath string) bool {
	for _, prefix := range p.NotOffered {
		if docPath == prefix || strings.HasPrefix(docPath, prefix+"/") {
			return true
		}
	}
	return false
}

// SignedCount is the number of operations of the document that declare the signature header.
func (d *Document) SignedCount() int {
	n := 0
	for _, op := range d.Ops {
		if op.Signed {
			n++
		}
	}
	return n
}

// required reports the first required key that profile.yaml leaves out.
func required(doc *yaml.Node) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("the file is not one YAML mapping")
	}
	top := doc.Content[0]
	if err := keys(top, "", "schema", "id", "basePath", "notImplementedStatus", "documents",
		"notOffered", "security", "consentStatus", "idempotencyHours"); err != nil {
		return err
	}
	if sec := value(top, "security"); sec != nil {
		if err := keys(sec, "security.", "fapi", "clientAuth", "signingAlgorithms",
			"consentCarrier", "accessTokenSeconds"); err != nil {
			return err
		}
	}
	if docs := value(top, "documents"); docs != nil && docs.Kind == yaml.SequenceNode {
		for i, d := range docs.Content {
			if err := keys(d, fmt.Sprintf("documents[%d].", i), "api", "file", "sha256",
				"operations", "served"); err != nil {
				return err
			}
		}
	}
	return nil
}

func keys(m *yaml.Node, where string, names ...string) error {
	if m.Kind != yaml.MappingNode {
		return fmt.Errorf("%s is not a mapping", strings.TrimSuffix(where, "."))
	}
	for _, n := range names {
		if value(m, n) == nil {
			return fmt.Errorf("field %s%s is missing", where, n)
		}
	}
	return nil
}

func value(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// check applies every rule that needs no document. It returns all broken rules, one per line.
func (p *Profile) check(id string) error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if p.Schema != Schema {
		bad("schema is %q, want %q", p.Schema, Schema)
	}
	if p.ID == "" || p.ID != id {
		bad("id is %q, want the folder name %q", p.ID, id)
	}
	if !strings.HasPrefix(p.BasePath, "/") || strings.HasSuffix(p.BasePath, "/") {
		bad("basePath %q must start with / and must not end with /", p.BasePath)
	}
	if p.NotImplementedStatus < 400 || p.NotImplementedStatus > 499 {
		bad("notImplementedStatus %d is not a status from 400 to 499", p.NotImplementedStatus)
	}
	if len(p.Documents) == 0 {
		bad("documents is empty")
	}
	apis, files := map[string]bool{}, map[string]bool{}
	for i, d := range p.Documents {
		if d.API == "" || apis[d.API] {
			bad("documents[%d]: api %q is empty or not unique", i, d.API)
		}
		apis[d.API] = true
		if d.File == "" || files[d.File] || path.Base(d.File) != d.File || !fs.ValidPath(d.File) {
			bad("documents[%d]: file %q is empty, not unique, or not a plain file name", i, d.File)
		}
		files[d.File] = true
		if !hexSHA256.MatchString(d.SHA256) {
			bad("documents[%d]: sha256 %q is not 64 lower-case hex characters", i, d.SHA256)
		}
		if d.Operations < 1 || d.Served < 0 || d.Called < 0 || d.Served+d.Called > d.Operations {
			bad("documents[%d]: operations %d, served %d, called %d do not fit", i, d.Operations, d.Served, d.Called)
		}
	}
	seen := map[string]bool{}
	for _, prefix := range p.NotOffered {
		if !strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || seen[prefix] {
			bad("notOffered %q must start with /, must not end with / and must be unique", prefix)
		}
		seen[prefix] = true
	}
	s := p.Security
	if s.FAPI != "fapi1-advanced-final" {
		bad("security.fapi %q is not fapi1-advanced-final", s.FAPI)
	}
	if !members(s.ClientAuth, clientAuths) {
		bad("security.clientAuth %q must be a non-empty list of different values, each tls_client_auth or private_key_jwt", s.ClientAuth)
	}
	if !members(s.SigningAlgorithms, algorithms) {
		bad("security.signingAlgorithms %q must be a non-empty list of different values, each PS256", s.SigningAlgorithms)
	}
	if !consentCarriers[s.ConsentCarrier] {
		bad("security.consentCarrier %q is not intent-claim", s.ConsentCarrier)
	}
	if s.AccessTokenSeconds < 60 || s.AccessTokenSeconds > 600 {
		bad("security.accessTokenSeconds %d is not from 60 to 600", s.AccessTokenSeconds)
	}
	got := make([]string, 0, len(p.ConsentStatus))
	for f := range p.ConsentStatus {
		got = append(got, f)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(families, ",") {
		bad("consentStatus has the families %q, want exactly %q", got, families)
	}
	for _, f := range got {
		codes := p.ConsentStatus[f]
		for _, st := range []string{"awaiting", "authorised", "rejected"} {
			if _, ok := codes[st]; !ok {
				bad("consentStatus.%s has no code for the state %s", f, st)
			}
		}
		for st, code := range codes {
			if !states[st] {
				bad("consentStatus.%s: %q is not a consent state", f, st)
			}
			if st == "consumed" && f != "payment" {
				bad("consentStatus.%s: only the payment family has the state consumed", f)
			}
			if !wireCode.MatchString(code) {
				bad("consentStatus.%s.%s: %q is not four capital letters", f, st, code)
			}
		}
	}
	if p.IdempotencyHours < 1 {
		bad("idempotencyHours %d is below 1", p.IdempotencyHours)
	}
	return errors.Join(errs...)
}

func members(list []string, allowed map[string]bool) bool {
	if len(list) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range list {
		if !allowed[v] || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// openAPI is the part of an OpenAPI 3.0 document that Load reads.
type openAPI struct {
	OpenAPI    string                          `yaml:"openapi"`
	Paths      map[string]map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Parameters map[string]parameter `yaml:"parameters"`
	} `yaml:"components"`
}

type parameter struct {
	Ref      string `yaml:"$ref"`
	In       string `yaml:"in"`
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
}

type operation struct {
	Parameters []parameter `yaml:"parameters"`
}

// readDocument checks the SHA-256 of one document, reads its operations and checks the counts.
func (p *Profile) readDocument(fsys fs.FS, d *Document) error {
	where := "document " + d.File
	raw, err := fs.ReadFile(fsys, d.File)
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != d.SHA256 {
		return fmt.Errorf("%s: SHA-256 is %s, the profile says %s", where, got, d.SHA256)
	}
	var doc openAPI
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.0.") {
		return fmt.Errorf("%s: openapi is %q, want a version 3.0", where, doc.OpenAPI)
	}
	resolve := func(q parameter) (parameter, error) {
		if q.Ref == "" {
			return q, nil
		}
		name, ok := strings.CutPrefix(q.Ref, "#/components/parameters/")
		target, found := doc.Components.Parameters[name]
		if !ok || !found || target.Ref != "" {
			return q, fmt.Errorf("%s: parameter reference %q is not a parameter of the document", where, q.Ref)
		}
		return target, nil
	}
	d.Ops = nil
	for docPath, item := range doc.Paths {
		if _, ok := item["$ref"]; ok {
			return fmt.Errorf("%s: path %s is a reference, which this loader does not read", where, docPath)
		}
		var shared []parameter
		if n, ok := item["parameters"]; ok {
			if err := n.Decode(&shared); err != nil {
				return fmt.Errorf("%s: path %s: %w", where, docPath, err)
			}
		}
		for key, n := range item {
			if !methods[key] {
				continue
			}
			var op operation
			if err := n.Decode(&op); err != nil {
				return fmt.Errorf("%s: %s %s: %w", where, key, docPath, err)
			}
			signed := false
			for _, q := range append(append([]parameter{}, shared...), op.Parameters...) {
				r, err := resolve(q)
				if err != nil {
					return err
				}
				if r.In == "header" && strings.EqualFold(r.Name, SignatureHeader) && r.Required {
					signed = true
				}
			}
			d.Ops = append(d.Ops, Operation{Method: strings.ToUpper(key), Path: docPath, Signed: signed})
		}
	}
	sort.Slice(d.Ops, func(i, j int) bool {
		if d.Ops[i].Path != d.Ops[j].Path {
			return d.Ops[i].Path < d.Ops[j].Path
		}
		return d.Ops[i].Method < d.Ops[j].Method
	})
	if len(d.Ops) != d.Operations {
		return fmt.Errorf("%s: it has %d operations, the profile says %d", where, len(d.Ops), d.Operations)
	}
	notOffered := 0
	for _, op := range d.Ops {
		if p.NotOfferedPath(op.Path) {
			notOffered++
		}
	}
	if d.Served+d.Called+notOffered != d.Operations {
		return fmt.Errorf("%s: served %d plus called %d plus not offered %d is not the %d operations",
			where, d.Served, d.Called, notOffered, d.Operations)
	}
	return nil
}
