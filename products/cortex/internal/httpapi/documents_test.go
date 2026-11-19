package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/events"
	"github.com/openova-io/openova/products/cortex/internal/ingest"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// The document of the fixture: readable by every caller, managed by group:hr-admins.
const docID = "00000000-0000-4000-8000-0000000000d1"

func (m *memStore) Source(_ context.Context, p []string, id string) (store.SourceAccess, error) {
	switch id {
	case "hr":
		return store.SourceAccess{Connector: "upload", Manager: slices.Contains(p, "group:hr-admins")}, nil
	case "repo":
		return store.SourceAccess{Connector: "git", Manager: true}, nil
	}
	return store.SourceAccess{}, store.ErrNotFound
}
func (m *memStore) Document(_ context.Context, p []string, id string) (store.DocumentAccess, error) {
	if id != docID {
		return store.DocumentAccess{}, store.ErrNotFound
	}
	return store.DocumentAccess{Document: store.Document{ID: docID, SourceID: "hr", Title: "Leave policy", URI: "/v1/documents/" + docID + "/content",
		State: "indexed", UpdatedAt: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}, ObjectKey: docID + "/s", Manager: slices.Contains(p, "group:hr-admins")}, nil
}
func (m *memStore) Enqueue(_ context.Context, j store.Job) (string, error) {
	m.queued = append(m.queued, j)
	return "job-1", nil
}
func (m *memStore) MayWatch(_ context.Context, _ []string, _, documentID string) (bool, error) {
	return documentID != "hidden", nil
}

// memBucket is the originals bucket in memory.
type memBucket map[string]string

func (m memBucket) Put(_ context.Context, key string, c io.Reader, _ int64, _ string) error {
	b, _ := io.ReadAll(c)
	m[key] = string(b)
	return nil
}
func (m memBucket) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	b, ok := m[key]
	if !ok {
		return nil, 0, ingest.ErrNoObject
	}
	return io.NopCloser(strings.NewReader(b)), int64(len(b)), nil
}
func (m memBucket) Delete(_ context.Context, key string) error { delete(m, key); return nil }

// form writes a multipart body: each pair is a field name and its value; a name that starts
// with "file" is a file part named file with that name's rest as its file name.
func form(t *testing.T, parts ...[2]string) (string, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for _, p := range parts {
		var err error
		var pw io.Writer
		if name, ok := strings.CutPrefix(p[0], "file"); ok {
			pw, err = w.CreateFormFile("file", name)
		} else {
			pw, err = w.CreateFormField(p[0])
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(pw, p[1])
	}
	_ = w.Close()
	return b.String(), w.FormDataContentType()
}

func (f *fixture) upload(t *testing.T, source, token, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/sources/"+source+"/documents", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	return w
}

func TestUploadRoute(t *testing.T) {
	f := newFixture(t)
	manager, staff := f.token(t, map[string]any{"groups": []string{"hr-admins"}}), f.token(t, nil)
	text := "# Pay\n\n" + marker + "\n"
	body, ct := form(t, [2]string{"reader", "user:alice"}, [2]string{"file" + marker + ".md", text}, [2]string{"reader", "group:board"})
	w := f.upload(t, "hr", manager, body, ct)
	var got map[string]string
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &got) != nil || got["jobId"] != "job-1" || len(got["documentId"]) != 36 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var it ingest.Item
	if len(f.store.queued) != 1 || json.Unmarshal(f.store.queued[0].Item, &it) != nil || f.objects[it.Object] != text ||
		!slices.Equal(it.Readers, []string{"user:alice", "group:board"}) || it.Title != marker+".md" || f.store.queued[0].DocumentID != got["documentId"] {
		t.Fatalf("queued %+v, item %+v, objects %v", f.store.queued, it, f.objects)
	}
	for what, c := range map[string]struct {
		source, token string
		parts         [][2]string
		status        int
		code          string
	}{
		"a reader of the source":       {"hr", staff, [][2]string{{"filea.txt", "x"}}, 403, "not-manager"},
		"a source that does not exist": {"finance", manager, [][2]string{{"filea.txt", "x"}}, 403, "not-manager"},
		"a git source":                 {"repo", manager, [][2]string{{"filea.txt", "x"}}, 409, "not-an-upload-source"},
		"a reader of another form":     {"hr", manager, [][2]string{{"reader", "role:admin"}, {"filea.txt", "x"}}, 400, "invalid-reader"},
		"a reader with a space":        {"hr", manager, [][2]string{{"reader", "user:a b"}, {"filea.txt", "x"}}, 400, "invalid-reader"},
		"no file":                      {"hr", manager, [][2]string{{"reader", "user:alice"}}, 400, "invalid-request"},
		"two files":                    {"hr", manager, [][2]string{{"filea.txt", "x"}, {"fileb.txt", "y"}}, 400, "invalid-request"},
		"another field":                {"hr", manager, [][2]string{{"path", "/etc"}, {"filea.txt", "x"}}, 400, "invalid-request"},
		"a file over the limit":        {"hr", manager, [][2]string{{"filea.txt", strings.Repeat("x", 65)}}, 413, "too-large"},
		"a body over the limit":        {"hr", manager, [][2]string{{"filea.txt", strings.Repeat("x", 2<<20)}}, 413, "too-large"},
		"a binary file":                {"hr", manager, [][2]string{{"filea.png", "\x89PNG\r\n\x1a\n\x00\x00"}}, 415, "type-not-allowed"},
	} {
		body, ct := form(t, c.parts...)
		if w := f.upload(t, c.source, c.token, body, ct); w.Code != c.status || !strings.Contains(w.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%s: %d %s, want %d %s", what, w.Code, w.Body, c.status, c.code)
		}
	}
	if w := f.upload(t, "hr", manager, "not multipart", "text/plain"); w.Code != 400 {
		t.Errorf("a body that is not multipart: %d", w.Code)
	}
	if len(f.store.queued) != 1 || len(f.objects) != 1 {
		t.Fatalf("a refused upload stored or queued something: %v %v", f.store.queued, f.objects)
	}
	if strings.Contains(f.log.String(), marker) || strings.Contains(f.log.String(), "alice") {
		t.Fatalf("the log holds the upload's name, text or readers:\n%s", f.log)
	}
}

func TestDocumentRoutes(t *testing.T) {
	f := newFixture(t)
	manager, staff := f.token(t, map[string]any{"groups": []string{"hr-admins"}}), f.token(t, nil)
	w := f.call(t, f.srv.Handler(), "GET", "/v1/documents/"+docID, staff, "")
	want := `{"id":"` + docID + `","sourceId":"hr","state":"indexed","title":"Leave policy","updatedAt":"2026-11-20T08:00:00Z","uri":"/v1/documents/` + docID + `/content"}`
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := f.call(t, f.srv.Handler(), "GET", "/v1/documents/"+docID+"/content", staff, ""); w.Code != 404 || !strings.Contains(w.Body.String(), "document-not-found") {
		t.Fatalf("content without its original: %d %s", w.Code, w.Body)
	}
	f.objects[docID+"/s"] = "Leave accrues monthly."
	w = f.call(t, f.srv.Handler(), "GET", "/v1/documents/"+docID+"/content", staff, "")
	if w.Code != 200 || w.Body.String() != "Leave accrues monthly." || w.Header().Get("Content-Type") != "application/octet-stream" ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Length") != "22" {
		t.Fatalf("content: %d %v %s", w.Code, w.Header(), w.Body)
	}
	for _, path := range []string{"/v1/documents/00000000-0000-4000-8000-0000000000ff", "/v1/documents/00000000-0000-4000-8000-0000000000ff/content"} {
		if w := f.call(t, f.srv.Handler(), "GET", path, staff, ""); w.Code != 404 || !strings.Contains(w.Body.String(), "document-not-found") {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := f.call(t, f.srv.Handler(), "DELETE", "/v1/documents/"+docID, staff, ""); w.Code != 403 || !strings.Contains(w.Body.String(), "not-manager") {
		t.Fatalf("delete by a reader: %d %s", w.Code, w.Body)
	}
	if w := f.call(t, f.srv.Handler(), "DELETE", "/v1/documents/00000000-0000-4000-8000-0000000000ff", manager, ""); w.Code != 404 {
		t.Fatalf("delete of a document that does not exist: %d %s", w.Code, w.Body)
	}
	if w := f.call(t, f.srv.Handler(), "DELETE", "/v1/documents/"+docID, manager, ""); w.Code != 202 || strings.TrimSpace(w.Body.String()) != `{"jobId":"job-1"}` {
		t.Fatalf("delete by a manager: %d %s", w.Code, w.Body)
	}
	if q := f.store.queued; len(q) != 1 || q[0].Kind != "delete" || q[0].SourceID != "hr" || q[0].DocumentID != docID || string(q[0].Item) != `{"object":"`+docID+`/s"}` {
		t.Fatalf("queued %+v", f.store.queued)
	}
}

// TestEventsStream reads the stream through a real server: a frame for each job the caller may
// see, none for one it may not, a comment line on the heartbeat, and the end after StreamFor.
func TestEventsStream(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.srv.Handler())
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+f.token(t, nil))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	lines := bufio.NewScanner(resp.Body)
	if !lines.Scan() || lines.Text() != ": connected" {
		t.Fatalf("first line %q", lines.Text())
	}
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	f.hub.Publish(events.Event{JobID: "j0", SourceID: "hr", DocumentID: "hidden", Kind: "index", State: "queued", At: at})
	f.hub.Publish(events.Event{JobID: "j1", SourceID: "hr", DocumentID: docID, Kind: "index", State: "done", At: at})
	var all []string
	for lines.Scan() {
		all = append(all, lines.Text())
	}
	text := strings.Join(all, "\n")
	frame := "event: job\ndata: {\"jobId\":\"j1\",\"sourceId\":\"hr\",\"documentId\":\"" + docID + "\",\"kind\":\"index\",\"state\":\"done\",\"at\":\"2026-11-20T08:00:00Z\"}\n"
	if !strings.Contains(text, frame) || strings.Contains(text, "j0") || !strings.Contains(text, ": keep-alive") {
		t.Fatalf("the stream:\n%s", text)
	}
}
