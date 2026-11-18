package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestVersionPrintsTheVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 || out.String() != version+"\n" || errOut.Len() != 0 {
		t.Fatalf("version: code %d, out %q, err %q", code, out.String(), errOut.String())
	}
}

func TestUnknownSubcommandExits2(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"proxy"}, &out, &errOut); code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "use serve, stub or version") {
		t.Fatalf("unknown: code %d, out %q, err %q", code, out.String(), errOut.String())
	}
}

func TestServeWithoutEventSourceExits1(t *testing.T) {
	t.Setenv("EVENT_SOURCE", "")
	var out, errOut bytes.Buffer
	for _, args := range [][]string{nil, {"serve"}} {
		if code := run(args, &out, &errOut); code != 1 {
			t.Fatalf("%v: code %d, want 1", args, code)
		}
	}
}

func TestStubSubcommandExits1WhenItsAddressIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	old := stubAddr
	stubAddr = ln.Addr().String()
	t.Cleanup(func() { stubAddr = old })
	var out, errOut bytes.Buffer
	if code := run([]string{"stub"}, &out, &errOut); code != 1 {
		t.Fatalf("stub on a taken address: code %d, want 1", code)
	}
}

func TestStubSubcommandAnswersAndStops(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan os.Signal, 1)
	done := make(chan int, 1)
	go func() { done <- serveStub(ln, stop) }()
	resp, err := http.Post("http://"+ln.Addr().String()+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"conformance-echo","messages":[{"role":"user","content":"up"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"content":"echo:up"`) || !strings.Contains(string(body), `"usage":`) {
		t.Fatalf("stub answered %d %s", resp.StatusCode, body)
	}
	stop <- syscall.SIGTERM
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("stub exited %d after SIGTERM, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stub did not stop after SIGTERM")
	}
}
