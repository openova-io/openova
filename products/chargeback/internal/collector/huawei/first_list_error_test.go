package huawei

import (
	"errors"
	"testing"
)

// The surfaced error must not depend on map iteration order: an IAM
// rejection wins over any other failure wherever it sits in the map, and
// without one the registry's first failing kind is reported.
func TestFirstListErrorIsDeterministic(t *testing.T) {
	auth := &GatewayError{Code: "APIGW.0301", Message: "Incorrect IAM authentication information", Status: 401}
	if !auth.Unauthorized() {
		t.Fatal("fixture must be an unauthorized gateway error")
	}
	other := errors.New("nat: 404 not published")
	kinds := SupportedKinds()
	if len(kinds) < 3 {
		t.Fatalf("registry too small for the test: %v", kinds)
	}
	failed := map[string]error{}
	for _, k := range kinds {
		failed[k] = other
	}
	// Put the auth error on the LAST kind so a first-entry-wins mutant fails.
	failed[kinds[len(kinds)-1]] = auth
	for i := 0; i < 50; i++ {
		if got := firstListError(failed); got != auth {
			t.Fatalf("run %d: got %v, want the IAM rejection", i, got)
		}
	}
	// Without an auth error the first kind in registry order is surfaced.
	failed[kinds[len(kinds)-1]] = other
	second := errors.New("second kind failed")
	failed[kinds[1]] = second
	firstErr := errors.New("first kind failed")
	failed[kinds[0]] = firstErr
	if got := firstListError(failed); got != firstErr {
		t.Fatalf("got %v, want the first registry kind's error", got)
	}
	if firstListError(map[string]error{}) != nil {
		t.Fatal("empty map must yield nil")
	}
}
