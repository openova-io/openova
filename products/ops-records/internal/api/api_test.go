package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// fixture holds the records of two Organizations, acme and globex.
type fixture struct {
	store                        *store.Memory
	a1, a2, a3, g1               string // acme: a1 and a2 open, a3 resolved; globex: g1 open
	s1, s2, s3, sg               string // s1 and s2 in a1, s3 in a2, sg in g1
	judgementAcme, judgementGlob string
	handler                      http.Handler
	hub                          *Hub
	woken                        int
}

var at = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{store: store.NewMemory(), a1: ids.NewIncident(), a2: ids.NewIncident(), a3: ids.NewIncident(),
		g1: ids.NewIncident(), s1: ids.New(), s2: ids.New(), s3: ids.New(), sg: ids.New(),
		judgementAcme: ids.New(), judgementGlob: ids.New(), hub: NewHub()}
	resolved := at.Add(time.Hour)
	err := f.store.InTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		for _, in := range []store.Incident{
			{ID: f.a1, Organization: "acme", Title: "web down", Severity: 3},
			{ID: f.a2, Organization: "acme", Title: "database slow", Severity: 2},
			{ID: f.a3, Organization: "acme", Title: "old", Severity: 4, State: store.IncidentResolved, ResolvedAt: &resolved},
			{ID: f.g1, Organization: "globex", Title: "globex down", Severity: 3},
		} {
			in.SeveritySetBy, in.OpenedBy, in.OpenedAt = store.ByRules, store.ByRules, at
			if in.State == "" {
				in.State = store.IncidentOpen
			}
			if err := tx.InsertIncident(ctx, in); err != nil {
				return err
			}
			if _, err := tx.AppendIncidentEvent(ctx, store.IncidentEvent{Organization: in.Organization, IncidentID: in.ID,
				At: at, ActorKind: store.ByRules, Actor: "correlate-rules", Kind: "opened"}); err != nil {
				return err
			}
		}
		for _, s := range []struct{ id, org, incident string }{{f.s1, "acme", f.a1}, {f.s2, "acme", f.a1},
			{f.s3, "acme", f.a2}, {f.sg, "globex", f.g1}} {
			if err := tx.InsertSignal(ctx, store.Signal{ID: s.id, Organization: s.org, Source: store.SourceAlert,
				Kind: "pod-crash-loop", EntityKind: "deployment", EntityRef: s.org + "/web", Cluster: "c1", SeverityFloor: 3,
				Title: "web restarts", Fingerprint: "alert/c1/" + s.id, State: store.SignalOpen, FirstSeen: at, LastSeen: at,
				IncidentID: s.incident}); err != nil {
				return err
			}
		}
		for org, id := range map[string]string{"acme": f.judgementAcme, "globex": f.judgementGlob} {
			if err := tx.InsertJudgement(ctx, store.Judgement{ID: id, Organization: org, Judge: "correlator",
				SubjectKind: "signal_batch", SubjectID: "cluster/c1", Outcome: store.OutcomeTimeout, AskedAt: at,
				DeadlineAt: at.Add(time.Minute)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Server{Store: f.store, Hub: f.hub, Now: func() time.Time { return at.Add(2 * time.Hour) },
		Wake: func() { f.woken++ }}).Register(mux)
	f.handler = mux
	return f
}

func person(org, tier string) map[string]string {
	return map[string]string{HeaderOrganization: org, HeaderUser: "ops@" + org + ".test", HeaderTier: tier}
}

func (f *fixture) do(t *testing.T, method, path string, headers map[string]string, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: the answer is not JSON: %q", method, path, rec.Body.String())
	}
	return rec.Code, out
}

func itemIDs(body map[string]any) []string {
	var out []string
	items, _ := body["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

func (f *fixture) events(t *testing.T, org, incident string) []store.IncidentEvent {
	t.Helper()
	ev, err := f.store.ListIncidentEvents(context.Background(), org, incident)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// fakeJWT is a token with the claims given and no valid signature.
func fakeJWT(claims string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return "Bearer " + enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(claims)) + ".c2ln"
}

func TestEveryRouteFiltersByOrganization(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		org, tier string
		incidents int
	}{{"acme", "viewer", 3}, {"globex", "viewer", 1}, {"", TierSovereignAdmin, 4}} {
		code, body := f.do(t, "GET", "/api/v1/ops/incidents", person(c.org, c.tier), "")
		if code != http.StatusOK || len(itemIDs(body)) != c.incidents {
			t.Errorf("list incidents as %q: %d %v", c.org, code, itemIDs(body))
		}
		for _, item := range body["items"].([]any) {
			if org := item.(map[string]any)["organization"]; c.org != "" && org != c.org {
				t.Errorf("list incidents as %q answered an incident of %v", c.org, org)
			}
		}
	}
	if code, body := f.do(t, "GET", "/api/v1/ops/incidents?state=resolved", person("acme", "viewer"), ""); code != http.StatusOK ||
		len(itemIDs(body)) != 1 || itemIDs(body)[0] != f.a3 {
		t.Errorf("list resolved incidents: %d %v", code, body)
	}
	if code, body := f.do(t, "GET", "/api/v1/ops/incidents/"+f.g1, person("acme", "admin"), ""); code != http.StatusNotFound ||
		body["error"] != "not_found" {
		t.Errorf("acme read globex's incident: %d %v", code, body)
	}
	code, body := f.do(t, "GET", "/api/v1/ops/incidents/"+f.g1, person("globex", "viewer"), "")
	signals, _ := body["signals"].([]any)
	events, _ := body["events"].([]any)
	if code != http.StatusOK || body["id"] != f.g1 || len(signals) != 1 || len(events) != 1 {
		t.Errorf("globex read its incident: %d %v", code, body)
	}
	if code, body := f.do(t, "GET", "/api/v1/ops/judgements", person("acme", "viewer"), ""); code != http.StatusOK ||
		strings.Join(itemIDs(body), ",") != f.judgementAcme {
		t.Errorf("acme's judgements: %d %v", code, itemIDs(body))
	}
	if code, body := f.do(t, "GET", "/api/v1/ops/judgements?outcome=undecided", person("acme", "viewer"), ""); code != http.StatusOK ||
		len(itemIDs(body)) != 0 {
		t.Errorf("acme's undecided judgements: %d %v", code, itemIDs(body))
	}
	if code, _ := f.do(t, "POST", "/api/v1/ops/incidents/"+f.g1+"/actions", person("acme", "owner"), `{"action":"resolve"}`); code != http.StatusNotFound {
		t.Errorf("acme acted on globex's incident: %d", code)
	}
	if in, _ := f.store.GetIncident(context.Background(), "globex", f.g1); in.State != store.IncidentOpen || len(f.events(t, "globex", f.g1)) != 1 {
		t.Errorf("globex's incident changed: %+v", in)
	}
}

func TestIdentityIsRequired(t *testing.T) {
	f := newFixture(t)
	for name, headers := range map[string]map[string]string{
		"no headers":                    nil,
		"no user":                       {HeaderOrganization: "acme", HeaderTier: "viewer"},
		"a tier off the ladder":         {HeaderOrganization: "acme", HeaderUser: "a@acme.test", HeaderTier: "root"},
		"every Organization, as admin":  person("", "admin"),
		"every Organization, as viewer": person("", "viewer"),
	} {
		if code, body := f.do(t, "GET", "/api/v1/ops/incidents", headers, ""); code != http.StatusUnauthorized || body["error"] != "missing_identity" {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
}

func TestAgentTokenIsRefused(t *testing.T) {
	f := newFixture(t)
	agent := person("acme", "owner")
	agent["Authorization"] = fakeJWT(`{"typ":"agent","org_id":"acme","agent":"correlator","tier":"developer"}`)
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/api/v1/ops/incidents/" + f.a1 + "/actions", `{"action":"resolve"}`},
		{"POST", "/api/v1/ops/incidents/" + f.a1 + "/actions", `{"action":"acknowledge"}`},
		{"GET", "/api/v1/ops/incidents", ""},
		{"GET", "/api/v1/ops/judgements", ""},
	} {
		if code, body := f.do(t, r.method, r.path, agent, r.body); code != http.StatusForbidden || body["error"] != "agent_token_forbidden" {
			t.Errorf("%s %s with an agent token: %d %v", r.method, r.path, code, body)
		}
	}
	if in, _ := f.store.GetIncident(context.Background(), "acme", f.a1); in.State != store.IncidentOpen || len(f.events(t, "acme", f.a1)) != 1 {
		t.Fatalf("an agent token changed the incident: %+v", in)
	}
	session := person("acme", "owner")
	session["Authorization"] = fakeJWT(`{"org_id":"acme","tier":"owner"}`)
	if code, _ := f.do(t, "POST", "/api/v1/ops/incidents/"+f.a1+"/actions", session, `{"action":"acknowledge"}`); code != http.StatusOK {
		t.Fatalf("a token that is not an agent token was refused: %d", code)
	}
}

func TestViewerCannotAct(t *testing.T) {
	f := newFixture(t)
	if code, body := f.do(t, "POST", "/api/v1/ops/incidents/"+f.a1+"/actions", person("acme", "viewer"), `{"action":"acknowledge"}`); code != http.StatusForbidden || body["error"] != "tier_too_low" {
		t.Fatalf("a viewer acted: %d %v", code, body)
	}
}

func TestActions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	act := func(id, body string) (int, map[string]any) {
		return f.do(t, "POST", "/api/v1/ops/incidents/"+id+"/actions", person("acme", "operator"), body)
	}
	last := func(id string) store.IncidentEvent { ev := f.events(t, "acme", id); return ev[len(ev)-1] }

	if code, body := act(f.a1, `{"action":"acknowledge"}`); code != http.StatusOK || body["id"] != f.a1 ||
		last(f.a1).Kind != EventAcknowledged || last(f.a1).ActorKind != store.ByPerson || last(f.a1).Actor != "ops@acme.test" {
		t.Fatalf("acknowledge: %d %v %+v", code, body, last(f.a1))
	}
	if code, body := act(f.a1, `{"action":"set_severity","severity":1}`); code != http.StatusOK || body["severity"] != 1.0 ||
		body["severitySetBy"] != store.ByPerson || last(f.a1).Kind != EventSeverity {
		t.Fatalf("set_severity: %d %v", code, body)
	}
	if code, _ := act(f.a1, `{"action":"detach","signalId":"`+f.s2+`"}`); code != http.StatusOK {
		t.Fatalf("detach: %d", code)
	}
	if s, _ := f.store.GetSignal(ctx, "acme", f.s2); s.IncidentID != "" || last(f.a1).Kind != EventDetached {
		t.Fatalf("detach left the signal in %q", s.IncidentID)
	}
	if code, body := act(f.a2, `{"action":"merge","otherIncidentId":"`+f.a1+`"}`); code != http.StatusOK || body["parentId"] != f.a1 {
		t.Fatalf("merge: %d %v", code, body)
	}
	if s, _ := f.store.GetSignal(ctx, "acme", f.s3); s.IncidentID != f.a1 || last(f.a1).Kind != EventAttached || last(f.a2).Kind != EventMerged {
		t.Fatalf("merge did not move the signal: %q", s.IncidentID)
	}
	if in, _ := f.store.GetIncident(ctx, "acme", f.a1); in.Severity != 1 {
		t.Fatalf("merge changed a severity a person set: %d", in.Severity)
	}
	fb, _ := f.store.ListFeedback(ctx, "acme", f.a1)
	fb2, _ := f.store.ListFeedback(ctx, "acme", f.a2)
	if len(fb) != 2 || fb[0].Kind != store.FeedbackSeverity || fb[1].Kind != store.FeedbackDetach ||
		len(fb2) != 1 || fb2[0].Kind != store.FeedbackMerge || fb[0].Person != "ops@acme.test" {
		t.Fatalf("feedback: %+v %+v", fb, fb2)
	}

	for name, c := range map[string]struct {
		id, body string
		code     int
	}{
		"an action on a merged incident":       {f.a2, `{"action":"acknowledge"}`, http.StatusConflict},
		"an action on a resolved incident":     {f.a3, `{"action":"acknowledge"}`, http.StatusConflict},
		"merge into itself":                    {f.a1, `{"action":"merge","otherIncidentId":"` + f.a2 + `"}`, http.StatusConflict},
		"merge into a resolved incident":       {f.a1, `{"action":"merge","otherIncidentId":"` + f.a3 + `"}`, http.StatusConflict},
		"detach a signal of another incident":  {f.a1, `{"action":"detach","signalId":"` + f.sg + `"}`, http.StatusNotFound},
		"detach a signal that is not attached": {f.a1, `{"action":"detach","signalId":"` + f.s2 + `"}`, http.StatusConflict},
		"a severity off the scale":             {f.a1, `{"action":"set_severity","severity":5}`, http.StatusBadRequest},
		"an unknown action":                    {f.a1, `{"action":"delete"}`, http.StatusBadRequest},
		"an unknown field":                     {f.a1, `{"action":"acknowledge","by":"x"}`, http.StatusBadRequest},
		"an unknown incident":                  {ids.NewIncident(), `{"action":"acknowledge"}`, http.StatusNotFound},
	} {
		if code, body := act(c.id, c.body); code != c.code || (code == http.StatusConflict && body["error"] != "invalid_state") {
			t.Errorf("%s: %d %v, want %d", name, code, body, c.code)
		}
	}
	if code, body := act(f.a1, `{"action":"resolve"}`); code != http.StatusOK || body["state"] != store.IncidentResolved ||
		body["resolvedAt"] == nil || last(f.a1).Kind != EventResolved {
		t.Fatalf("resolve: %d %v", code, body)
	}
	if f.woken != 5 {
		t.Fatalf("the decider was woken %d times for 5 actions", f.woken)
	}
}

func TestMergeMakesTheTargetAsUrgent(t *testing.T) {
	f := newFixture(t)
	if code, _ := f.do(t, "POST", "/api/v1/ops/incidents/"+f.a1+"/actions", person("acme", "developer"),
		`{"action":"merge","otherIncidentId":"`+f.a2+`"}`); code != http.StatusOK {
		t.Fatalf("merge: %d", code)
	}
	if in, _ := f.store.GetIncident(context.Background(), "acme", f.a2); in.Severity != 2 {
		t.Fatalf("a severity 2 target became %d", in.Severity)
	}
	// The other way: a2 (severity 2) merged into a1 (severity 3) makes a1 severity 2.
	f2 := newFixture(t)
	if code, _ := f2.do(t, "POST", "/api/v1/ops/incidents/"+f2.a2+"/actions", person("acme", "developer"),
		`{"action":"merge","otherIncidentId":"`+f2.a1+`"}`); code != http.StatusOK {
		t.Fatalf("merge: %d", code)
	}
	if in, _ := f2.store.GetIncident(context.Background(), "acme", f2.a1); in.Severity != 2 || in.SeveritySetBy != store.ByRules {
		t.Fatalf("the target of a more urgent incident: %+v", in)
	}
}

func TestStreamSendsOnlyTheCallersChanges(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.handler)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/ops/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range person("acme", "viewer") {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := bufio.NewReader(resp.Body)
	if first, _ := lines.ReadString('\n'); resp.Header.Get("Content-Type") != "text/event-stream" || first != ": ready\n" {
		t.Fatalf("stream start: %q %q", resp.Header.Get("Content-Type"), first)
	}
	f.do(t, "POST", "/api/v1/ops/incidents/"+f.g1+"/actions", person("globex", "admin"), `{"action":"acknowledge"}`)
	f.do(t, "POST", "/api/v1/ops/incidents/"+f.a1+"/actions", person("acme", "admin"), `{"action":"acknowledge"}`)
	var got []string
	for len(got) < 2 {
		line, err := lines.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line != "\n" && !strings.HasPrefix(line, ":") {
			got = append(got, strings.TrimSpace(line))
		}
	}
	want := []string{"event: incident", `data: {"organization":"acme","incidentId":"` + f.a1 + `","kind":"acknowledge"}`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stream of acme:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	f.hub.Close()
	if rest, err := io.ReadAll(lines); err != nil || strings.TrimSpace(string(rest)) != "" {
		t.Fatalf("the stream did not end when the hub closed: %q %v", rest, err)
	}
}
