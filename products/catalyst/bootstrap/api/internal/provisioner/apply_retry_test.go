package provisioner

import (
	"reflect"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

func init() {
	providers.RegisterProvider("classifying", classifyDouble{portDouble{name: "classifying"}})
}

// classifyDouble calls every failure that says "transient" retryable; it
// gives a reason only when the failure says "named".
type classifyDouble struct{ portDouble }

func (classifyDouble) ClassifyApplyError(stderr string) providers.ApplyErrorClass {
	switch stderr {
	case "transient":
		return providers.ApplyErrorClass{Retryable: true}
	case "transient named":
		return providers.ApplyErrorClass{Retryable: true, Reason: "cloud pool busy", Addresses: []string{"a"}, StopOnRepeat: true}
	}
	return providers.ApplyErrorClass{}
}

// The adapter's classifier decides; OpenTofu's own read-after-create text is
// retryable on every cloud, also on one without a classifier; a retryable
// class without a reason gets the engine's.
func TestClassifyApplyError_TheAdapterDecides(t *testing.T) {
	const inconsistent = "Error: Provider produced inconsistent result after apply"
	cases := []struct {
		provider, stderr string
		want             providers.ApplyErrorClass
	}{
		{"classifying", "transient named", providers.ApplyErrorClass{Retryable: true, Reason: "cloud pool busy", Addresses: []string{"a"}, StopOnRepeat: true}},
		{"classifying", "transient", providers.ApplyErrorClass{Retryable: true, Reason: "transient apply failure"}},
		{"classifying", "invalid flavor", providers.ApplyErrorClass{}},
		{"classifying", inconsistent, providers.ApplyErrorClass{Retryable: true, Reason: "transient apply failure"}},
		{"aliasing", "transient", providers.ApplyErrorClass{}},
		{"aliasing", inconsistent, providers.ApplyErrorClass{Retryable: true, Reason: "transient apply failure"}},
	}
	for _, c := range cases {
		if got := classifyApplyError(c.provider, c.stderr); !reflect.DeepEqual(got, c.want) {
			t.Errorf("classifyApplyError(%q, %q) = %+v; want %+v", c.provider, c.stderr, got, c.want)
		}
	}
}

// An address stops the retries on its third consecutive failure of a class
// that stops on a repeat; a failure that does not name it starts it again.
func TestApplyFailures_StopsOnTheThirdConsecutiveRepeat(t *testing.T) {
	stop := func(addrs ...string) providers.ApplyErrorClass {
		return providers.ApplyErrorClass{Retryable: true, Addresses: addrs, StopOnRepeat: true}
	}
	f := applyFailures{}
	steps := []struct {
		class providers.ApplyErrorClass
		want  []string
	}{
		{stop("a", "b"), nil},
		{providers.ApplyErrorClass{Retryable: true, Addresses: []string{"a"}}, nil}, // counts a, forgets b
		{stop("a", "b"), []string{"a"}},                                             // a: 3, b: 1
		{stop("b"), nil},                                                            // a forgotten, b: 2
		{providers.ApplyErrorClass{Retryable: true, Addresses: []string{"b"}}, nil}, // b: 3, the class does not stop
		{stop("b"), []string{"b"}},
	}
	for i, s := range steps {
		if got := f.record(s.class); !reflect.DeepEqual(got, s.want) {
			t.Fatalf("step %d: record = %q; want %q (counts %v)", i+1, got, s.want, f)
		}
	}
}

// A capability never sees the redaction marker of a stored record as a
// credential, as Request.Credential does not.
func TestCapabilitySpec_LeavesOutTheRedactionMarker(t *testing.T) {
	req := Request{DeploymentID: "d1", Credentials: map[string]string{"access_key": "AK", "secret_key": RedactedMarker}}
	got := capabilitySpec(req).Creds.Raw
	if want := map[string]string{"access_key": "AK"}; !reflect.DeepEqual(got, want) {
		t.Errorf("capabilitySpec creds = %v; want %v", got, want)
	}
	if req.Credentials["secret_key"] != RedactedMarker {
		t.Error("capabilitySpec changed the request's own credentials")
	}
}
