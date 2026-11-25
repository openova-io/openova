package blueprintedit

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const live = `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-redis
  labels:
    catalyst.openova.io/managed-by: catalog-seed
spec:
  version: 1.2.3
  class: data-service
  lifecycle: supported
  visibility: listed
  topology:
    supported: [singleton]
  card:
    title: Redis
`

// TestSchemaCopyMatchesSource fails when the embedded copy differs from
// contract C1: copy platform/_schemas/blueprint.schema.json over it.
func TestSchemaCopyMatchesSource(t *testing.T) {
	src, err := os.ReadFile("../../../../../../platform/_schemas/blueprint.schema.json")
	if err != nil || !bytes.Equal(src, schemaJSON) {
		t.Fatalf("internal/blueprintedit/blueprint.schema.json must equal platform/_schemas/blueprint.schema.json (read error: %v)", err)
	}
}

func TestCheck(t *testing.T) {
	for _, c := range []struct{ name, live, old, new, want string }{
		{"title", live, "title: Redis", "title: Redis Cache", ""},
		{"visibility", live, "visibility: listed", "visibility: unlisted", ""},
		{"card fields", live, "    title: Redis\n", "    title: R\n    tagline: t\n    summary: s\n    description: d\n    icon: i\n    iconLight: l\n    iconDark: d\n", ""},
		{"version", live, "version: 1.2.3", "version: 1.2.4", "contract-field-read-only: the write changes spec.version;"},
		{"class", live, "class: data-service", "class: application", "changes spec.class;"},
		{"topology", live, "[singleton]", "[singleton, active-active]", "changes spec.topology.supported;"},
		{"label", live, "catalog-seed", "console", "changes metadata.labels.catalyst.openova.io/managed-by;"},
		{"added and removed", live, "  lifecycle: supported\n", "  shareable: false\n", "changes spec.lifecycle, spec.shareable;"},
		{"no live object", "", "title: Redis", "title: Redis", ""},
		{"no live object, no class", "", "  class: data-service\n", "", "blueprint-schema-invalid: at '/spec': missing properties: 'class'"},
		{"invalid visibility", live, "visibility: listed", "visibility: everyone", "blueprint-schema-invalid: at '/spec/visibility'"},
		{"unreadable live object", "a: [", "title: Redis", "title: Redis", "the live Blueprint cannot be read: "},
		{"live object not a mapping", "- a\n", "title: Redis", "title: Redis", "the live Blueprint cannot be read: "},
		{"dotted key is not a card path", live, "    title: Redis\n", "    title: Redis\n  card.title: X\n", "changes spec.card.title;"},
	} {
		if !strings.Contains(live, c.old) {
			t.Fatalf("%s: fixture has no %q", c.name, c.old)
		}
		err := Check([]byte(c.live), []byte(strings.Replace(live, c.old, c.new, 1)))
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

func TestAnswer(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
		want string
	}{
		{&ContractFieldError{Paths: []string{"spec.version"}}, 422, `"error":"contract-field-read-only","paths":["spec.version"]`},
		{&SchemaError{Detail: "x"}, 422, `{"detail":"x","error":"blueprint-schema-invalid"}`},
		{errors.New("gitea down"), 502, `{"detail":"gitea down","error":"gitea-upstream"}`},
		{nil, 200, ""},
	} {
		rec := httptest.NewRecorder()
		wrote := Answer(rec, c.err)
		if wrote != (c.err != nil) || rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) || (!wrote && rec.Body.Len() != 0) {
			t.Errorf("%v: code %d body %s", c.err, rec.Code, rec.Body.String())
		}
	}
}
