package store_test

import (
	"testing"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/store/storetest"
)

func TestMemoryPassesThePortTests(t *testing.T) {
	storetest.Run(t, func(_ *testing.T, now func() time.Time) store.Store { return store.NewMemory(now) })
}
