// Package retrycall holds the retry calls of a deployment: the source of the
// third clause of gate check FND-G02 (Zero touch), "the deployment record
// shows no retry call".
//
// Every route whose effect is "run this install step again" appends one entry
// (time, route, what was run again, who) to the Log of the deployment at the
// moment the engine accepts the call, and saves the record, so the list
// survives a restart of the engine. The routes, in internal/handler unless
// named otherwise:
//
//	POST /api/v1/deployments/{id}/phases/{phase}/retry   phase 0, phase 1, phase1-watch (retry.go)
//	POST /api/v1/deployments/{depId}/jobs/{jobId}/retry   any job, a cutover step included (jobs_retry.go)
//	POST /api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/{action}   reconcile, resume (reconcilers.go)
//	POST /api/v1/deployments/{id}/republish-dns          (sovereign_dns_republish.go)
//	POST /api/v1/deployments/{id}/network-registry       (internal/lifecycle, wired in cmd/api)
//	POST /api/v1/sovereign/cutover/start                 a run of the cutover chain by hand (cutover.go)
//
// The check reads the list on the Sovereign: GET /api/v1/deployments/{id}
// serves it as retryCalls. The list reaches the Sovereign inside the record
// the hand-over exports. The Sovereign's import keeps the union of the list
// it holds and the list that arrives, so no import drops an entry. A call the
// parent accepts after the hand-over fired is sent to the Sovereign through
// the same import route with OnlyParam, which merges the list into the record
// the Sovereign holds and changes nothing else. Delivery is durable: the parent
// counts the entries the Sovereign confirmed (Record.RetryCallsSent) and sends
// the list again until it confirms them all (Log.Deliver), after a restart of
// the engine too. The Sovereign appends its own calls to the same list.
package retrycall

import (
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// The route of each entry: the method and the route pattern the router registers.
const (
	RoutePhase           = "POST /api/v1/deployments/{id}/phases/{phase}/retry"
	RouteJob             = "POST /api/v1/deployments/{depId}/jobs/{jobId}/retry"
	RouteReconciler      = "POST /api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/{action}"
	RouteRepublishDNS    = "POST /api/v1/deployments/{id}/republish-dns"
	RouteNetworkRegistry = "POST /api/v1/deployments/{id}/network-registry"
	RouteCutoverStart    = "POST /api/v1/sovereign/cutover/start"
)

// OnlyParam is the query parameter of an import that carries the list for a
// record the Sovereign already holds: with OnlyParam=true the Sovereign
// merges the list and leaves every other field of that record as it is.
const OnlyParam = "retryCallsOnly"

// Log is the list of retry calls of one deployment, safe for concurrent use.
// The zero Log never recorded: List returns nil, which the record writes as
// null and the check reads as "not measured". Start makes it record, from an
// empty list.
type Log struct {
	mu      sync.Mutex
	calls   []store.RetryCall
	sent    int  // the entries the Sovereign confirmed it holds
	sending bool // a round of Deliver runs
}

// Start makes l record from an empty list; a list it holds stays.
func (l *Log) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls == nil {
		l.calls = []store.RetryCall{}
	}
}

// Add appends one entry at the current time and reports whether it did. An
// empty target is not a retry call (a reconciler suspend) and adds nothing.
// A Log that never recorded records from here: its list is then not empty, so
// the check fails on the entry and never passes.
func (l *Log) Add(route, target, by string) bool {
	if target == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, store.RetryCall{At: time.Now().UTC(), Route: route, Target: target, By: by})
	return true
}

// List returns a copy of the list: nil when l never recorded.
func (l *Log) List() []store.RetryCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// Set replaces the list and the count of entries the Sovereign confirmed, as
// a record read back from the store holds them.
func (l *Log) Set(calls []store.RetryCall, sent int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls, l.sent = slices.Clone(calls), sent
}

// Sent returns how many entries the Sovereign confirmed it holds.
func (l *Log) Sent() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sent
}

// Deliver sends the list of the record id in st to its Sovereign (Forward)
// until the Sovereign confirms every entry, and calls save after each
// confirmation so the count survives a restart. post sends one body and
// reports whether the Sovereign took it (2xx) and, when not, whether to try
// again: the export budget ran out while the Sovereign was unreachable. One
// round runs at a time; a call recorded during a round is sent by the next.
// A 4xx, a record Forward does not send, or a wiped Sovereign end the rounds;
// what stays unconfirmed is sent by the next call or the next start.
func (l *Log) Deliver(st *store.Store, id string, post func(url string, body []byte) (took, again bool), save func()) {
	for l.begin() {
		url, body, n, ok := Forward(st, id)
		took, again := false, false
		if ok {
			took, again = post(url, body)
		}
		l.mu.Lock()
		if took {
			l.sent = max(l.sent, n)
		}
		l.sending = false
		l.mu.Unlock()
		if took {
			save()
		} else if !again {
			return
		}
	}
}

// begin starts a round of Deliver: the list holds an entry the Sovereign has
// not confirmed and no other round runs.
func (l *Log) begin() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sending || l.sent >= len(l.calls) {
		return false
	}
	l.sending = true
	return true
}

// Merge adds to l the entries of rec's list that l lacks, sets rec's list to
// the union, and reports whether the import r asks for the list only
// (OnlyParam=true).
func (l *Log) Merge(r *http.Request, rec *store.Record) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = Union(l.calls, rec.RetryCalls)
	rec.RetryCalls = slices.Clone(l.calls)
	return r.URL.Query().Get(OnlyParam) == "true"
}

// Union returns the entries of held and incoming, each once, oldest first. It
// is nil only when both are nil: a list never recorded stays not recorded.
func Union(held, incoming []store.RetryCall) []store.RetryCall {
	if held == nil && incoming == nil {
		return nil
	}
	out := []store.RetryCall{}
	seen := map[string]bool{}
	for _, c := range slices.Concat(held, incoming) {
		key := strings.Join([]string{c.At.UTC().Format(time.RFC3339Nano), c.Route, c.Target, c.By}, "\x00")
		if !seen[key] {
			seen[key] = true
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b store.RetryCall) int { return a.At.Compare(b.At) })
	return out
}

// Caller names who made the call r: the email of the session, else its
// subject, else the X-User-Email header, else "operator".
func Caller(r *http.Request) string {
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if e := strings.TrimSpace(claims.Email); e != "" {
			return e
		}
		if s := strings.TrimSpace(claims.Sub); s != "" {
			return s
		}
	}
	if e := strings.TrimSpace(r.Header.Get("X-User-Email")); e != "" {
		return e
	}
	return "operator"
}

// ReconcilerTarget names what a reconciler action runs again: reconcile and
// resume run the reconciler's step again; suspend runs nothing, so "".
func ReconcilerTarget(action, kind, ns, name string) string {
	if action != "reconcile" && action != "resume" {
		return ""
	}
	return action + " " + kind + " " + ns + "/" + name
}

// CutoverTarget names a run of the cutover chain by hand.
func CutoverTarget(remirror bool) string {
	if remirror {
		return "cutover chain with remirror"
	}
	return "cutover chain"
}

// ChildImportURL is the Sovereign's import route. It is a variable so a test
// can point it at a stub.
var ChildImportURL = func(fqdn string) string {
	return "https://api." + fqdn + "/api/v1/internal/deployments/import"
}

// Forward returns what the parent sends the Sovereign after it saved a retry
// call in the record id of st: the import URL with OnlyParam, the record as
// JSON and the number of entries the record lists. ok is false when the
// record cannot be read, when the hand-over has not fired (the hand-over
// itself then carries the list), when the record names no Sovereign, when the
// Sovereign is wiped or being wiped, or when this engine is that Sovereign
// (SOVEREIGN_FQDN, the variable a Sovereign's catalyst-api runs with).
func Forward(st *store.Store, id string) (url string, body []byte, n int, ok bool) {
	if st == nil {
		return "", nil, 0, false
	}
	rec, err := st.Load(id)
	if err != nil {
		return "", nil, 0, false
	}
	fqdn := strings.TrimSpace(rec.Request.SovereignFQDN)
	if rec.Result == nil || rec.Result.HandoverFiredAt == nil || fqdn == "" ||
		slices.Contains([]string{"wiping", "wiped", "aborted"}, rec.Status) ||
		fqdn == strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) {
		return "", nil, 0, false
	}
	if body, err = json.Marshal(rec); err != nil {
		return "", nil, 0, false
	}
	return ChildImportURL(fqdn) + "?" + OnlyParam + "=true", body, len(rec.RetryCalls), true
}

// Client is the HTTP client of a forward, the client of the hand-over
// exports: the Sovereign's certificate may still be the one of its first
// minutes.
func Client() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // same reason as the hand-over exports: the child's certificate may lag the hand-over
		},
	}
}

// Attempts logs the attempts of the forward of the record id, in the shape
// the hand-over export retry calls back with.
func Attempts(log *slog.Logger, id string) func(attempt int, kind string, status int, err error) {
	return func(attempt int, kind string, status int, err error) {
		if log == nil {
			return
		}
		if kind == "ok" {
			log.Info("retry calls: sent to the Sovereign", "id", id, "attempt", attempt)
			return
		}
		log.Warn("retry calls: send to the Sovereign", "id", id, "attempt", attempt, "outcome", kind, "status", status, "err", err)
	}
}
