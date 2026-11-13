package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/openova-io/openova/core/cmd/sovereign-ops/internal/ops"
)

// TestDispatch: a missing or unknown subcommand fails with exit code 1 and
// a result line; a known one ends with its own evidence, error and code.
func TestDispatch(t *testing.T) {
	subcommands["probe"] = func(_ context.Context, args []string, _ io.Writer) ([]string, error) {
		if len(args) == 1 && args[0] == "--retry" {
			return []string{"tried"}, errors.Join(errors.New("probe"), ops.Transient(errors.New("no answer")))
		}
		return []string{"ran " + args[0]}, nil
	}
	defer delete(subcommands, "probe")
	for _, c := range []struct {
		args []string
		code int
		line string
	}{
		{nil, 1, `{"command":"","result":"failed","evidence":[],"error":"usage: sovereign-ops <subcommand> [flags]; subcommands: [probe]"}`},
		{[]string{"no-such"}, 1, `{"command":"no-such","result":"failed","evidence":[],"error":"unknown subcommand \"no-such\"; subcommands: [probe]"}`},
		{[]string{"probe", "x"}, 0, `{"command":"probe","result":"success","evidence":["ran x"],"error":""}`},
		{[]string{"probe", "--retry"}, 75, `{"command":"probe","result":"failed","evidence":["tried"],"error":"probe\ntransient: no answer"}`},
	} {
		var out bytes.Buffer
		if code := run(context.Background(), c.args, &out, io.Discard, ""); code != c.code || out.String() != c.line+"\n" {
			t.Fatalf("%q: exit code %d, line %s; want %d, %s", c.args, code, out.String(), c.code, c.line)
		}
	}
}
