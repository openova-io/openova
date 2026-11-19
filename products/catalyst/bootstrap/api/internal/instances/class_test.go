package instances

import (
	"net/http"
	"testing"
)

func TestClassLifecycleRefusal(t *testing.T) {
	cases := []struct {
		name, class, lifecycle string
		admin                  bool
		status                 int
		code                   string
	}{
		{"a platform component for a caller who is not a sovereign-admin", "platform-component", "experimental", false, http.StatusForbidden, "class-not-installable"},
		{"a platform component for a sovereign-admin", "platform-component", "experimental", true, 0, ""},
		{"a deprecated Blueprint for a sovereign-admin", "application", "deprecated", true, http.StatusConflict, "blueprint-deprecated"},
		{"a deprecated platform component for a caller who is not a sovereign-admin", "platform-component", "deprecated", false, http.StatusForbidden, "class-not-installable"},
		{"an experimental application", "application", "experimental", false, 0, ""},
		{"a supported data service", "data-service", "supported", false, 0, ""},
		{"an object written before contract C1", "", "", false, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassLifecycleRefusal("bp-x", tc.class, tc.lifecycle, tc.admin)
			if tc.status == 0 {
				if got != nil {
					t.Fatalf("want no refusal, got %+v", got)
				}
				return
			}
			if got == nil || got.Status != tc.status || got.Code != tc.code {
				t.Fatalf("want %d %s, got %+v", tc.status, tc.code, got)
			}
		})
	}
}
