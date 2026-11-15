package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/suite"
)

// A run that is NOT-MEASURED has an exit code of its own, apart from a FAIL.
func TestExitCode(t *testing.T) {
	for name, c := range map[string]struct {
		res  *suite.Run
		err  error
		want int
	}{
		"PASS":                  {&suite.Run{Result: suite.Pass}, nil, 0},
		"FAIL":                  {&suite.Run{Result: suite.Fail}, nil, 1},
		"a manifest that fails": {nil, fmt.Errorf("x: %w", suite.ErrManifest), 2},
		"a harness error":       {&suite.Run{Result: suite.Fail}, errors.New("record: disk full"), 3},
		"NOT-MEASURED":          {&suite.Run{Result: suite.NotMeasured}, nil, 4},
	} {
		if got := exitCode(c.res, c.err); got != c.want {
			t.Errorf("%s: exit code %d, want %d", name, got, c.want)
		}
	}
}
