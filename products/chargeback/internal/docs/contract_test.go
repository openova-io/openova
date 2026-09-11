package docs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// sharedFixture is a document produced BY THIS MAPPER and rendered BY THE
// RENDERER'S OWN TEST SUITE (docrender/internal/pdf renders every file in
// that directory's fixture set).
//
// The two live in different Go modules and cannot import each other, so this
// committed file is the contract between them. If this side changes a field
// name, a shape or a date format, this test goes red; if the renderer stops
// accepting what this side sends, ITS test goes red on the same file. Without
// a shared artefact the drift would surface as a 400 on a customer's invoice
// download, months later, on a Sovereign.
const sharedFixture = "../../docrender/testdata/bss-invoice.json"

func TestMappedInvoiceMatchesTheSharedRendererFixture(t *testing.T) {
	path := filepath.Clean(sharedFixture)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the shared fixture is missing: %v\nRegenerate it with DOCRENDER_FIXTURE_WRITE=1 go test -count=1 ./internal/docs/", err)
	}

	got, err := json.MarshalIndent(InvoiceRequest(issuedStatement(), customer(), settings(), "en"), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	if os.Getenv("DOCRENDER_FIXTURE_WRITE") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s — re-run the docrender suite to confirm it still renders", path)
		return
	}

	if string(got) != string(want) {
		t.Fatalf(`the mapped document no longer matches %s.

The renderer's test suite renders that exact file, so a change here is a
change to the contract between the two modules. If the change is intended,
regenerate it and RUN THE RENDERER'S TESTS:

    DOCRENDER_FIXTURE_WRITE=1 go test -count=1 ./internal/docs/
    (cd ../docrender && go test -count=1 ./...)

--- mapped now ---
%s
--- committed ---
%s`, path, got, want)
	}
}
