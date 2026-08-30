package huawei

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The vectors below were produced by running the Catalyst bootstrap API's
// signer (products/catalyst/bootstrap/api/internal/providers/huawei/sigv3.go)
// with its clock pinned to 2026-08-31T12:00:00Z and these exact inputs. The
// access/secret keys are throwaway test strings, not credentials.
const (
	vecAK   = "AKIAEXAMPLETESTKEY00"
	vecSK   = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEYX"
	vecPID  = "0123456789abcdef0123456789abcdef"
	vecDate = "20260831T120000Z"
)

var fixedClock = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }

func TestSignVectorsMatchReferenceImplementation(t *testing.T) {
	cases := []struct {
		name, method, url string
		body              []byte
		want              string
	}{
		{
			"ecs-list-with-query", "GET",
			"https://ecs.me-east-215.kom4dc.nationalcloud.om/v1/0123456789abcdef0123456789abcdef/cloudservers/detail?limit=200&offset=1", nil,
			"SDK-HMAC-SHA256 Access=AKIAEXAMPLETESTKEY00, SignedHeaders=host;x-project-id;x-sdk-date, Signature=38fa1d8855584c2bd2f9362aeed32312354cd7974e6bebbab13c094af9e6f38c",
		},
		{
			"evs-list-no-query", "GET",
			"https://evs.me-east-215.kom4dc.nationalcloud.om/v2/0123456789abcdef0123456789abcdef/cloudvolumes/detail", nil,
			"SDK-HMAC-SHA256 Access=AKIAEXAMPLETESTKEY00, SignedHeaders=host;x-project-id;x-sdk-date, Signature=bbf04d77324b131c5249f2d5661e9d370cb0b66f58896d2ba5d6bfa4a4d5501e",
		},
		{
			"post-with-body", "POST",
			"https://ecs.me-east-215.kom4dc.nationalcloud.om/v1/0123456789abcdef0123456789abcdef/cloudservers/action",
			[]byte(`{"os-stop":{"servers":[{"id":"abc"}]}}`),
			"SDK-HMAC-SHA256 Access=AKIAEXAMPLETESTKEY00, SignedHeaders=content-type;host;x-project-id;x-sdk-date, Signature=af9d9a1a146e7a60d63cb071965e954fcbd18f33eff0f1ed24903f247d7e57b4",
		},
		{
			"ces-dim-query", "GET",
			"https://ces.me-east-215.kom4dc.nationalcloud.om/V1.0/0123456789abcdef0123456789abcdef/metric-data?namespace=SYS.ECS&metric_name=cpu_util&dim.0=instance_id,abc-123&from=1756641600000&to=1756645200000&period=3600&filter=average", nil,
			"SDK-HMAC-SHA256 Access=AKIAEXAMPLETESTKEY00, SignedHeaders=host;x-project-id;x-sdk-date, Signature=599be8e46080d4a0dd876f0d9d21129b82b1aa5b5ccac4de785fe2cdd68d2a4a",
		},
	}
	creds := Credentials{AccessKey: vecAK, SecretKey: vecSK, ProjectID: vecPID}
	s := Signer{Now: fixedClock}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r *http.Request
			if c.body != nil {
				r, _ = http.NewRequest(c.method, c.url, bytes.NewReader(c.body))
			} else {
				r, _ = http.NewRequest(c.method, c.url, nil)
			}
			got, err := s.Sign(r, creds, c.body)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("Authorization mismatch\n got %s\nwant %s", got, c.want)
			}
			if r.Header.Get("Authorization") != got {
				t.Error("Authorization header not stamped on request")
			}
			if r.Header.Get("X-Sdk-Date") != vecDate {
				t.Errorf("X-Sdk-Date = %q", r.Header.Get("X-Sdk-Date"))
			}
			if r.Header.Get("X-Project-Id") != vecPID {
				t.Errorf("X-Project-Id = %q", r.Header.Get("X-Project-Id"))
			}
			if strings.Contains(got, vecSK) {
				t.Error("secret key leaked into Authorization header")
			}
		})
	}
}

func TestSignRejectsMissingCredentials(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://ecs.example/v1/p/cloudservers/detail", nil)
	if _, err := (Signer{}).Sign(r, Credentials{AccessKey: "a"}, nil); err == nil {
		t.Fatal("missing secret accepted")
	}
	if _, err := (Signer{}).Sign(nil, Credentials{AccessKey: "a", SecretKey: "b"}, nil); err == nil {
		t.Fatal("nil request accepted")
	}
}

func TestCanonicalURIAlwaysTrailingSlash(t *testing.T) {
	cases := map[string]string{
		"https://vpc.example.com/v1/projects/":                        "/v1/projects/",
		"https://vpc.example.com/v1/projects/abc/publicips":           "/v1/projects/abc/publicips/",
		"https://ecs.example.com/v1/proj/cloudservers/detail?limit=1": "/v1/proj/cloudservers/detail/",
		"https://api.example.com/":                                    "/",
	}
	for in, want := range cases {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := canonicalURI(u); got != want {
			t.Errorf("canonicalURI(%q) = %q, want %q", in, got, want)
		}
	}
	if got := canonicalURI(nil); got != "/" {
		t.Errorf("nil url = %q", got)
	}
}

func TestCanonicalQueryStringSortsKeysAndValues(t *testing.T) {
	q := url.Values{}
	q.Add("b", "2")
	q.Add("a", "z")
	q.Add("a", "y")
	q.Add("dim.0", "instance_id,abc")
	got := canonicalQueryString(q)
	want := "a=y&a=z&b=2&dim.0=instance_id%2Cabc"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
