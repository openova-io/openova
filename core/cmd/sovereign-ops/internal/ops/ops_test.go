package ops

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestResultLine checks the exact result line on stdout and in the
// termination log: evidence is never null, error is empty on success.
func TestResultLine(t *testing.T) {
	for _, c := range []struct {
		evidence []string
		err      error
		want     string
	}{
		{nil, nil, `{"command":"probe","result":"success","evidence":[],"error":""}`},
		{[]string{"commit abc", "wrote a/<b>.yaml"}, nil, `{"command":"probe","result":"success","evidence":["commit abc","wrote a/<b>.yaml"],"error":""}`},
		{[]string{"read x"}, errors.New("boom"), `{"command":"probe","result":"failed","evidence":["read x"],"error":"boom"}`},
		{nil, Transient(errors.New("no answer")), `{"command":"probe","result":"failed","evidence":[],"error":"transient: no answer"}`},
	} {
		termLog := filepath.Join(t.TempDir(), "termination-log")
		var out bytes.Buffer
		Finish(&out, termLog, "probe", c.evidence, c.err)
		got, err := os.ReadFile(filepath.Clean(termLog))
		if err != nil || out.String() != c.want+"\n" || string(got) != c.want+"\n" {
			t.Fatalf("stdout %q, termination log %q (%v), want %q", out.String(), got, err, c.want)
		}
	}
}

// TestExitCodes checks 0, 1 and 75, also when the termination log cannot
// be written.
func TestExitCodes(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "termination-log")
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("boom"), 1},
		{Transient(errors.New("no answer")), 75},
		{errors.Join(errors.New("seed"), Transient(errors.New("no answer"))), 75},
	} {
		var out bytes.Buffer
		if got := Finish(&out, missing, "probe", nil, c.err); got != c.want || out.Len() == 0 {
			t.Fatalf("error %v: exit code %d and %d bytes on stdout, want %d and one line", c.err, got, out.Len(), c.want)
		}
	}
	if Transient(nil) != nil {
		t.Fatal("Transient(nil) is not nil")
	}
}
