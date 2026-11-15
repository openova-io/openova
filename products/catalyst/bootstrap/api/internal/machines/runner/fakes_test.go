package runner

import (
	"bufio"
	"bytes"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeS3 is an S3 endpoint over TLS that keeps every object it is given.
type fakeS3 struct {
	srv     *httptest.Server
	mu      sync.Mutex
	objects map[string][]byte // "/<bucket>/<key>" -> content
	auth    []string
	status  int // when set, every request gets this status
}

func newFakeS3(t *testing.T) *fakeS3 {
	f := &fakeS3{objects: map[string][]byte{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
		return
	}
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		body = unchunk(body)
	}
	f.objects[r.URL.Path] = body
	w.Header().Set("ETag", `"0"`)
	w.WriteHeader(http.StatusOK)
}

// unchunk decodes an aws-chunked body: chunks of "<hex size>[;...]\r\n<data>\r\n".
func unchunk(body []byte) []byte {
	var out []byte
	r := bufio.NewReader(bytes.NewReader(body))
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return out
		}
		size, err := strconv.ParseInt(strings.TrimSpace(strings.SplitN(line, ";", 2)[0]), 16, 64)
		if err != nil || size == 0 {
			return out
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return out
		}
		out = append(out, chunk...)
		_, _ = r.ReadString('\n')
	}
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}

func (f *fakeS3) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.auth)
}

// testEnv builds the places of a run: a work folder, a content folder with release
// 0.1.82 and the data stream of Ubuntu 24.04, a binding with the server's certificate,
// and fake programs that fail the test when they are called.
func testEnv(t *testing.T, s3 *fakeS3) Env {
	dir := t.TempDir()
	env := Env{
		HostRoot:    filepath.Join(dir, "host"),
		WorkDir:     filepath.Join(dir, "work"),
		ContentDir:  filepath.Join(dir, "content"),
		BindingDir:  filepath.Join(dir, "binding"),
		ResultFile:  filepath.Join(dir, "termination-log"),
		Trivy:       filepath.Join(dir, "bin", "trivy"),
		OscapChroot: filepath.Join(dir, "bin", "oscap-chroot"),
	}
	for _, d := range []string{env.HostRoot, env.WorkDir, env.BindingDir, filepath.Join(dir, "bin"), filepath.Join(env.ContentDir, "0.1.82")} {
		must(t, os.MkdirAll(d, 0o750))
	}
	must(t, os.WriteFile(filepath.Join(env.ContentDir, "0.1.82", "ssg-ubuntu2404-ds.xml"), []byte("<ds/>\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(env.BindingDir, BindingAccessKeyID), []byte("AKTEST\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(env.BindingDir, BindingSecretAccessKey), []byte("not-a-real-key\n"), 0o600))
	if s3 != nil {
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s3.srv.Certificate().Raw})
		must(t, os.WriteFile(filepath.Join(env.BindingDir, BindingCA), ca, 0o600))
	}
	fakeProgram(t, env.Trivy, "echo trivy must not run >&2; exit 99")
	fakeProgram(t, env.OscapChroot, "echo oscap-chroot must not run >&2; exit 99")
	return env
}

// fakeProgram writes a shell program that records its arguments, one per line, in
// <path>.args, then runs body.
func fakeProgram(t *testing.T, path, body string) {
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$0.args\"\n" + body + "\n"
	must(t, os.WriteFile(path, []byte(script), 0o700)) //nolint:gosec // a fake program must be executable
}

// args returns what a fake program was called with, or nil when it did not run.
func args(t *testing.T, path string) []string {
	data, err := os.ReadFile(path + ".args") //nolint:gosec // a file of the test folder
	if os.IsNotExist(err) {
		return nil
	}
	must(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// config is a valid node-mode run of task on Machine acme/ledger-db-1, run scan-1.
func config(task string, s3 *fakeS3) Config {
	c := Config{
		Task: task, Mode: ModeNode,
		Machine:  Ref{Namespace: "acme", Name: "ledger-db-1"},
		Run:      Ref{Namespace: "machine-ops", Name: "scan-1"},
		OSFamily: "ubuntu", OSVersion: "24.04",
		Evidence: Evidence{Endpoint: "https://127.0.0.1:1", Region: "us-east-1", Bucket: "machine-evidence",
			Prefix: "acme/ledger-db-1/scan-1/"},
	}
	if s3 != nil {
		c.Evidence.Endpoint = s3.srv.URL
	}
	if task == TaskHardening {
		c.ProfileID = "xccdf_org.ssgproject.content_profile_cis_level1_server"
		c.ContentRelease = "0.1.82"
	}
	return c
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
