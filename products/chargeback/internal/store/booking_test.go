package store_test

import (
	"context"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// assignBook puts a source on a price book (DESIGN.md §2: the book is a
// property of the SOURCE, never of the customer), failing the test when the
// book's scope does not match the source's layer.
func assignBook(t *testing.T, st *store.Store, sourceID, bookID string) {
	t.Helper()
	if err := st.SetSourcePriceBook(context.Background(), sourceID, bookID); err != nil {
		t.Fatalf("assign book %s to source %s: %v", bookID, sourceID, err)
	}
}
