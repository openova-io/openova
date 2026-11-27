package suite

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// PackHoldFile is the file, under the checkout of the commit under test, that
// names the required checks a run in local (the local provider pack of the job
// conformance-local) holds at expected-fail. The pack installs the kit charts
// of a change from the tree, but runs the images that the kit pins from main;
// a check that proves a change to such an image passes there only after the
// merge and the next pin.
const PackHoldFile = "tests/acceptance/sovereign/local-pack-expected-fail.txt"

// Hold is one line of PackHoldFile: a check and the reason it is held.
type Hold struct {
	ID     string
	Reason string
}

// HoldForPack sets to expected-fail, in m, every check that PackHoldFile
// under repoRoot names, when r is RunInLocal; a run in cloud or in self holds
// nothing and reads no file, so the gate of a Sovereign keeps every check at
// its state of the manifest. Expected-fail tolerates both outcomes: a held
// check that fails does not fail the run, and one that passes is recorded PASS
// and fails nothing (Run.Finalize fails a run on a required check only).
//
// A line is a check identifier, a space and the reason; blank lines and lines
// that start with # are skipped. A missing file holds nothing. A line without
// a reason, a check that is not in m, is not required, does not list local in
// runs_in, or is named twice is an error that wraps ErrManifest: the run stops
// and writes no record. HoldForPack returns the holds in the order of the file.
func HoldForPack(m *Manifest, r RunIn, repoRoot string) ([]Hold, error) {
	if r != RunInLocal {
		return nil, nil
	}
	b, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot, filepath.FromSlash(PackHoldFile))))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	holds := []Hold{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, reason, _ := strings.Cut(line, " ")
		reason = strings.TrimSpace(reason)
		at := fmt.Sprintf("%s line %d", PackHoldFile, n)
		i := slices.IndexFunc(m.Checks, func(e ManifestCheck) bool { return e.ID == id })
		switch {
		case reason == "":
			return nil, fmt.Errorf("%w: %s: %s has no reason", ErrManifest, at, id)
		case slices.ContainsFunc(holds, func(h Hold) bool { return h.ID == id }):
			return nil, fmt.Errorf("%w: %s: %s is named twice", ErrManifest, at, id)
		case i < 0:
			return nil, fmt.Errorf("%w: %s: %s is not a check of the manifest", ErrManifest, at, id)
		case m.Checks[i].State != StateRequired:
			return nil, fmt.Errorf("%w: %s: %s is %s, and only a required check is held", ErrManifest, at, id, m.Checks[i].State)
		case !slices.Contains(m.Checks[i].RunsIn, string(RunInLocal)):
			return nil, fmt.Errorf("%w: %s: %s does not run in local", ErrManifest, at, id)
		}
		m.Checks[i].State = StateExpectedFail
		holds = append(holds, Hold{ID: id, Reason: reason})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrManifest, PackHoldFile, err)
	}
	return holds, nil
}
