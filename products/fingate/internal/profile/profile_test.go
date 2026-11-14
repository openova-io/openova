package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

const rw400Dir = "../../profiles/rw-4.0.0"

// want holds the facts of one document of profile rw-4.0.0, counted from the publisher's files.
type want struct {
	api, file, sha256                  string
	operations, served, called, signed int
}

var rw400 = []want{
	{"account-info", "account-info-openapi.yaml", "7d9297935e11971173664ba793c14b6fd64e162a97d2de46967cd7f9cc0ca792", 29, 29, 0, 0},
	{"payment-initiation", "payment-initiation-openapi.yaml", "1cb84c8b6972cb56f414448af94f5c7595d6ebe13a5c41deeba5904e04efb41e", 41, 16, 0, 15},
	{"vrp", "vrp-openapi.yaml", "f547f3cbc465970c65a744dce9c28fa5f0201fb5f07b8f35af79942e3c64fb20", 9, 9, 0, 5},
	{"confirmation-funds", "confirmation-funds-openapi.yaml", "2ab1717555e8e960980be39f07821e1c0d280662a0fbb84dd562168cb5604358", 4, 4, 0, 0},
	{"events", "events-openapi.yaml", "ba248d53243ddad30ee856e175f8df38669eac2b1015c7c10d88db56afbcb642", 5, 5, 0, 2},
	{"event-notifications", "event-notifications-openapi.yaml", "c60932f376472697c22af602d4deba5842921732aa547ef71d6c41a67389908a", 1, 0, 1, 0},
}

func loadRW400(t *testing.T) *Profile {
	t.Helper()
	p, err := LoadDir(rw400Dir)
	if err != nil {
		t.Fatalf("LoadDir(%s): %v", rw400Dir, err)
	}
	return p
}

// TestRW400Profile asserts each hash, each operation count, the totals and the signature-header counts.
func TestRW400Profile(t *testing.T) {
	p := loadRW400(t)
	if len(p.Documents) != len(rw400) {
		t.Fatalf("documents: got %d, want %d", len(p.Documents), len(rw400))
	}
	total, served, called, notOffered := 0, 0, 0, 0
	for i, w := range rw400 {
		d := p.Documents[i]
		if d.API != w.api || d.File != w.file || d.SHA256 != w.sha256 {
			t.Errorf("documents[%d]: got %s %s %s, want %s %s %s", i, d.API, d.File, d.SHA256, w.api, w.file, w.sha256)
		}
		if d.Operations != w.operations || len(d.Ops) != w.operations || d.Served != w.served || d.Called != w.called {
			t.Errorf("%s: operations %d (read %d), served %d, called %d; want %d, %d, %d",
				w.api, d.Operations, len(d.Ops), d.Served, d.Called, w.operations, w.served, w.called)
		}
		if got := d.SignedCount(); got != w.signed {
			t.Errorf("%s: %d operations declare %s, want %d", w.api, got, SignatureHeader, w.signed)
		}
		total += len(d.Ops)
		served += d.Served
		called += d.Called
		for _, op := range d.Ops {
			if p.NotOfferedPath(op.Path) {
				notOffered++
			}
		}
	}
	if total != 89 || served != 63 || called != 1 || notOffered != 25 {
		t.Errorf("totals: %d operations, %d served, %d called, %d not offered; want 89, 63, 1, 25", total, served, called, notOffered)
	}
	if p.ID != "rw-4.0.0" || p.BasePath != "/open-banking/v4.0" || p.NotImplementedStatus != 404 || p.IdempotencyHours != 24 {
		t.Errorf("got id %q, basePath %q, notImplementedStatus %d, idempotencyHours %d", p.ID, p.BasePath, p.NotImplementedStatus, p.IdempotencyHours)
	}
	wantNotOffered := []string{"/international-payment-consents", "/international-payments",
		"/international-scheduled-payment-consents", "/international-scheduled-payments",
		"/international-standing-order-consents", "/international-standing-orders",
		"/file-payment-consents", "/file-payments"}
	if !reflect.DeepEqual(p.NotOffered, wantNotOffered) {
		t.Errorf("notOffered: got %q", p.NotOffered)
	}
	wantSecurity := Security{FAPI: "fapi1-advanced-final", ClientAuth: []string{"tls_client_auth", "private_key_jwt"},
		SigningAlgorithms: []string{"PS256"}, ConsentCarrier: "intent-claim", AccessTokenSeconds: 300}
	if !reflect.DeepEqual(p.Security, wantSecurity) {
		t.Errorf("security: got %+v", p.Security)
	}
}

// consentSchemas names, per consent family, the consent response schemas whose Data.Status
// enumeration holds the family's wire codes in the documents of rw-4.0.0.
var consentSchemas = map[string]struct {
	file    string
	schemas []string
}{
	"account-access":     {"account-info-openapi.yaml", []string{"OBReadConsentResponse1"}},
	"payment":            {"payment-initiation-openapi.yaml", []string{"OBWriteDomesticConsentResponse5", "OBWriteDomesticScheduledConsentResponse5", "OBWriteDomesticStandingOrderConsentResponse6"}},
	"funds-confirmation": {"confirmation-funds-openapi.yaml", []string{"OBFundsConfirmationConsentResponse1"}},
	"vrp":                {"vrp-openapi.yaml", []string{"OBDomesticVRPConsentResponse"}},
}

// TestRW400ConsentCodes asserts the consent status map and that every code is one the documents declare.
func TestRW400ConsentCodes(t *testing.T) {
	p := loadRW400(t)
	wantStatus := map[string]map[string]string{
		"account-access":     {"awaiting": "AWAU", "authorised": "AUTH", "rejected": "RJCT", "revoked": "CANC", "expired": "EXPD"},
		"payment":            {"awaiting": "AWAU", "authorised": "AUTH", "rejected": "RJCT", "consumed": "COND"},
		"funds-confirmation": {"awaiting": "AWAU", "authorised": "AUTH", "rejected": "RJCT", "revoked": "CANC", "expired": "EXPD"},
		"vrp":                {"awaiting": "AWAU", "authorised": "AUTH", "rejected": "RJCT", "revoked": "CANC", "expired": "EXPD"},
	}
	if !reflect.DeepEqual(p.ConsentStatus, wantStatus) {
		t.Errorf("consentStatus: got %v", p.ConsentStatus)
	}
	fsys := osDir(t)
	for family, codes := range p.ConsentStatus {
		cs := consentSchemas[family]
		doc := readYAML(t, fsys, cs.file)
		for _, schema := range cs.schemas {
			enum := map[string]bool{}
			for _, v := range dig(t, doc, "components", "schemas", schema, "properties", "Data", "properties", "Status", "enum").([]any) {
				enum[v.(string)] = true
			}
			for state, code := range codes {
				if !enum[code] {
					t.Errorf("%s.%s: code %s is not in the Status enumeration of %s in %s", family, state, code, schema, cs.file)
				}
			}
		}
	}
}

// TestRW400Documents asserts that each document is OpenAPI 3.0.0 of version 4.0.0 and that the
// files on disk hash to the values the profile states.
func TestRW400Documents(t *testing.T) {
	fsys := osDir(t)
	for _, w := range rw400 {
		raw, err := fs.ReadFile(fsys, w.file)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != w.sha256 {
			t.Errorf("%s: the file does not hash to %s", w.file, w.sha256)
		}
		doc := readYAML(t, fsys, w.file)
		if doc["openapi"] != "3.0.0" || dig(t, doc, "info", "version") != "4.0.0" {
			t.Errorf("%s: openapi %v, info.version %v; want 3.0.0 and 4.0.0", w.file, doc["openapi"], dig(t, doc, "info", "version"))
		}
	}
}

// TestLoadRejects proves that each rule of the loader can fail.
func TestLoadRejects(t *testing.T) {
	// first is the documents line of the fixture. second returns it with one more document after it.
	const first = "  - {api: a, file: a.yaml, sha256: SUM, operations: 3, served: 1, called: 1}\n"
	second := func(api, file string) string {
		return first + "  - {api: " + api + ", file: " + file + ", sha256: " + strings.Repeat("0", 64) + ", operations: 1, served: 1}\n"
	}
	cases := []struct{ name, find, replace, err string }{
		{"wrong schema", "schema: openova.io/fingate-profile/v1", "schema: other/v1", "schema is"},
		{"id is not the folder", "id: p1", "id: p2", "want the folder name"},
		{"unknown field", "idempotencyHours: 24", "idempotencyHours: 24\nextra: 1", "field extra not found"},
		{"missing field", "idempotencyHours: 24", "", "field idempotencyHours is missing"},
		{"missing served", "served: 1, ", "", "field documents[0].served is missing"},
		{"bad basePath", "basePath: /ob", "basePath: ob", "basePath"},
		{"status out of range", "notImplementedStatus: 404", "notImplementedStatus: 200", "notImplementedStatus"},
		{"api twice", first, second("a", "b.yaml"), `documents[1]: api "a" is empty or not unique`},
		{"file twice", first, second("b", "a.yaml"), `documents[1]: file "a.yaml" is empty, not unique`},
		{"file with a folder", "file: a.yaml", "file: x/a.yaml", "not a plain file name"},
		{"sha256 not hex", "sha256: SUM", "sha256: xyz", "is not 64 lower-case hex"},
		{"wrong hash", "sha256: SUM", "sha256: " + strings.Repeat("0", 64), "SHA-256 is"},
		{"counts do not fit", "served: 1", "served: 3", "do not fit"},
		{"wrong operation count", "operations: 3", "operations: 2", "it has 3 operations, the profile says 2"},
		{"sum does not hold", "served: 1", "served: 0", "is not the 3 operations"},
		{"bad notOffered", "  - /b", "  - b", "notOffered"},
		{"bad fapi", "fapi: fapi1-advanced-final", "fapi: fapi2", "security.fapi"},
		{"bad client auth", "clientAuth: [tls_client_auth]", "clientAuth: [client_secret_basic]", "security.clientAuth"},
		{"bad algorithm", "signingAlgorithms: [PS256]", "signingAlgorithms: [RS256]", "security.signingAlgorithms"},
		{"bad carrier", "consentCarrier: intent-claim", "consentCarrier: other", "security.consentCarrier"},
		{"second carrier", "consentCarrier: intent-claim", "consentCarrier: authorization-details", "security.consentCarrier"},
		{"token lifetime", "accessTokenSeconds: 300", "accessTokenSeconds: 30", "security.accessTokenSeconds"},
		{"missing family", "  vrp: {awaiting: AWAU, authorised: AUTH, rejected: RJCT}\n", "", "consentStatus has the families"},
		{"unknown state", "revoked: CANC", "cancelled: CANC", "is not a consent state"},
		{"consumed outside payment", "vrp: {awaiting: AWAU,", "vrp: {consumed: COND, awaiting: AWAU,", "only the payment family"},
		{"missing state", "funds-confirmation: {awaiting: AWAU, ", "funds-confirmation: {", "no code for the state awaiting"},
		{"bad code", "rejected: RJCT, revoked", "rejected: rjct, revoked", "is not four capital letters"},
		{"idempotency below 1", "idempotencyHours: 24", "idempotencyHours: 0", "idempotencyHours 0"},
	}
	if _, err := Load(fixture(t, "", ""), "p1"); err != nil {
		t.Fatalf("the valid fixture does not load: %v", err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(fixture(t, c.find, c.replace), "p1")
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("got error %v, want one that contains %q", err, c.err)
			}
		})
	}
	t.Run("broken parameter reference", func(t *testing.T) {
		doc := strings.Replace(fixtureDoc, "#/components/parameters/sig", "#/components/parameters/none", 1)
		if _, err := Load(fixtureWith(t, "", "", doc), "p1"); err == nil || !strings.Contains(err.Error(), "is not a parameter of the document") {
			t.Errorf("got error %v", err)
		}
	})
	t.Run("document is not OpenAPI 3.0", func(t *testing.T) {
		doc := strings.Replace(fixtureDoc, "openapi: 3.0.0", "openapi: 3.1.0", 1)
		if _, err := Load(fixtureWith(t, "", "", doc), "p1"); err == nil || !strings.Contains(err.Error(), "want a version 3.0") {
			t.Errorf("got error %v", err)
		}
	})
	t.Run("signature header is counted", func(t *testing.T) {
		p, err := Load(fixture(t, "", ""), "p1")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Documents[0].SignedCount(); got != 1 {
			t.Errorf("signed operations: got %d, want 1", got)
		}
	})
	t.Run("an optional signature header is not counted", func(t *testing.T) {
		doc := strings.Replace(fixtureDoc, "required: true", "required: false", 1)
		p, err := Load(fixtureWith(t, "", "", doc), "p1")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Documents[0].SignedCount(); got != 0 {
			t.Errorf("signed operations: got %d, want 0", got)
		}
	})
	t.Run("a prefix covers whole path segments", func(t *testing.T) {
		p, err := Load(fixture(t, "", ""), "p1")
		if err != nil {
			t.Fatal(err)
		}
		for docPath, want := range map[string]bool{"/b": true, "/b/{id}": true, "/bc": false, "/a/b": false} {
			if got := p.NotOfferedPath(docPath); got != want {
				t.Errorf("NotOfferedPath(%s): got %v, want %v", docPath, got, want)
			}
		}
	})
}

// fixtureDoc has three operations: GET /a, POST /a, which declares the signature header, and
// GET /b/{id}, which lies under the notOffered prefix /b. fixtureProfile counts one served, one
// called and one not offered.
const fixtureDoc = `openapi: 3.0.0
info: {version: 1.0.0}
paths:
  /a:
    get: {responses: {}}
    post:
      parameters:
        - $ref: '#/components/parameters/sig'
      responses: {}
  /b/{id}:
    get: {responses: {}}
components:
  parameters:
    sig: {in: header, name: x-jws-signature, required: true, schema: {type: string}}
`

const fixtureProfile = `schema: openova.io/fingate-profile/v1
id: p1
basePath: /ob
notImplementedStatus: 404
documents:
  - {api: a, file: a.yaml, sha256: SUM, operations: 3, served: 1, called: 1}
notOffered:
  - /b
security:
  fapi: fapi1-advanced-final
  clientAuth: [tls_client_auth]
  signingAlgorithms: [PS256]
  consentCarrier: intent-claim
  accessTokenSeconds: 300
consentStatus:
  account-access: {awaiting: AWAU, authorised: AUTH, rejected: RJCT, revoked: CANC}
  payment: {awaiting: AWAU, authorised: AUTH, rejected: RJCT, consumed: COND}
  funds-confirmation: {awaiting: AWAU, authorised: AUTH, rejected: RJCT}
  vrp: {awaiting: AWAU, authorised: AUTH, rejected: RJCT}
idempotencyHours: 24
`

func fixture(t *testing.T, find, replace string) fs.FS {
	return fixtureWith(t, find, replace, fixtureDoc)
}

func fixtureWith(t *testing.T, find, replace, doc string) fs.FS {
	t.Helper()
	text := fixtureProfile
	if find != "" {
		if strings.Count(text, find) != 1 {
			t.Fatalf("fixture: %q does not occur exactly once", find)
		}
		text = strings.Replace(text, find, replace, 1)
	}
	sum := sha256.Sum256([]byte(doc))
	text = strings.Replace(text, "SUM", hex.EncodeToString(sum[:]), 1)
	return fstest.MapFS{
		FileName: &fstest.MapFile{Data: []byte(text)},
		"a.yaml": &fstest.MapFile{Data: []byte(doc)},
	}
}

func osDir(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(rw400Dir)
}

func readYAML(t *testing.T, fsys fs.FS, name string) map[string]any {
	t.Helper()
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return doc
}

func dig(t *testing.T, node any, keys ...string) any {
	t.Helper()
	for _, k := range keys {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("%v: not a mapping at %s", keys, k)
		}
		node, ok = m[k]
		if !ok {
			t.Fatalf("%v: no key %s", keys, k)
		}
	}
	return node
}
