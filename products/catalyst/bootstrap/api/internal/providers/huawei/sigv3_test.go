// sigv3_test.go — pins the canonical URI trailing-slash convention
// per Wave 5.91 (#2428 Bug 2 fix).
//
// HCS expects canonical-request URIs to ALWAYS end with `/`, even when
// the request URI itself does not. The Wave 5.91 fix at sigv3.go's
// canonicalURI handles this; this test pins the behavior so a future
// "spec-compliance" refactor doesn't silently regress.

package huawei

import (
	"net/url"
	"testing"
)

func TestCanonicalURI_AlwaysTrailingSlash(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty url returns root", "", "/"},
		{"already-trailing-slash preserved", "https://vpc.example.com/v1/projects/", "/v1/projects/"},
		{"trailing-slash-appended for HCS", "https://vpc.example.com/v1/projects/abc/publicips", "/v1/projects/abc/publicips/"},
		{"deep path gets trailing-slash", "https://ecs.example.com/v1/proj/cloudservers/detail", "/v1/proj/cloudservers/detail/"},
		{"root-only stays root", "https://api.example.com/", "/"},
		{"path-with-uuid trailing-slash", "https://vpc.example.com/v1/proj/vpcs/abc-123-def", "/v1/proj/vpcs/abc-123-def/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var u *url.URL
			if c.in != "" {
				var err error
				u, err = url.Parse(c.in)
				if err != nil {
					t.Fatalf("parse %q: %v", c.in, err)
				}
			}
			got := canonicalURI(u)
			if got != c.want {
				t.Errorf("canonicalURI(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}
