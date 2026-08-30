// Package huawei is the cloud collector for Huawei Cloud Stack Online
// (National Cloud, Kom4DC) projects: AK/SK request signing, the list APIs
// for ECS/EVS/EIP/ELB/NAT, the CTS change-log poller, the CES utilisation
// sampler and the inventory-to-usage emitter.
//
// Signing mirrors the SDK-HMAC-SHA256 algorithm the Catalyst bootstrap API
// uses for the same gateway (products/catalyst/bootstrap/api/internal/
// providers/huawei/sigv3.go). That package is internal to another module, so
// the semantics are reproduced here and pinned by signer_test.go against
// vectors derived from that implementation with a fixed clock.
package huawei

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// Algorithm is the canonical AK/SK Signature v3 algorithm string.
	Algorithm = "SDK-HMAC-SHA256"

	// dateFormat matches the X-Sdk-Date ISO-8601 basic format.
	dateFormat = "20060102T150405Z"
)

// Credentials is the signing material for one project.
type Credentials struct {
	AccessKey string
	SecretKey string
	ProjectID string
}

// Signer stamps SDK-HMAC-SHA256 signatures onto requests. Now is injectable
// so tests can pin exact header values; nil means time.Now.
type Signer struct {
	Now func() time.Time
}

// Sign mutates req in place: X-Sdk-Date, Host, X-Project-Id (when the
// credentials carry one and the header is unset), Content-Type for bodies,
// and Authorization. body must be the exact bytes the request will send
// (nil for none). It returns the Authorization header value.
func (s Signer) Sign(req *http.Request, creds Credentials, body []byte) (string, error) {
	if req == nil {
		return "", errors.New("nil request")
	}
	if creds.AccessKey == "" || creds.SecretKey == "" {
		return "", errors.New("access_key and secret_key are required to sign a request")
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	date := now().UTC().Format(dateFormat)
	req.Header.Set("X-Sdk-Date", date)
	if creds.ProjectID != "" && req.Header.Get("X-Project-Id") == "" {
		req.Header.Set("X-Project-Id", creds.ProjectID)
	}
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	signedHeaders, canonicalHdrs := canonicalHeaders(req.Header)
	canonicalReq := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQueryString(req.URL.Query()),
		canonicalHdrs,
		signedHeaders,
		sha256Hex(body),
	}, "\n")
	stringToSign := strings.Join([]string{Algorithm, date, sha256Hex([]byte(canonicalReq))}, "\n")

	mac := hmac.New(sha256.New, []byte(creds.SecretKey))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	auth := fmt.Sprintf("%s Access=%s, SignedHeaders=%s, Signature=%s", Algorithm, creds.AccessKey, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
	return auth, nil
}

// canonicalHeaders returns (signedHeaders, canonicalHeaders): lower-cased
// names sorted, values trimmed, multi-values comma-joined, each line
// terminated by a newline.
func canonicalHeaders(h http.Header) (string, string) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)
	var signed, canonical []string
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		signed = append(signed, k)
		values := h.Values(http.CanonicalHeaderKey(k))
		if len(values) == 0 {
			values = h[http.CanonicalHeaderKey(k)]
		}
		trimmed := make([]string, len(values))
		for i, v := range values {
			trimmed[i] = strings.TrimSpace(v)
		}
		canonical = append(canonical, k+":"+strings.Join(trimmed, ","))
	}
	return strings.Join(signed, ";"), strings.Join(canonical, "\n") + "\n"
}

// canonicalURI is the escaped path, always terminated by "/" — the HCS
// gateway computes its canonical request with a trailing slash even when
// the request URI has none, and rejects the signature otherwise.
func canonicalURI(u *url.URL) string {
	if u == nil || u.Path == "" {
		return "/"
	}
	p := u.EscapedPath()
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// canonicalQueryString is the sorted key=value list, values sorted per key.
func canonicalQueryString(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
