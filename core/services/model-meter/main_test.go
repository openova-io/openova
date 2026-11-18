package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionPrintsTheVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 || out.String() != version+"\n" || errOut.Len() != 0 {
		t.Fatalf("version: code %d, out %q, err %q", code, out.String(), errOut.String())
	}
}

func TestUnknownSubcommandExits2(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"proxy"}, &out, &errOut); code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "serve or version") {
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
