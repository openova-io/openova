package meter

// Tests of streamed answers. The tests that need a real connection serve the meter with httptest.

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	chunkRole   = "data: {\"object\":\"chat.completion.chunk\",\"model\":\"sim-default\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"
	chunkText   = "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"
	chunkUsage  = "data: {\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":" + usageWithCache + "}\n\n"
	chunkDone   = "data: [DONE]\n\n"
	streamBody  = chunkRole + chunkText + chunkUsage + chunkDone
	streamNoUse = chunkRole + chunkText + chunkDone
	askStream   = `{"model":"agent-default","stream":true,"messages":[{"role":"user","content":"<b>hi</b> & bye"}]}`
	askUsage    = `{"model":"agent-default","stream":true,"stream_options":{"include_usage":true},"messages":[]}`
)

// The measures of usageWithCache.
var streamMeasures = []Measure{{MeterInput, 304}, {MeterCacheRead, 1536}, {MeterOutput, 212}, {MeterRequests, 1}}

// feed writes s to a parser one byte at a time and returns what it forwards and the usage.
func feed(p StreamParser, s string) (string, Usage) {
	var out []byte
	for i := 0; i < len(s); i++ {
		out = append(out, p.Write([]byte{s[i]})...)
	}
	rest, u := p.Close()
	return string(append(out, rest...)), u
}

func TestStreamParserReadsUsageAndToolCalls(t *testing.T) {
	tool1 := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"a\"}}]}}]}\n\n"
	args := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]}}]}\n\n"
	tool2 := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_2\",\"function\":{\"name\":\"b\"}}]}}]}\n\n"
	both := "data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n"
	in := ": comment\n\n" + chunkRole + tool1 + args + tool2 + both + chunkUsage + chunkDone
	crlf := strings.ReplaceAll(in, "\n", "\r\n")
	want := Usage{PromptTokens: 1840, CachedTokens: 1536, CompletionTokens: 212, ToolCalls: 2, ResponseModel: "sim-default", Complete: true}
	for _, tc := range []struct {
		name, in, out string
		drop          bool
	}{
		{"kept", in, in, false},
		{"dropped", in, strings.Replace(in, chunkUsage, "", 1), true},
		{"dropped, CRLF lines", crlf, strings.Replace(crlf, strings.ReplaceAll(chunkUsage, "\n", "\r\n"), "", 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewStreamParser(tc.drop)
			out, u := feed(p, tc.in)
			if out != tc.out || u != want || !p.(*sseParser).done {
				t.Fatalf("forwarded %q, usage %+v; want %q, %+v and done", out, u, tc.out, want)
			}
		})
	}
}

func TestStreamParserLines(t *testing.T) {
	p := &sseParser{drop: true}
	if out := p.Write([]byte(chunkText + "data: [DO")); string(out) != chunkText {
		t.Fatalf("forwarded %q, want the ended lines only", out)
	}
	if rest, _ := p.Close(); string(rest) != "data: [DO" || p.done {
		t.Fatalf("Close gave %q, want the held line", rest)
	}
	// A line too long to read is forwarded as it arrives.
	p = &sseParser{drop: true}
	n := len(p.Write([]byte("data: "))) + len(p.Write(bytes.Repeat([]byte("x"), maxStreamLine+1)))
	if n != maxStreamLine+7 || len(p.Write([]byte("x\n\n"+chunkDone))) != 3+len(chunkDone) || !p.done {
		t.Fatalf("forwarded %d bytes of a long line", n)
	}
}

func TestWithStreamUsage(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		drop     bool
	}{
		{`not json`, `not json`, true},
		{`{"model":"a","stream":false}`, `{"model":"a","stream":false}`, true},
		{askStream, `{"messages":[{"role":"user","content":"<b>hi</b> & bye"}],"model":"agent-default","stream":true,"stream_options":{"include_usage":true}}`, true},
		{`{"stream":true,"stream_options":{"x":1}}`, `{"stream":true,"stream_options":{"include_usage":true,"x":1}}`, true},
		{`{"stream":true,"stream_options":{"include_usage":false}}`, `{"stream":true,"stream_options":{"include_usage":true}}`, true},
		{`{"stream":true,"stream_options":null}`, `{"stream":true,"stream_options":{"include_usage":true}}`, true},
		{askUsage, askUsage, false},
	} {
		if got, drop := withStreamUsage([]byte(tc.in)); string(got) != tc.want || drop != tc.drop {
			t.Errorf("withStreamUsage(%s) = %s, %v; want %s, %v", tc.in, got, drop, tc.want, tc.drop)
		}
	}
}

// streamRig is a back end that answers with a stream written by serve, and the meter in front.
type streamRig struct {
	pub     *fakePublisher
	handler *Handler
	mu      sync.Mutex
	bodies  []string // the request bodies the back end received
}

func newStreamRig(t *testing.T, serve func(w http.ResponseWriter, r *http.Request)) *streamRig {
	t.Helper()
	rg := &streamRig{pub: &fakePublisher{}}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rg.mu.Lock()
		rg.bodies = append(rg.bodies, string(b))
		rg.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		serve(w, r)
	}))
	t.Cleanup(backend.Close)
	h, err := NewHandler(Config{Backends: map[string]Backend{"meter-simulation": {Name: "simulation", URL: backend.URL}},
		Publisher: rg.pub, Source: testSource, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	rg.handler = h
	return rg
}

func writes(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }
}

func (rg *streamRig) received() []string {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return append([]string(nil), rg.bodies...)
}

// do sends one request through the meter, called directly, and returns the status, the answer
// and the ring entry.
func (rg *streamRig) do(body string, length int64) (int, string, Record) {
	req := httptest.NewRequest(http.MethodPost, "http://meter-simulation/v1/chat/completions", strings.NewReader(body))
	req.ContentLength = length
	for k, vs := range identityHeaders() {
		req.Header[k] = vs
	}
	w := httptest.NewRecorder()
	rg.handler.ServeHTTP(w, req)
	rec, _ := rg.handler.Ring().Get("req-0000001")
	return w.Code, w.Body.String(), rec
}

func TestStreamedResponseIsMetered(t *testing.T) {
	for _, tc := range []struct {
		name, ask, backendAnswer, callerGets, backendGets string
		events                                            int
		status                                            string
	}{
		{"usage chunk dropped", askStream, streamBody, streamNoUse, `"stream_options":{"include_usage":true}}`, 1, StatusOK},
		{"usage chunk kept when asked", askUsage, streamBody, streamBody, askUsage, 1, StatusOK},
		{"no usage chunk", askStream, streamNoUse, streamNoUse, `"include_usage":true`, 0, StatusOK},
		{"ended by the back end before done", askStream, chunkRole + chunkUsage, chunkRole, `"include_usage":true`, 0, StatusAborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rg := newStreamRig(t, writes(tc.backendAnswer))
			status, got, rec := rg.do(tc.ask, int64(len(tc.ask)))
			if status != http.StatusOK || got != tc.callerGets {
				t.Fatalf("the caller got %d %q, want 200 %q", status, got, tc.callerGets)
			}
			if b := rg.received(); len(b) != 1 || !strings.Contains(b[0], tc.backendGets) {
				t.Fatalf("the back end got %v, want a body with %s", b, tc.backendGets)
			}
			evs := rg.pub.events()
			if len(evs) != tc.events || (tc.events == 1 && !reflect.DeepEqual(evs[0].Measures, streamMeasures)) {
				t.Fatalf("events = %+v, want %d with the measures %v", evs, tc.events, streamMeasures)
			}
			if !rec.Stream || rec.Status != tc.status || rec.Published != (tc.events == 1) {
				t.Fatalf("ring entry = %+v, want a stream with status %s", rec, tc.status)
			}
		})
	}
}

// serveMeter serves the meter on a real listener, sends a stream request and returns the
// answer once its first line has arrived. It fails when that line takes five seconds.
func serveMeter(t *testing.T, rg *streamRig) (*http.Response, *bufio.Reader) {
	t.Helper()
	srv := httptest.NewServer(rg.handler)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(askStream))
	req.Host = "meter-simulation"
	for k, vs := range identityHeaders() {
		req.Header[k] = vs
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() }) // runs before srv.Close, which waits for the request
	br := bufio.NewReader(resp.Body)
	got := make(chan string, 1)
	go func() { line, _ := br.ReadString('\n'); got <- line }()
	select {
	case line := <-got:
		if line+"\n" != chunkRole {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no chunk reached the caller while the back end was still streaming")
	}
	return resp, br
}

// blocking writes the first chunk, waits for release or for the end of the request, then
// writes the rest.
func blocking(release <-chan struct{}) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chunkRole)
		_ = http.NewResponseController(w).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, chunkText+chunkUsage+chunkDone)
		case <-r.Context().Done():
		}
	}
}

func TestStreamChunksAreForwardedBeforeTheBackEndFinishes(t *testing.T) {
	release := make(chan struct{})
	rg := newStreamRig(t, blocking(release))
	_, br := serveMeter(t, rg)
	close(release)
	if rest, err := io.ReadAll(br); err != nil || string(rest) != "\n"+chunkText+chunkDone {
		t.Fatalf("rest = %q, %v", rest, err)
	}
	if evs := rg.pub.events(); len(evs) != 1 || !reflect.DeepEqual(evs[0].Measures, streamMeasures) {
		t.Fatalf("events = %+v, want one", evs)
	}
}

func TestStreamClosedByTheCallerIsAborted(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rg := newStreamRig(t, blocking(release))
	resp, _ := serveMeter(t, rg)
	_ = resp.Body.Close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if rec, ok := rg.handler.Ring().Get("req-0000001"); ok {
			if rec.Status != StatusAborted || !rec.Stream || rec.Published || len(rg.pub.events()) != 0 {
				t.Fatalf("ring entry = %+v, want a stream, status aborted, nothing published", rec)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no ring entry five seconds after the caller closed the stream")
		}
	}
}

func TestRequestBodyAbove16MiBGets413(t *testing.T) {
	big := `{"model":"agent-default","stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("x", 20<<20) + `"}]}`
	for _, length := range []int64{int64(len(big)), -1} {
		rg := newStreamRig(t, writes(streamBody))
		status, got, rec := rg.do(big, length)
		if status != http.StatusRequestEntityTooLarge || !strings.Contains(got, `"code":"request_too_large"`) {
			t.Fatalf("length %d: response = %d %s, want 413 request_too_large", length, status, got)
		}
		if len(rg.received()) != 0 || len(rg.pub.events()) != 0 || rec.Status != StatusRejected || rec.HTTPStatus != 413 {
			t.Fatalf("length %d: forwarded or metered, or ring entry %+v is not rejected 413", length, rec)
		}
	}
}
