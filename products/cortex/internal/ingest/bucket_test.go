package ingest

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSignKnownAnswer signs the example "GET Object" of the Amazon S3 documentation of
// Signature Version 4 and expects its published signature.
func TestSignKnownAnswer(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-9")
	req.Header.Set("x-amz-content-sha256", emptySHA256)
	sign(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1", time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// fakeS3 is a path-style bucket that keeps objects in memory and records the access key of
// each call.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	keys    []string
}

func (s *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cred, _, _ := strings.Cut(strings.TrimPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="), "/")
	s.keys = append(s.keys, cred)
	key, ok := strings.CutPrefix(r.URL.Path, "/originals/")
	if !ok || r.Header.Get("x-amz-date") == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.objects[key], _ = io.ReadAll(r.Body)
	case http.MethodGet:
		b, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

// binding writes the files of an object-storage binding for the server srv into a folder.
func binding(t *testing.T, srv *httptest.Server, more map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"endpoint": srv.URL, "region": "us-east-1", "bucket": "originals", "path-style": "true",
		"access-key-id": "AK1", "secret-access-key": "SK1",
		"ca.crt": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))}
	for k, v := range more {
		files[k] = v
	}
	for k, v := range files {
		if v == "-" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBucketRoundTrip(t *testing.T) {
	s3 := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewTLSServer(s3)
	defer srv.Close()
	dir := binding(t, srv, nil)
	b, err := OpenBucket(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "00000000-0000-4000-8000-000000000001/" + strings.Repeat("ab", 32)
	if err := b.Put(ctx, key, strings.NewReader("hello"), 5, strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}
	// A rotated key is used by the next call.
	if err := os.WriteFile(filepath.Join(dir, "access-key-id"), []byte("AK2"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, n, err := b.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "hello" || n != 5 {
		t.Fatalf("Get = %q, %d", got, n)
	}
	if err := b.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, key); err != nil {
		t.Fatalf("a second delete: %v", err)
	}
	if _, _, err := b.Get(ctx, key); !errors.Is(err, ErrNoObject) {
		t.Fatalf("Get after Delete: %v", err)
	}
	if want := []string{"AK1", "AK2", "AK2", "AK2", "AK2"}; strings.Join(s3.keys, " ") != strings.Join(want, " ") {
		t.Fatalf("access keys %v, want %v", s3.keys, want)
	}
	if err := b.Put(ctx, "../etc/passwd", bytes.NewReader(nil), 0, emptySHA256); err == nil {
		t.Fatal("a key this package does not make was sent")
	}
}

func TestOpenBucketRefuses(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	for what, more := range map[string]map[string]string{
		"an http endpoint":       {"endpoint": strings.Replace(srv.URL, "https:", "http:", 1)},
		"an endpoint with path":  {"endpoint": srv.URL + "/bucket"},
		"no region":              {"region": "-"},
		"a bucket of upper case": {"bucket": "Originals"},
		"path-style yes":         {"path-style": "yes"},
		"a ca.crt of no PEM":     {"ca.crt": "not a certificate"},
	} {
		if _, err := OpenBucket(binding(t, srv, more)); err == nil {
			t.Errorf("%s: OpenBucket passed", what)
		}
	}
	b, err := OpenBucket(binding(t, srv, map[string]string{"path-style": "false", "secret-access-key": "-"}))
	if err != nil || b.base != "https://originals."+strings.TrimPrefix(srv.URL, "https://") {
		t.Fatalf("a virtual-hosted bucket: %+v, %v", b, err)
	}
	if _, _, err := b.Get(context.Background(), "00000000-0000-4000-8000-000000000001/"+strings.Repeat("ab", 32)); err == nil ||
		!strings.Contains(err.Error(), "secret-access-key") {
		t.Fatalf("a call without the secret key file: %v", err)
	}
}
