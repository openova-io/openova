package suite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/storefront"
)

// onboardWorld answers as the hosts of sov.test do for the onboarding journey,
// as the mailbox does, and as kubectl and its port forward do in the regions
// a and b. The Organization lives in region a. Every request is kept as
// "<host> <method> <path>", with " session" when it carried the console cookie.
type onboardWorld struct {
	t  *testing.T
	mu sync.Mutex

	env                     map[string]string
	plans, apps, checkout   string
	issueStatus, appStatus  int
	mailCode, token, role   string
	previewCredit           int
	orgReady                string
	quota, keepOrg          bool
	doc, quote              string
	quoteStatus             int
	seen, mailed            []string
	issued, orgReq, provReq map[string]any
	orgSlug                 string
	deleted, revoked        bool

	// The Sovereign's own mailbox: its Secret per region, the reads of that
	// Secret, the forwards started and ended, the mailboxes opened.
	secrets                     map[string]string
	kubeCalls, forwards, opened []string
	stops                       int
	forwardErr                  error
}

const sovPhrase = "stand-in-7Hq2"

// sovSecret is the Secret of the Sovereign's gate mailbox as kubectl prints
// it, without the key drop and with the pairs of set.
func sovSecret(drop string, set ...string) string {
	kv := map[string]string{"address": "conformance@sov.test", "user": "conformance", "password": sovPhrase,
		"imap-host": "mail.sov.test", "imap-service": "bp-stalwart-sovereign-mail", "imap-port": "993"}
	delete(kv, drop)
	for i := 0; i+1 < len(set); i += 2 {
		kv[set[i]] = set[i+1]
	}
	for k, v := range kv {
		kv[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	b, _ := json.Marshal(map[string]any{"kind": "Secret", "data": kv})
	return string(b)
}

func (w *onboardWorld) forward(_ context.Context, r kube.Region, ns, svc string, port int) (string, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.forwards = append(w.forwards, fmt.Sprintf("%s %s %s %d", r.ID, ns, svc, port))
	return "127.0.0.1:40993", func() { w.mu.Lock(); w.stops++; w.mu.Unlock() }, w.forwardErr
}

func (w *onboardWorld) open(addr, serverName, user, password string) codeReader {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.opened = append(w.opened, strings.Join([]string{addr, serverName, user, password}, "|"))
	return w
}

const (
	onboardPlans = `[{"id":"sb","slug":"sandbox","product_slug":"sandbox","price_baisa":0},
	  {"id":"id-m","slug":"m","price_baisa":4490},{"id":"id-s","slug":"s","price_baisa":2490}]`
	onboardApps = `[{"id":"a-pg","slug":"postgres","system":true,"deployable":true},{"id":"a-wp","slug":"wordpress","deployable":true,"dependencies":["mysql"]},
	  {"id":"a-gh","slug":"ghost","deployable":true},{"id":"a-cs","slug":"comingsoon","deployable":false},{"id":"a-rd","slug":"redis","kind":"service","deployable":true}]`
	onboardCheckout = `{"order_id":"o-1","paid_by_credit":true,"amount_baisa":2490,"price_source":"bss:OpenOva plans@2026-11-19"}`
	onboardSession  = "console-session"
)

func newOnboardWorld(t *testing.T) *onboardWorld {
	return &onboardWorld{t: t, env: map[string]string{envMailboxIMAP: "imap.test:993", envMailboxUser: "gate", envMailboxPhrase: "pw",
		envMailboxAddress: "gate+{tag}@mail.test"}, plans: onboardPlans, apps: onboardApps, checkout: onboardCheckout,
		issueStatus: 200, appStatus: 200, mailCode: "123456", token: "customer-token", role: "member", previewCredit: 5,
		orgReady: "True", quota: true, doc: priceListDoc, quote: `{"error":"package-required"}`, quoteStatus: 409}
}

func (w *onboardWorld) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := r.URL.Host + " " + r.Method + " " + r.URL.Path
	if c, err := r.Cookie("catalyst_session"); err == nil && c.Value == onboardSession {
		line += " session"
	}
	w.seen = append(w.seen, line)
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	customer := r.Header.Get("Authorization") == "Bearer "+w.token
	out := func(status int, s string) {
		rw.WriteHeader(status)
		_, _ = io.WriteString(rw, s)
	}
	switch host, path := r.URL.Host, r.URL.Path; {
	case host == "console.sov.test" && path == "/api/v1/org/billing/vouchers/issue":
		w.issued = body
		out(w.issueStatus, `{"code":"VCH-ABCD2345","credit_omr":5}`)
	case host == "console.sov.test" && path == "/api/v1/org/billing/vouchers/revoke/VCH-ABCD2345":
		w.revoked = true
		out(404, `{"error":"voucher not valid"}`)
	case host == "chargeback.sov.test" && path == "/api/v1/public/packages":
		rw.Header().Set("Access-Control-Allow-Origin", "https://marketplace.sov.test")
		out(200, w.doc)
	case strings.HasSuffix(host, ".omani.homes"):
		out(w.appStatus, "<html>ok</html>")
	case host != "marketplace.sov.test":
		out(418, "")
	case path == "/api/catalog/plans":
		out(200, w.plans)
	case path == "/api/catalog/apps":
		out(200, w.apps)
	case path == "/api/auth/magic-link":
		w.mailed = append(w.mailed, fmt.Sprint(body["email"]))
		out(200, `{"message":"check your email","expires_in_sec":600}`)
	case path == "/api/auth/verify" && body["code"] == "123456":
		out(200, fmt.Sprintf(`{"token":%q,"refresh_token":"r1","user":{"email":%q,"role":"member"}}`, w.token, body["email"]))
	case path == "/api/auth/verify":
		out(401, `{"error":"invalid code"}`)
	case path == "/api/billing/vouchers/redeem-preview":
		out(200, fmt.Sprintf(`{"code":"VCH-ABCD2345","credit_omr":%d}`, w.previewCredit))
	case path == "/api/billing/quote":
		out(w.quoteStatus, w.quote)
	case !customer:
		out(401, `{"error":"unauthorized"}`)
	case path == "/api/auth/me":
		out(200, fmt.Sprintf(`{"user":{"email":%q,"role":%q}}`, w.mailed[0], w.role))
	case path == "/api/tenant/orgs":
		w.orgReq, w.orgSlug = body, fmt.Sprint(body["slug"])
		out(200, fmt.Sprintf(`{"id":"t-1","slug":%q,"parent_domain":"omani.homes"}`, w.orgSlug))
	case path == "/api/billing/checkout":
		out(200, w.checkout)
	case path == "/api/provisioning/start":
		w.provReq = body
		out(200, `{"status":"pending"}`)
	case path == "/api/tenant/orgs/t-1" && r.Method == http.MethodDelete:
		w.deleted = true
		out(200, `{"status":"deleted"}`)
	default:
		out(418, "")
	}
}

func (w *onboardWorld) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	return rec.Result(), nil
}

// Code is the gate lane's mailbox: the code reaches an address once the
// storefront was asked to mail it.
func (w *onboardWorld) Code(_ context.Context, address string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, m := range w.mailed {
		if m == address && w.mailCode != "" {
			return w.mailCode, nil
		}
	}
	return "", storefront.ErrNoMail
}

func (w *onboardWorld) kubectl(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	there := r.ID == "a" && w.orgSlug != "" && (!w.deleted || w.keepOrg)
	switch args[1] {
	case "secret":
		w.kubeCalls = append(w.kubeCalls, r.ID+" "+strings.Join(args, " "))
		return []byte(w.secrets[r.ID]), nil
	case "organizations.orgs.openova.io":
		if !there {
			return []byte(`{"items":[]}`), nil
		}
		return []byte(fmt.Sprintf(`{"items":[{"metadata":{"name":"org-x"},"spec":{"slug":%q},"status":{"conditions":[{"type":"Ready","status":%q,"reason":"R"}]}}]}`,
			w.orgSlug, w.orgReady)), nil
	case "resourcequota":
		if there && w.quota {
			return []byte("resourcequota/plan-quota\n"), nil
		}
	case "httproutes.gateway.networking.k8s.io":
		if there {
			return []byte(fmt.Sprintf(`{"items":[{"spec":{"hostnames":["console.%s.omani.homes","ghost.%s.omani.homes"]}}]}`, w.orgSlug, w.orgSlug)), nil
		}
		return []byte(`{"items":[]}`), nil
	case "namespace":
		if there {
			return []byte("namespace/" + w.orgSlug + "\n"), nil
		}
	}
	return nil, nil
}

// onboardRun runs BSS-G01 against w with short waits, then the end of the run.
func onboardRun(t *testing.T, w *onboardWorld) *runner {
	t.Helper()
	o := onboarding{getenv: func(k string) string { return w.env[k] },
		mailbox:   w.open,
		kubectl:   w.kubectl,
		forward:   w.forward,
		transport: w,
		waits:     onboardWaits{mail: 60 * time.Millisecond, ready: 60 * time.Millisecond, price: 60 * time.Millisecond, gone: 60 * time.Millisecond, every: 5 * time.Millisecond},
		tag:       func() string { return "TAG1" }}
	console := api.New("https://console.sov.test", onboardSession)
	console.HTTP.Transport = w
	cc := &CheckContext{RunIn: RunInCloud, API: console, Regions: []kube.Region{{ID: "a"}, {ID: "b"}}}
	r := &runner{out: io.Discard, run: &Run{Mode: "run", Steps: []*Step{priceListD01()}}}
	m := &Manifest{Checks: []ManifestCheck{entry(onboardID, StateExpectedFail)}}
	r.runChecks(context.Background(), m, []Check{onboardingCheck{o: o}}, cc, true)
	r.runAtEnd(context.Background())
	return r
}

// onboardSteps is "<id> <status>" for every step of the check.
func onboardSteps(r *runner) []string {
	out := []string{}
	for _, s := range r.run.Steps {
		if s.Journey == onboardID {
			out = append(out, s.ID+" "+s.Status)
		}
	}
	return out
}

func onboardStep(r *runner, id string) *Step {
	for _, s := range r.run.Steps {
		if s.Journey == onboardID && s.ID == id {
			return s
		}
	}
	return nil
}

func TestOnboardingJourneyPasses(t *testing.T) {
	w := newOnboardWorld(t)
	r := onboardRun(t, w)
	if got := r.run.Checks[0]; got.Status != Pass {
		t.Fatalf("want PASS, got %s %q; steps %v", got.Status, got.Reason, onboardSteps(r))
	}
	want := "BSS-G01.1 PASS,BSS-G01.2 PASS,BSS-G01.3 PASS,BSS-G01.4 PASS,BSS-G01.5 PASS,BSS-G01.6 PASS,BSS-G01.7 PASS,BSS-G01.8 PASS,BSS-G01.9 PASS"
	if got := strings.Join(onboardSteps(r), ","); got != want {
		t.Errorf("steps:\n got %s\nwant %s", got, want)
	}
	if ev := onboardStep(r, "BSS-G01.8").Evidence; ev[onboardPriceKey] != "published" || ev["order_id"] != "o-1" || ev["price_source"] != "bss:OpenOva plans@2026-11-19" {
		t.Errorf("step 8 evidence: %v", ev)
	}
	if ev := onboardStep(r, "BSS-G01.6").Evidence; ev["region"] != "a" || ev["app_host"] != "ghost.g01-tag1.omani.homes" {
		t.Errorf("step 6 evidence: %v", ev)
	}
	if w.issued["credit_omr"] != float64(5) || w.issued["max_redemptions"] != float64(1) || w.issued["plan_tier"] != "s" {
		t.Errorf("voucher issued: %v", w.issued)
	}
	if fmt.Sprint(w.orgReq["apps"]) != "[a-gh]" || w.orgReq["package_sku"] != "plan.s" || w.orgReq["plan_id"] != "id-s" || w.orgReq["slug"] != "g01-tag1" || w.orgReq["defer_launch"] != true {
		t.Errorf("organization request: %v", w.orgReq)
	}
	if w.provReq["order_id"] != "o-1" || w.provReq["subdomain"] != "g01-tag1" {
		t.Errorf("provisioning request: %v", w.provReq)
	}
	if len(w.mailed) != 1 || w.mailed[0] != "gate+tag1@mail.test" {
		t.Errorf("sign-in codes asked for %v, want one for gate+tag1@mail.test", w.mailed)
	}
	if strings.Join(w.opened, ",") != "imap.test:993||gate|pw" || len(w.forwards) != 0 || len(w.kubeCalls) != 0 {
		t.Errorf("the gate lane's mailbox: opened %v, forwards %v, Secret reads %v", w.opened, w.forwards, w.kubeCalls)
	}
	if !w.deleted || !w.revoked {
		t.Errorf("clean-up: deleted=%t revoked=%t", w.deleted, w.revoked)
	}
	for _, s := range w.seen {
		console := strings.HasPrefix(s, "console.sov.test ")
		if console != strings.HasSuffix(s, " session") {
			t.Errorf("request %q: the console session goes to the console and nowhere else", s)
		}
		if console && !strings.Contains(s, "/vouchers/") {
			t.Errorf("console request %q: the journey makes no console call but the voucher", s)
		}
	}
}

func TestOnboardingJourneyFails(t *testing.T) {
	for _, c := range []struct {
		name, step, fail string
		set              func(w *onboardWorld)
	}{
		{"a half-set mailbox of the gate lane", "BSS-G01.1", "BSS_GATE_MAILBOX_IMAP, BSS_GATE_MAILBOX_PASSWORD not set", func(w *onboardWorld) {
			delete(w.env, envMailboxIMAP)
			delete(w.env, envMailboxPhrase)
			w.secrets = map[string]string{"a": sovSecret("")}
		}},
		{"no mailbox", "BSS-G01.1", "no region (a, b) holds the Secret platform/stalwart-sovereign-conformance", func(w *onboardWorld) { w.env = map[string]string{} }},
		{"the Sovereign's Secret holds no password", "BSS-G01.1", "[b] the Secret platform/stalwart-sovereign-conformance holds no password", func(w *onboardWorld) {
			w.env, w.secrets = map[string]string{}, map[string]string{"b": sovSecret("password")}
		}},
		{"the Sovereign's Secret names a sub-address", "BSS-G01.1", "the key address of the Secret platform/stalwart-sovereign-conformance is not one plain address", func(w *onboardWorld) {
			w.env, w.secrets = map[string]string{}, map[string]string{"a": sovSecret("", "address", "conformance+x@sov.test")}
		}},
		{"the Sovereign's Secret names no port", "BSS-G01.1", "the key imap-port of the Secret platform/stalwart-sovereign-conformance is not a port", func(w *onboardWorld) {
			w.env, w.secrets = map[string]string{}, map[string]string{"a": sovSecret("", "imap-port", "imaps")}
		}},
		{"the port forward fails", "BSS-G01.4", "the sign-in code to conformance+tag1@sov.test: timed out after 60ms; last observed: [a] kubectl port-forward refused", func(w *onboardWorld) {
			w.env, w.secrets, w.forwardErr = map[string]string{}, map[string]string{"a": sovSecret("")}, errors.New("[a] kubectl port-forward refused")
		}},
		{"an address without {tag}", "BSS-G01.1", "holds {tag} once", func(w *onboardWorld) { w.env[envMailboxAddress] = "gate@mail.test" }},
		{"no priced plan", "BSS-G01.2", "lists no plan without a product and with a price", func(w *onboardWorld) {
			w.plans = `[{"id":"id-free","slug":"free","price_baisa":0},{"id":"id-p","slug":"p","product_slug":"x","price_baisa":900}]`
		}},
		{"no application", "BSS-G01.2", "lists no deployable application", func(w *onboardWorld) {
			w.apps = `[{"id":"a-cs","slug":"comingsoon","deployable":false}]`
		}},
		{"the voucher is refused", "BSS-G01.3", "HTTP 403", func(w *onboardWorld) { w.issueStatus = 403 }},
		{"no mail", "BSS-G01.4", "the sign-in code to gate+tag1@mail.test: timed out", func(w *onboardWorld) { w.mailCode = "" }},
		{"a wrong code", "BSS-G01.4", "POST /auth/verify: HTTP 401", func(w *onboardWorld) { w.mailCode = "654321" }},
		{"the customer is a sovereign-admin", "BSS-G01.4", `has the role "sovereign-admin"`, func(w *onboardWorld) { w.role = "sovereign-admin" }},
		{"the preview shows another credit", "BSS-G01.5", "a credit of 3 OMR, issued 5", func(w *onboardWorld) { w.previewCredit = 3 }},
		{"a card payment", "BSS-G01.5", "a card payment session", func(w *onboardWorld) {
			w.checkout = `{"session_url":"https://pay.test/x","order_id":"o-1"}`
		}},
		{"not paid from credit", "BSS-G01.5", "not paid from credit", func(w *onboardWorld) { w.checkout = `{"order_id":"o-1"}` }},
		{"the Organization is not Ready", "BSS-G01.6", `a: Organization Ready="False" R`, func(w *onboardWorld) { w.orgReady = "False" }},
		{"no plan-quota", "BSS-G01.6", "no ResourceQuota plan-quota in g01-tag1", func(w *onboardWorld) { w.quota = false }},
		{"the application does not serve", "BSS-G01.6", "https://ghost.g01-tag1.omani.homes/ answered 503", func(w *onboardWorld) { w.appStatus = 503 }},
		{"the customer holds the console session", "BSS-G01.7", "is the console session of the sovereign-admin", func(w *onboardWorld) { w.token = onboardSession }},
		{"the quote is priced from another list", "BSS-G01.8", "the quote of plan id-s is 3000 baisa and the price book shows 2.490 for plan.s", func(w *onboardWorld) {
			w.quote, w.quoteStatus = `{"amount_baisa":3000}`, 200
		}},
		{"the Organization stays", "BSS-G01.9", "the Organization g01-tag1 is not gone", func(w *onboardWorld) { w.keepOrg = true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOnboardWorld(t)
			c.set(w)
			r := onboardRun(t, w)
			got := r.run.Checks[0]
			failed := []string{}
			for _, s := range r.run.Steps {
				if s.Status == Fail {
					failed = append(failed, s.ID)
				}
			}
			if got.Status != Fail || !strings.Contains(got.Reason, c.fail) || len(failed) != 1 || failed[0] != c.step {
				t.Errorf("want FAIL at %s with %q, got %s %q, failed steps %v", c.step, c.fail, got.Status, got.Reason, failed)
			}
		})
	}
}

func TestOnboardingJourneySendsNothingWithoutAMailbox(t *testing.T) {
	w := newOnboardWorld(t)
	w.env = map[string]string{}
	r := onboardRun(t, w)
	read := "get secret stalwart-sovereign-conformance -n platform --ignore-not-found -o json"
	if len(w.seen) != 0 || len(onboardSteps(r)) != 1 || len(w.forwards) != 0 || strings.Join(w.kubeCalls, ",") != "a "+read+",b "+read {
		t.Errorf("want one failed step, no request or forward, and the two reads of the Secret; got steps %v, requests %v, forwards %v, reads %v",
			onboardSteps(r), w.seen, w.forwards, w.kubeCalls)
	}
}

// With no BSS_GATE_MAILBOX_* the journey reads the Secret from the region that
// holds it, signs in a sub-address of its one address, and reads the code
// through a port forward of that region, the certificate checked against the
// server's name; the password is in no record.
func TestOnboardingJourneyReadsTheSovereignsMailbox(t *testing.T) {
	w := newOnboardWorld(t)
	w.env, w.secrets = map[string]string{}, map[string]string{"b": sovSecret("")}
	r := onboardRun(t, w)
	if got := r.run.Checks[0]; got.Status != Pass {
		t.Fatalf("want PASS, got %s %q; steps %v", got.Status, got.Reason, onboardSteps(r))
	}
	if strings.Join(w.mailed, ",") != "conformance+tag1@sov.test" || len(w.forwards) == 0 || w.stops != len(w.forwards) || len(w.opened) != len(w.forwards) {
		t.Errorf("codes asked for %v, forwards %v (%d ended), mailboxes opened %v", w.mailed, w.forwards, w.stops, w.opened)
	}
	for i := range w.forwards {
		if w.forwards[i] != "b platform bp-stalwart-sovereign-mail 993" || w.opened[i] != "127.0.0.1:40993|mail.sov.test|conformance|"+sovPhrase {
			t.Errorf("read %d: forward %q and mailbox %q, want the Service and port of the Secret in region b, the server's name and its login", i, w.forwards[i], w.opened[i])
		}
	}
	ev := onboardStep(r, "BSS-G01.1").Evidence
	all, _ := json.Marshal(r.run)
	if ev["mailbox"] != "sovereign" || ev["mailbox_region"] != "b" || ev["address"] != "conformance+tag1@sov.test" ||
		strings.Contains(string(all), sovPhrase) || strings.Contains(string(all), base64.StdEncoding.EncodeToString([]byte(sovPhrase))) {
		t.Errorf("step 1 evidence %v, or the record of the run holds the password", ev)
	}
}

func TestOnboardingPriceListNotSeen(t *testing.T) {
	w := newOnboardWorld(t)
	w.doc = `{"packages":[{"sku":"plan.m","price_month":"4.490"}]}`
	r := onboardRun(t, w)
	ev := onboardStep(r, "BSS-G01.8").Evidence
	if got := r.run.Checks[0]; got.Status != Pass || ev[onboardPriceKey] != "not-seen" {
		t.Errorf("want PASS with %s not-seen, got %s %q %v", onboardPriceKey, got.Status, got.Reason, ev[onboardPriceKey])
	}
}

func TestOnboardedOrgIsMemoised(t *testing.T) {
	w := newOnboardWorld(t)
	o := onboarding{getenv: func(k string) string { return w.env[k] }, mailbox: w.open,
		kubectl: w.kubectl, forward: w.forward, transport: w, waits: onboardWaits{mail: time.Second, ready: time.Second, every: time.Millisecond},
		tag: func() string { return "m1" }}
	console := api.New("https://console.sov.test", onboardSession)
	console.HTTP.Transport = w
	cc := &CheckContext{API: console, Regions: []kube.Region{{ID: "a"}}, Prior: []*Step{priceListD01()}}
	rec := &runner{out: io.Discard, run: &Run{}}
	first, ran, err := o.onboarded(context.Background(), cc, checkRecorder{r: rec, id: onboardID})
	if err != nil || !ran || first.OrgSlug != "g01-m1" || first.Token != "customer-token" {
		t.Fatalf("first call: %+v ran=%t %v", first, ran, err)
	}
	asked := len(w.seen)
	again, ran, err := o.onboarded(context.Background(), cc, checkRecorder{r: rec, id: "BSS-G04"})
	if err != nil || ran || again != first || len(w.seen) != asked {
		t.Errorf("second call: ran=%t err=%v, %d new requests, same result %t", ran, err, len(w.seen)-asked, again == first)
	}
	// A later check of the billing area calls onboardedOrg and gets the same.
	if later, err := onboardedOrg(context.Background(), cc, checkRecorder{r: rec, id: "BSS-G05"}); err != nil || later != first || len(w.seen) != asked {
		t.Errorf("onboardedOrg: err=%v, %d new requests, same result %t", err, len(w.seen)-asked, later == first)
	}
	// Another run journeys again, and a failed journey is stored as failed.
	w.env = map[string]string{}
	cc2 := &CheckContext{API: console, Regions: []kube.Region{{ID: "a"}}, Prior: []*Step{priceListD01()}}
	_, _, err1 := o.onboarded(context.Background(), cc2, checkRecorder{r: rec, id: onboardID})
	_, ran, err2 := o.onboarded(context.Background(), cc2, checkRecorder{r: rec, id: "BSS-G04"})
	if err1 == nil || ran || err2 == nil || err2.Error() != err1.Error() {
		t.Errorf("a failed journey: %v then ran=%t %v", err1, ran, err2)
	}
	atEnd = nil
}

func TestAtEndRunsInReverseAndFailsItsCheck(t *testing.T) {
	r := &runner{out: io.Discard, run: &Run{Checks: []CheckResult{{ID: "X-G01", Status: Pass}, {ID: "X-G02", Status: Pass}}}}
	order := []string{}
	AtEnd("X-G01", func(_ context.Context, rec StepRecorder) {
		order = append(order, "first")
		rec.Step("X-G01.9", "clean", func(Evidence) error { return fmt.Errorf("left behind") })
	})
	AtEnd("X-G02", func(_ context.Context, rec StepRecorder) {
		order = append(order, "second")
		rec.Step("X-G02.9", "clean", func(Evidence) error { return nil })
	})
	r.runAtEnd(context.Background())
	if strings.Join(order, ",") != "second,first" || len(atEnd) != 0 {
		t.Errorf("order %v, %d hooks left", order, len(atEnd))
	}
	if c := r.run.Checks; c[0].Status != Fail || c[0].Reason != "left behind" || c[1].Status != Pass {
		t.Errorf("checks after the hooks: %+v", c)
	}
}

func TestOnboardingCheckIsRegistered(t *testing.T) {
	found := false
	for _, c := range RegisteredChecks() {
		found = found || c.ID() == onboardID
	}
	if !found {
		t.Errorf("%s is not registered", onboardID)
	}
}
