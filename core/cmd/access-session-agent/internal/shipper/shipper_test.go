package shipper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const uid = "5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11"

// ingest is the controller's internal port: it answers each request with the
// next status of answers (the last one repeats) and records what arrived.
type ingest struct {
	mu      sync.Mutex
	answers []int
	calls   []call
}

type call struct{ method, path, sum, body string }

func (in *ingest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	in.mu.Lock()
	in.calls = append(in.calls, call{r.Method, r.URL.Path, r.Header.Get("X-Chunk-SHA256"), string(b)})
	status := in.answers[0]
	if len(in.answers) > 1 {
		in.answers = in.answers[1:]
	}
	in.mu.Unlock()
	w.WriteHeader(status)
}

func (in *ingest) seen() []call {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.calls)
}

func client(t *testing.T, answers ...int) (*Client, *ingest) {
	t.Helper()
	in := &ingest{answers: answers}
	h := httptest.NewServer(in)
	t.Cleanup(h.Close)
	return &Client{Base: h.URL, UID: uid, HTTP: h.Client(), Pause: 10 * time.Millisecond}, in
}

func TestReadyRetriesUntilTaken(t *testing.T) {
	c, in := client(t, 503, 503, 204)
	if err := c.Ready(context.Background(), "01:02:ab"); err != nil {
		t.Fatal(err)
	}
	want := call{"POST", "/internal/v1/sessions/" + uid + "/ready", "", `{"credentialID":"01:02:ab"}`}
	if got := in.seen(); len(got) != 3 || got[2] != want {
		t.Fatalf("got %+v, want 3 attempts, the last %+v", got, want)
	}
}

func TestRefusalIsNotRetried(t *testing.T) {
	c, in := client(t, 403)
	var refused *Refused
	if err := c.Closed(context.Background(), "user"); !errors.As(err, &refused) || refused.Status != 403 {
		t.Fatalf("got %v, want the refusal 403", err)
	}
	if n := len(in.seen()); n != 1 {
		t.Fatalf("a refusal was tried %d times", n)
	}
}

func TestChunkGivesUp(t *testing.T) {
	c, in := client(t, 503)
	start := time.Now()
	if err := c.Chunk(context.Background(), "screen-1", 1, []byte("x"), 200*time.Millisecond); err == nil {
		t.Fatal("a chunk the controller never took was delivered")
	}
	if took, n := time.Since(start), len(in.seen()); took < 200*time.Millisecond || n < 2 {
		t.Fatalf("gave up after %s and %d attempts", took, n)
	}
}

func recorder(c *Client, dir string, used float64) *Recorder {
	return &Recorder{Client: c, Dir: dir, ChunkBytes: 10, ChunkTime: 150 * time.Millisecond, Terminal: true,
		GiveUp: 300 * time.Millisecond, Settle: 50 * time.Millisecond, Used: func() (float64, error) { return used, nil }}
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// chunks are the chunks of one stream, in the order they arrived; each must
// carry the SHA-256 of its body.
func chunks(t *testing.T, calls []call, stream string) []string {
	t.Helper()
	prefix, out := "/internal/v1/sessions/"+uid+"/streams/"+stream+"/chunks/", []string{}
	for _, c := range calls {
		if seq, ok := strings.CutPrefix(c.path, prefix); ok {
			if sum := sha256.Sum256([]byte(c.body)); c.method != "PUT" || c.sum != hex.EncodeToString(sum[:]) {
				t.Fatalf("chunk %s of %s: %s with digest %s", seq, stream, c.method, c.sum)
			}
			if want := strconv.Itoa(len(out) + 1); seq != want {
				t.Fatalf("chunk %s of %s has the number %s", want, stream, seq)
			}
			out = append(out, c.body)
		}
	}
	return out
}

func TestRecorderSealsBySizeByTimeAndAtTheEnd(t *testing.T) {
	c, in := client(t, 204)
	dir := t.TempDir()
	failures := make(chan Failure, 8)
	conn, err := recorder(c, dir, 0.1).Follow(context.Background(), 1, func(f Failure) { failures <- f })
	if err != nil {
		t.Fatal(err)
	}
	appendTo(t, filepath.Join(dir, "screen-1"), "0123456789abcdefghijKLMNO")
	deadline := time.Now().Add(3 * time.Second)
	for len(chunks(t, in.seen(), "screen-1")) < 3 && time.Now().Before(deadline) {
		<-time.After(20 * time.Millisecond)
	}
	if got := chunks(t, in.seen(), "screen-1"); !slices.Equal(got, []string{"0123456789", "abcdefghij", "KLMNO"}) {
		t.Fatalf("before the end the screen stream shipped %q, want two full chunks and the rest after the chunk time", got)
	}
	appendTo(t, filepath.Join(dir, "typescript-1"), "ls\n")
	conn.Stop()
	calls := in.seen()
	if got := chunks(t, calls, "typescript-1"); !slices.Equal(got, []string{"ls\n"}) {
		t.Fatalf("at the end the typescript stream shipped %q", got)
	}
	if got := chunks(t, calls, "typescript-timing-1"); len(got) != 0 {
		t.Fatalf("a file guacd never wrote shipped %q", got)
	}
	if len(failures) != 0 {
		t.Fatalf("the recording failed: %+v", <-failures)
	}
}

func TestRecorderFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer int
		used   float64
		want   string
	}{{"undeliverable", 503, 0.1, RecordingFailed}, {"refused", 409, 0.1, RecordingFailed}, {"volume", 204, 0.95, RecordingVolumeFull}} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := client(t, tc.answer)
			dir := t.TempDir()
			failed := make(chan Failure, 4)
			conn, err := recorder(c, dir, tc.used).Follow(context.Background(), 1, func(f Failure) { failed <- f })
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Stop()
			appendTo(t, filepath.Join(dir, "screen-1"), "0123456789")
			select {
			case f := <-failed:
				if f.Reason != tc.want {
					t.Fatalf("failed with %q (%v), want %q", f.Reason, f.Err, tc.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("no failure, want %q", tc.want)
			}
		})
	}
}
