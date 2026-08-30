package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

const (
	opEmail   = "ops@nc.example"
	apiSecret = "APISECRET-not-real-2222-aaaa"
)

// recMail captures outbound mail so the test can read PINs and invite links.
type recMail struct {
	mu   sync.Mutex
	msgs []string
}

func (m *recMail) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, to+"|"+subject+"|"+body)
	return nil
}

func (m *recMail) last(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.msgs) == 0 {
		t.Fatal("no mail captured")
	}
	return m.msgs[len(m.msgs)-1]
}

// fakeVerifier accepts projects whose id starts with "ok-" and rejects the
// rest with the gateway's 401 code.
type fakeVerifier struct {
	mu    sync.Mutex
	calls []string
}

func (v *fakeVerifier) VerifyProject(_ context.Context, region, projectID, accessKey, secretKey string) error {
	v.mu.Lock()
	v.calls = append(v.calls, region+"/"+projectID+"/"+accessKey)
	v.mu.Unlock()
	if strings.HasPrefix(projectID, "ok-") {
		return nil
	}
	return &VerifyError{Code: "APIGW.0301", Message: "Incorrect IAM authentication information: verify aksk signature fail " + secretKey, Unauthorized: true}
}

type client struct {
	t       *testing.T
	h       http.Handler
	cookies []*http.Cookie
}

func (c *client) do(method, path, contentType string, body io.Reader) (*httptest.ResponseRecorder, map[string]any) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie {
			if ck.MaxAge < 0 {
				c.cookies = nil
			} else {
				c.cookies = []*http.Cookie{ck}
			}
		}
	}
	var out map[string]any
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func (c *client) json(method, path string, v any) (*httptest.ResponseRecorder, map[string]any) {
	b, _ := json.Marshal(v)
	return c.do(method, path, "application/json", bytes.NewReader(b))
}

func (c *client) mustJSON(method, path string, v any, want int) map[string]any {
	c.t.Helper()
	rec, out := c.json(method, path, v)
	if rec.Code != want {
		c.t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body.String())
	}
	return out
}

func (c *client) must(method, path string, want int) map[string]any {
	c.t.Helper()
	rec, out := c.do(method, path, "", nil)
	if rec.Code != want {
		c.t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body.String())
	}
	return out
}

var pinRe = regexp.MustCompile(`code is (\d{6})`)

func (c *client) signIn(email string, mail *recMail) map[string]any {
	c.t.Helper()
	c.mustJSON("POST", "/api/v1/auth/pin/request", map[string]string{"email": email}, 202)
	m := pinRe.FindStringSubmatch(mail.last(c.t))
	if m == nil {
		c.t.Fatalf("no PIN in mail: %s", mail.last(c.t))
	}
	return c.mustJSON("POST", "/api/v1/auth/pin/verify", map[string]string{"email": email, "code": m[1]}, 200)
}

func setupAPI(t *testing.T) (http.Handler, *store.Store, *recMail, *fakeVerifier, *bytes.Buffer) {
	t.Helper()
	st := testdb.Open(t)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{3}, 32))
	mail := &recMail{}
	ver := &fakeVerifier{}
	var logbuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := New(Deps{
		Store:    st,
		Keys:     keys,
		Mail:     mail,
		Verifier: ver,
		Config:   config.Config{PublicURL: "https://billing.t99.omani.works", Profile: "operator-central", OperatorEmails: []string{opEmail}},
		Metrics:  metrics.New(),
		Version:  "test",
	})
	return h, st, mail, ver, &logbuf
}

func multipartCSV(t *testing.T, csvText string) (string, io.Reader) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "import.csv")
	_, _ = io.WriteString(fw, csvText)
	_ = mw.Close()
	return mw.FormDataContentType(), &buf
}

func TestIntegrationEndToEndOnboardingUsageStatements(t *testing.T) {
	h, st, mail, ver, logbuf := setupAPI(t)
	ctx := context.Background()
	op := &client{t: t, h: h}

	// Unknown email: same 202, no mail, no PIN stored.
	before := len(mail.msgs)
	op.mustJSON("POST", "/api/v1/auth/pin/request", map[string]string{"email": "stranger@example.com"}, 202)
	if len(mail.msgs) != before {
		t.Fatal("mail sent to an unknown email")
	}

	// Operator signs in.
	me := op.signIn(opEmail, mail)
	if me["role"] != store.RoleOperator || me["profile"] != "operator-central" {
		t.Fatalf("me = %+v", me)
	}
	if op.must("GET", "/api/v1/auth/me", 200)["email"] != opEmail {
		t.Fatal("session cookie not honoured")
	}
	// Throttled second request within the window.
	if rec, _ := op.json("POST", "/api/v1/auth/pin/request", map[string]string{"email": opEmail}); rec.Code != 429 {
		t.Fatalf("throttle = %d", rec.Code)
	}

	// Price book from the CSV template.
	pb := op.mustJSON("POST", "/api/v1/pricebooks", map[string]any{"name": "NC list 2026", "annual_divisor": 8760, "bill_stopped": "storage-only"}, 201)
	pbID := pb["id"].(string)
	ct, body := multipartCSV(t, rating.PriceBookCSVTemplate)
	rec, out := op.do("POST", "/api/v1/pricebooks/"+pbID+"/import", ct, body)
	if rec.Code != 200 || out["imported"].(float64) != 9 {
		t.Fatalf("pricebook import = %d %s", rec.Code, rec.Body.String())
	}
	if got := op.must("GET", "/api/v1/pricebooks/"+pbID, 200); len(got["items"].([]any)) != 9 {
		t.Fatalf("pricebook items = %v", got["items"])
	}
	if rec, _ := op.do("GET", "/api/v1/pricebooks/template.csv", "", nil); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "sku,unit,annual_price") {
		t.Fatalf("template = %d", rec.Code)
	}

	// Bulk import: two customers, pending, with pre-registered projects.
	csvText := "slug,name,admin_email,region,project_ids,price_book,billing_mode,start_date\n" +
		"acme,Acme Trading,admin@acme.example,me-east-215,ok-p1;ok-p2,NC list 2026,chargeback,2026-08-01\n" +
		"beta,Beta Co,finance@beta.example,me-east-215,ok-p3,NC list 2026,showback,2026-08-01\n"
	ct, body = multipartCSV(t, csvText)
	rec, out = op.do("POST", "/api/v1/customers/import", ct, body)
	if rec.Code != 200 || out["created"].(float64) != 2 || out["updated"].(float64) != 0 || len(out["errors"].([]any)) != 0 {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	list := op.must("GET", "/api/v1/customers", 200)["customers"].([]any)
	if len(list) != 2 {
		t.Fatalf("customers = %d", len(list))
	}
	var acmeID, betaID string
	for _, c := range list {
		m := c.(map[string]any)
		if m["status"] != "pending" || m["source_count"].(float64) == 0 {
			t.Fatalf("imported customer = %+v", m)
		}
		if m["slug"] == "acme" {
			acmeID = m["id"].(string)
		} else {
			betaID = m["id"].(string)
		}
	}
	// Re-import updates instead of duplicating.
	ct, body = multipartCSV(t, csvText)
	rec, out = op.do("POST", "/api/v1/customers/import", ct, body)
	if rec.Code != 200 || out["updated"].(float64) != 2 {
		t.Fatalf("re-import = %s", rec.Body.String())
	}

	// Invite → activate.
	inv := op.mustJSON("POST", "/api/v1/customers/"+acmeID+"/invite", nil, 201)
	inviteURL := inv["invite_url"].(string)
	if !strings.HasPrefix(inviteURL, "https://billing.t99.omani.works/activate/") || !strings.Contains(mail.last(t), inviteURL) {
		t.Fatalf("invite url = %s mail = %s", inviteURL, mail.last(t))
	}
	token := strings.TrimPrefix(inviteURL, "https://billing.t99.omani.works/activate/")
	cust := &client{t: t, h: h} // a fresh browser, no session
	pre := cust.must("GET", "/api/v1/invites/"+token, 200)
	if pre["region"] != "me-east-215" || len(pre["project_ids"].([]any)) != 2 || pre["email"] != "admin@acme.example" {
		t.Fatalf("invite preview = %+v", pre)
	}
	// A bad project fails verification; the invite stays usable.
	rec, out = cust.json("POST", "/api/v1/invites/"+token+"/activate", map[string]any{"region": "me-east-215", "project_ids": []string{"ok-p1", "bad-p9"}, "access_key": "AKACME", "secret_key": apiSecret})
	if rec.Code != 422 || out["activated"] != false {
		t.Fatalf("partial activation = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), apiSecret) {
		t.Fatal("secret echoed in activation response")
	}
	if !strings.Contains(rec.Body.String(), "APIGW.0301") {
		t.Fatalf("gateway code missing from activation results: %s", rec.Body.String())
	}
	if len(cust.cookies) != 0 {
		t.Fatal("session issued on failed activation")
	}
	rec, out = cust.json("POST", "/api/v1/invites/"+token+"/activate", map[string]any{"region": "me-east-215", "project_ids": []string{"ok-p1", "ok-p2"}, "access_key": "AKACME", "secret_key": apiSecret})
	if rec.Code != 200 || out["activated"] != true {
		t.Fatalf("activation = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), apiSecret) {
		t.Fatal("secret echoed in activation response")
	}
	if len(cust.cookies) == 0 {
		t.Fatal("no session after activation")
	}
	if cust.must("GET", "/api/v1/invites/"+token, 410) == nil {
		t.Fatal("used invite still served")
	}
	me = cust.must("GET", "/api/v1/auth/me", 200)
	if me["role"] != store.RoleCustomerAdmin || me["customer_id"] != acmeID || me["customer"].(map[string]any)["status"] != "active" {
		t.Fatalf("customer me = %+v", me)
	}

	// Customer scope: own rows only, no operator surfaces, no secrets.
	mine := cust.must("GET", "/api/v1/customers", 200)["customers"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["id"] != acmeID {
		t.Fatalf("customer list = %+v", mine)
	}
	cust.must("GET", "/api/v1/customers/"+betaID, 404)
	cust.must("GET", "/api/v1/customers/"+betaID+"/sources", 404)
	cust.must("GET", "/api/v1/overview", 403)
	cust.mustJSON("POST", "/api/v1/customers", map[string]any{"slug": "x"}, 403)
	srcRec, srcOut := cust.do("GET", "/api/v1/customers/"+acmeID+"/sources", "", nil)
	sources := srcOut["sources"].([]any)
	if srcRec.Code != 200 || len(sources) != 2 {
		t.Fatalf("sources = %s", srcRec.Body.String())
	}
	for _, s := range sources {
		m := s.(map[string]any)
		if m["status"] != "verified" || m["access_key"] != "AKACME" {
			t.Fatalf("source = %+v", m)
		}
	}
	if strings.Contains(srcRec.Body.String(), apiSecret) || strings.Contains(strings.ToLower(srcRec.Body.String()), "secret") {
		t.Fatalf("sources response leaks secret material: %s", srcRec.Body.String())
	}
	srcID := sources[0].(map[string]any)["id"].(string)
	// Viewer role: read-only.
	cust.mustJSON("POST", "/api/v1/customers/"+acmeID+"/users", map[string]string{"email": "viewer@acme.example", "role": "viewer"}, 201)
	viewer := &client{t: t, h: h}
	if v := viewer.signIn("viewer@acme.example", mail); v["role"] != store.RoleCustomerViewer {
		t.Fatalf("viewer me = %+v", v)
	}
	viewer.mustJSON("POST", "/api/v1/sources/"+srcID+"/credential", map[string]string{"access_key": "x", "secret_key": "y"}, 403)
	viewer.must("GET", "/api/v1/customers/"+acmeID+"/usage", 200)

	// Usage for August, written the way the collector writes it.
	ws := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	for hr := 0; hr < 24; hr++ {
		status := "ACTIVE"
		if hr >= 12 {
			status = "SHUTOFF"
		}
		labels, _ := json.Marshal(map[string]any{"status": status, "name": "vm-1"})
		recs = append(recs, store.UsageRecord{CustomerID: acmeID, SourceID: srcID, ResourceID: "srv-1", ResourceKind: "ecs", SKU: "ecs.s6.large.2", Quantity: "1.000000", Unit: "instance-hour",
			WindowStart: ws.Add(time.Duration(hr) * time.Hour), WindowEnd: ws.Add(time.Duration(hr+1) * time.Hour), Region: "me-east-215", Labels: labels})
		recs = append(recs, store.UsageRecord{CustomerID: acmeID, SourceID: srcID, ResourceID: "eip-1", ResourceKind: "eip", SKU: "eip", Quantity: "1.000000", Unit: "hour",
			WindowStart: ws.Add(time.Duration(hr) * time.Hour), WindowEnd: ws.Add(time.Duration(hr+1) * time.Hour), Region: "me-east-215"})
		recs = append(recs, store.UsageRecord{CustomerID: acmeID, SourceID: srcID, ResourceID: "srv-1", ResourceKind: "ecs", SKU: "ecs.cpu_util", Quantity: "7.5", Unit: "pct-hour-avg",
			WindowStart: ws.Add(time.Duration(hr) * time.Hour), WindowEnd: ws.Add(time.Duration(hr+1) * time.Hour), Region: "me-east-215"})
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	usage := cust.must("GET", "/api/v1/customers/"+acmeID+"/usage?from=2026-08-01&to=2026-09-01&group_by=sku", 200)
	if rows := usage["rows"].([]any); len(rows) != 3 {
		t.Fatalf("usage rows = %+v", rows)
	}
	if rec, _ := cust.do("GET", "/api/v1/customers/"+acmeID+"/usage?group_by=weird", "", nil); rec.Code != 400 {
		t.Fatalf("bad group_by = %d", rec.Code)
	}

	// Statements: storage-only drops the 12 stopped instance-hours.
	run := op.mustJSON("POST", "/api/v1/statements/run", map[string]string{"period": "2026-08"}, 200)
	results := run["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("run results = %+v", results)
	}
	var stmtID string
	for _, r := range results {
		m := r.(map[string]any)
		if m["customer_id"] == acmeID {
			stmtID = m["statement_id"].(string)
			if m["lines"].(float64) != 2 || m["unpriced_skus"].([]any)[0] != "ecs.cpu_util" {
				t.Fatalf("acme result = %+v", m)
			}
		} else if m["lines"].(float64) != 0 || m["statement_id"] == "" {
			t.Fatalf("beta result = %+v", m)
		}
	}
	stmt := cust.must("GET", "/api/v1/statements/"+stmtID, 200)
	// ecs: 12 running hours × 0.1 = 1.2; eip: 24 × 0.005 = 0.12 → subtotal 1.32, tax 0.066, total 1.386.
	if stmt["subtotal"].(float64) != 1.32 || stmt["tax"].(float64) != 0.066 || stmt["total"].(float64) != 1.386 || stmt["status"] != "draft" {
		t.Fatalf("statement = %+v", stmt)
	}
	for _, l := range stmt["lines"].([]any) {
		m := l.(map[string]any)
		if m["sku"] == "ecs.s6.large.2" && (m["quantity"].(float64) != 12 || m["amount"].(float64) != 1.2 || m["resource_count"].(float64) != 1) {
			t.Fatalf("ecs line = %+v", m)
		}
	}
	rec, _ = cust.do("GET", "/api/v1/statements/"+stmtID+".csv", "", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || !strings.Contains(rec.Body.String(), "ecs.s6.large.2,instance-hour,12.000000,0.10000000,1.200000") {
		t.Fatalf("csv = %d %s", rec.Code, rec.Body.String())
	}
	if len(cust.must("GET", "/api/v1/customers/"+acmeID+"/statements", 200)["statements"].([]any)) != 1 {
		t.Fatal("customer statements list")
	}
	// Beta's statement is invisible to acme.
	all := op.must("GET", "/api/v1/statements?period=2026-08", 200)["statements"].([]any)
	if len(all) != 2 {
		t.Fatalf("operator statements = %d", len(all))
	}
	for _, s := range all {
		if id := s.(map[string]any)["id"].(string); id != stmtID {
			cust.must("GET", "/api/v1/statements/"+id, 404)
		}
	}
	cust.mustJSON("POST", "/api/v1/statements/"+stmtID+"/issue", nil, 403)
	if issued := op.mustJSON("POST", "/api/v1/statements/"+stmtID+"/issue", nil, 200); issued["status"] != "issued" {
		t.Fatalf("issued = %+v", issued)
	}
	run = op.mustJSON("POST", "/api/v1/statements/run", map[string]string{"period": "2026-08", "customer_id": acmeID}, 200)
	if r := run["results"].([]any)[0].(map[string]any); !strings.Contains(fmt.Sprint(r["error"]), "already issued") {
		t.Fatalf("re-run on issued = %+v", r)
	}
	ov := op.must("GET", "/api/v1/overview", 200)
	if ov["customers"].(map[string]any)["active"].(float64) != 1 || ov["last_period"].(map[string]any)["period"] != "2026-08" {
		t.Fatalf("overview = %+v", ov)
	}

	// Credential rotation with a rejected key: failure recorded, no secret anywhere.
	rec, out = cust.json("POST", "/api/v1/sources/"+srcID+"/credential", map[string]string{"access_key": "AKNEW", "secret_key": "ROTATED-" + apiSecret})
	if rec.Code != 200 {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
	}
	if out["source"].(map[string]any)["access_key"] != "AKNEW" {
		t.Fatalf("rotated source = %+v", out["source"])
	}
	// Re-verify with the stored (now rejected) credential path.
	ver.mu.Lock()
	verCalls := len(ver.calls)
	ver.mu.Unlock()
	rec, _ = cust.do("POST", "/api/v1/sources/"+srcID+"/verify", "", nil)
	if rec.Code != 200 {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body.String())
	}
	ver.mu.Lock()
	if len(ver.calls) != verCalls+1 || !strings.HasSuffix(ver.calls[len(ver.calls)-1], "/AKNEW") {
		t.Fatalf("verify did not use the stored credential: %v", ver.calls)
	}
	ver.mu.Unlock()

	// Audit trail is visible to the customer and carries no secret.
	audit := cust.must("GET", "/api/v1/customers/"+acmeID+"/audit", 200)
	entries := audit["entries"].([]any)
	if len(entries) < 5 {
		t.Fatalf("audit entries = %d", len(entries))
	}
	auditJSON, _ := json.Marshal(audit)
	for _, needle := range []string{apiSecret, "ROTATED-"} {
		if strings.Contains(string(auditJSON), needle) {
			t.Fatalf("audit leaks secret: %s", auditJSON)
		}
		if strings.Contains(logbuf.String(), needle) {
			t.Fatalf("logs leak secret: %s", logbuf.String())
		}
	}
	rows, _ := st.DB().QueryContext(ctx, `SELECT count(*) FROM credentials WHERE encode(secret_key_enc, 'escape') LIKE '%APISECRET%'`)
	defer rows.Close()
	var plain int
	for rows.Next() {
		_ = rows.Scan(&plain)
	}
	if plain != 0 {
		t.Fatal("secret stored in plaintext")
	}

	// Logout ends the session.
	cust.mustJSON("POST", "/api/v1/auth/logout", nil, 200)
	if len(cust.cookies) != 0 {
		t.Fatal("cookie not cleared")
	}
	op2 := &client{t: t, h: h, cookies: []*http.Cookie{{Name: sessionCookie, Value: "bogus"}}}
	op2.must("GET", "/api/v1/auth/me", 401)
}

func TestIntegrationOpsEndpoints(t *testing.T) {
	h, _, _, _, _ := setupAPI(t)
	c := &client{t: t, h: h}
	if out := c.must("GET", "/healthz", 200); out["status"] != "ok" {
		t.Fatalf("healthz = %+v", out)
	}
	if out := c.must("GET", "/readyz", 200); out["db"] != "ok" {
		t.Fatalf("readyz = %+v", out)
	}
	rec, _ := c.do("GET", "/metrics", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "chargeback_customers") || !strings.Contains(rec.Body.String(), "# TYPE") {
		t.Fatalf("metrics = %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = c.do("GET", "/some/ui/route", "", nil)
	if rec.Code != 404 {
		t.Fatalf("UI without a bundle should 404, got %d", rec.Code)
	}
}
