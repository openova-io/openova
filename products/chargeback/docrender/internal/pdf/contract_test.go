package pdf

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/fixture"
)

// The document the BSS actually sends.
//
// products/chargeback/internal/docs writes testdata/bss-invoice.json from its
// own mapper and asserts it matches byte for byte; this test RENDERS it. The
// two modules cannot import each other, so that committed file is the whole
// contract between them — and rendering it here is what turns "the shapes
// look compatible" into "the invoice the BSS produces comes out as a
// document". A field renamed on either side fails one of the two tests
// instead of surfacing as a 400 on a customer's download months later.
func TestTheDocumentTheBSSSendsRenders(t *testing.T) {
	req, err := fixture.Load("bss-invoice")
	if err != nil {
		t.Fatalf("the BSS contract fixture does not satisfy this renderer's own validation: %v", err)
	}
	out := mustRender(t, req)
	text := extractText(t, out)
	for _, want := range []string{
		"Tax Invoice", "INV-2026-00042", "OVERDUE",
		"Sovereign Cloud Operator LLC", "Acme Trading LLC",
		"OM1100012345", "OM1100098765", "PO-2026-118",
		"k8s.vcpu", "plan.m",
		"Net subtotal", "87.372",
		"Tax (5%)", "4.369",
		"Total", "91.741",
		"Balance due", "41.741",
		"Launch campaign", "TRF-88213", "Net 30 days",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the BSS's invoice rendered without %q\n--- extracted ---\n%s", want, text)
		}
	}
}
