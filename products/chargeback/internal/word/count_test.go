package word

import "testing"

func TestCount(t *testing.T) {
	for n, want := range map[int]string{0: "0 SKUs", 1: "1 SKU", 2: "2 SKUs"} {
		if got := Count(n, "SKU"); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}
