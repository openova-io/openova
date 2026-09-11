// Package fixture loads the example documents under docrender/testdata/ —
// the same JSON a caller POSTs, decoded through the same path the server
// uses, so a fixture that stops being valid fails the tests that read it.
package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
)

// Dir is docrender/testdata, resolved from this file's own location so a
// test in any package finds it.
func Dir() string {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "testdata"
	}
	return filepath.Join(filepath.Dir(self), "..", "..", "testdata")
}

// Raw returns the fixture bytes for a template name.
func Raw(template string) ([]byte, error) {
	return os.ReadFile(filepath.Join(Dir(), template+".json"))
}

// Load decodes and validates the fixture for a template name. Unknown fields
// are refused, exactly as the server refuses them, so a fixture cannot drift
// away from the wire contract without a test noticing.
func Load(template string) (document.Request, error) {
	raw, err := Raw(template)
	if err != nil {
		return document.Request{}, err
	}
	var req document.Request
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("%s.json: %w", template, err)
	}
	if err := req.Validate(); err != nil {
		return req, fmt.Errorf("%s.json: %w", template, err)
	}
	return req, nil
}

// All loads every document kind's fixture.
func All() (map[string]document.Request, error) {
	out := map[string]document.Request{}
	for _, k := range document.Templates {
		req, err := Load(k)
		if err != nil {
			return nil, err
		}
		out[k] = req
	}
	return out, nil
}
