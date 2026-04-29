package reserved

import "testing"

func TestIsReserved(t *testing.T) {
	cases := map[string]bool{
		"api":      true,
		"console":  true,
		"openbao":  true,
		"k8s":      true,
		"omantel":  false,
		"acme":     false,
		"":         false,
		"API":      true, // case-insensitive
		"  api  ":  true, // trims whitespace
		"foo-bar":  false,
		"my-corp":  false,
	}
	for k, want := range cases {
		if got := IsReserved(k); got != want {
			t.Errorf("IsReserved(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestAllSorted(t *testing.T) {
	names := All()
	if len(names) == 0 {
		t.Fatal("expected non-empty reserved list")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("All() not sorted at index %d: %q >= %q", i, names[i-1], names[i])
		}
	}
}

func TestAllNoReachableNames(t *testing.T) {
	// Sanity-check: every name in All() must be IsReserved.
	for _, n := range All() {
		if !IsReserved(n) {
			t.Errorf("%q is in All() but IsReserved returned false", n)
		}
	}
}
