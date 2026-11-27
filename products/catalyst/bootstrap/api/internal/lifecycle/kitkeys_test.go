package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

const kkFQDN = "sov.example.test"

func kkPath(region string) string {
	return "clusters/" + kkFQDN + "/regions/" + region + "/platform/root/sovereign-variables.yaml"
}

func kkSeed(region string) []byte {
	return []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  annotations:\n    catalyst.openova.io/change-id: seed\n" +
		"    kustomize.toolkit.fluxcd.io/substitute: disabled\n  name: sovereign-variables\n  namespace: flux-system\n" +
		"data:\n  MODEL_X: \"false\"\n  SOVEREIGN_REGION_ID: " + region + "\n")
}

// kkWriter seeds the documents of the regions alpha and beta.
func kkWriter(t *testing.T) *fake.Writer {
	w := fake.New()
	t.Cleanup(w.Close)
	w.Put(FleetRepo, kkPath("alpha"), kkSeed("alpha"))
	w.Put(FleetRepo, kkPath("beta"), kkSeed("beta"))
	return w
}

var kkOwner = &auth.Claims{Email: "owner@example.test", Tier: "owner"}

func kkPut(t *testing.T, g gitwrite.Writer, claims *auth.Claims, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	route := &KitKeysRoute{Git: g, FQDN: kkFQDN, Settable: map[string]bool{"MODEL_X": true}}
	router := chi.NewRouter()
	router.Put("/api/v1/sovereign/kit-keys/{key}", route.Put)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sovereign/kit-keys/"+key, strings.NewReader(body))
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func kkRaw(t *testing.T, w *fake.Writer, region string) string {
	t.Helper()
	b, _, err := w.Read(context.Background(), gitwrite.Catalog(), FleetRepo, kkPath(region))
	if err != nil {
		t.Fatalf("read %s: %v", region, err)
	}
	return string(b)
}

// kkData reads the data and the change-id annotation of one region's document.
func kkData(t *testing.T, w *fake.Writer, region string) (map[string]string, string) {
	t.Helper()
	var doc struct {
		Metadata struct{ Annotations map[string]string } `json:"metadata"`
		Data     map[string]string                       `json:"data"`
	}
	if err := yaml.Unmarshal([]byte(kkRaw(t, w, region)), &doc); err != nil {
		t.Fatalf("parse %s: %v", region, err)
	}
	return doc.Data, doc.Metadata.Annotations["catalyst.openova.io/change-id"]
}

func TestKitKeyCommitsEveryRegion(t *testing.T) {
	w := kkWriter(t)
	rec := kkPut(t, w, kkOwner, "MODEL_X", `{"value":"true"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	commits := w.Commits()
	if len(commits) != 1 {
		t.Fatalf("%d commits, want 1", len(commits))
	}
	c := commits[0]
	if want := []string{kkPath("alpha"), kkPath("beta")}; !reflect.DeepEqual(c.Paths, want) {
		t.Fatalf("commit paths %v, want %v", c.Paths, want)
	}
	if c.Author.Email != "owner@example.test" || !strings.HasPrefix(c.Message, "kit-keys: set MODEL_X\n") || !strings.HasSuffix(c.Message, "\nRequested-Via: api") {
		t.Fatalf("author %v, message %q", c.Author, c.Message)
	}
	var resp struct {
		Commit string   `json:"commit"`
		Paths  []string `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Commit != c.SHA || !reflect.DeepEqual(resp.Paths, c.Paths) {
		t.Fatalf("response %s (%v), want commit %s and paths %v", rec.Body, err, c.SHA, c.Paths)
	}
	for _, region := range []string{"alpha", "beta"} {
		data, changeID := kkData(t, w, region)
		if want := map[string]string{"MODEL_X": "true", "SOVEREIGN_REGION_ID": region}; !reflect.DeepEqual(data, want) {
			t.Fatalf("%s data %v, want %v", region, data, want)
		}
		if changeID == "seed" || !strings.Contains(c.Message, "\nChange-Id: "+changeID+"\n") {
			t.Fatalf("%s change-id %q is not the Change-Id of the commit:\n%s", region, changeID, c.Message)
		}
	}
}

func TestKitKeyNamedRegionOnly(t *testing.T) {
	w := kkWriter(t)
	rec := kkPut(t, w, kkOwner, "MODEL_X", `{"value":"on","regions":["beta","beta"]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if c := w.Commits(); len(c) != 1 || !reflect.DeepEqual(c[0].Paths, []string{kkPath("beta")}) {
		t.Fatalf("commits %+v, want one of %s only", c, kkPath("beta"))
	}
	if got := kkRaw(t, w, "alpha"); got != string(kkSeed("alpha")) {
		t.Fatalf("alpha changed:\n%s", got)
	}
	if data, _ := kkData(t, w, "beta"); data["MODEL_X"] != "on" {
		t.Fatalf("beta MODEL_X %q, want on", data["MODEL_X"])
	}
}

// Every refusal answers its code and error, and commits nothing.
func TestKitKeyRefusals(t *testing.T) {
	admin := &auth.Claims{Email: "admin@example.test", Tier: "admin"}
	cases := []struct {
		name, key, body string
		claims          *auth.Claims
		code            int
		errCode, field  string
	}{
		{"no session", "MODEL_X", `{"value":"true"}`, nil, 403, "forbidden", ""},
		{"tier admin", "MODEL_X", `{"value":"true"}`, admin, 403, "forbidden", ""},
		{"kit key not listed", "SOVEREIGN_REGION_ID", `{"value":"x"}`, kkOwner, 400, "key-not-settable", "key"},
		{"no value", "MODEL_X", `{"regions":["alpha"]}`, kkOwner, 400, "bad-value", "value"},
		{"control character", "MODEL_X", `{"value":"a\nb"}`, kkOwner, 400, "bad-value", "value"},
		{"unknown field", "MODEL_X", `{"value":"true","region":"alpha"}`, kkOwner, 400, "bad-body", ""},
		{"unknown region", "MODEL_X", `{"value":"true","regions":["alpha","delta"]}`, kkOwner, 400, "unknown-region", "regions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := kkWriter(t)
			rec := kkPut(t, w, tc.claims, tc.key, tc.body)
			var got map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
			if rec.Code != tc.code || got["error"] != tc.errCode || got["field"] != tc.field {
				t.Fatalf("status %d body %s, want %d error %q field %q", rec.Code, rec.Body, tc.code, tc.errCode, tc.field)
			}
			if c := w.Commits(); len(c) != 0 {
				t.Fatalf("%d commits after a refusal", len(c))
			}
			for _, region := range []string{"alpha", "beta"} {
				if got := kkRaw(t, w, region); got != string(kkSeed(region)) {
					t.Fatalf("%s changed after a refusal:\n%s", region, got)
				}
			}
		})
	}
}

func TestKitKeySameValueMakesNoCommit(t *testing.T) {
	w := kkWriter(t)
	if rec := kkPut(t, w, kkOwner, "MODEL_X", `{"value":"false"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if c := w.Commits(); len(c) != 0 {
		t.Fatalf("%d commits for a value already held", len(c))
	}
}

func TestKitKeyRegionNotSeeded(t *testing.T) {
	w := kkWriter(t)
	w.Put(FleetRepo, "clusters/"+kkFQDN+"/regions/gamma/platform/policy/mode.yaml", []byte("kind: ConfigMap\n"))
	rec := kkPut(t, w, kkOwner, "MODEL_X", `{"value":"true"}`)
	if !strings.Contains(rec.Body.String(), `"region-not-seeded"`) || rec.Code != http.StatusConflict || len(w.Commits()) != 0 {
		t.Fatalf("status %d body %s, %d commits; want 409 region-not-seeded and no commit", rec.Code, rec.Body, len(w.Commits()))
	}
}

// movingWriter lets another writer change alpha's file between the route's
// read and its commit.
type movingWriter struct{ *fake.Writer }

func (m movingWriter) Commit(ctx context.Context, c gitwrite.Change) (gitwrite.Result, error) {
	m.Put(FleetRepo, kkPath("alpha"), []byte(strings.Replace(string(kkSeed("alpha")), `"false"`, `"moved"`, 1)))
	return m.Writer.Commit(ctx, c)
}

func TestKitKeyHeadMoved(t *testing.T) {
	w := kkWriter(t)
	rec := kkPut(t, movingWriter{w}, kkOwner, "MODEL_X", `{"value":"true"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"head-moved"`) || len(w.Commits()) != 0 {
		t.Fatalf("status %d body %s, %d commits; want 409 head-moved and no commit", rec.Code, rec.Body, len(w.Commits()))
	}
	if data, _ := kkData(t, w, "alpha"); data["MODEL_X"] != "moved" {
		t.Fatalf("alpha MODEL_X %q, want the other writer's value", data["MODEL_X"])
	}
}

type downWriter struct{ gitwrite.Writer }

func (downWriter) List(context.Context, gitwrite.Scope, gitwrite.Repo, string) ([]gitwrite.Entry, error) {
	return nil, gitwrite.ErrUnavailable
}

func TestKitKeyGitUnreachable(t *testing.T) {
	for name, g := range map[string]gitwrite.Writer{"not configured": nil, "unreachable": downWriter{}} {
		rec := kkPut(t, g, kkOwner, "MODEL_X", `{"value":"true"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status %d body %s, want 503", name, rec.Code, rec.Body)
		}
	}
}

func TestParseKitKeyList(t *testing.T) {
	got, err := ParseKitKeyList("# comment\n\nGPU_RUNTIME\n  MODEL_X  \n")
	if err != nil || !reflect.DeepEqual(got, map[string]bool{"GPU_RUNTIME": true, "MODEL_X": true}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := ParseKitKeyList("MODEL_X\nmodel-x\n"); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("a line that is not a key name: error %v", err)
	}
	// 58 characters fit the commit summary "kit-keys: set <key>" of 72; 59 do not.
	if _, err := ParseKitKeyList(strings.Repeat("A", 58) + "\n" + strings.Repeat("B", 59)); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("a key of 59 characters: error %v", err)
	}
}
