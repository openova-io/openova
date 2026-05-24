// Package huawei — Huawei Cloud (Stack) IAM Signature v3 over plain HTTP.
//
// Mirrors the canonical AK/SK signing flow documented at
// https://support.huaweicloud.com/intl/en-us/devg-apisign/api-sign-algorithm.html
// — same signing model AWS SigV4 uses (SHA256 + canonical request +
// string-to-sign + HMAC chain) with Huawei-specific tweaks (algorithm
// string `SDK-HMAC-SHA256`, header `X-Sdk-Date`).
//
// We sign requests directly with `net/http` rather than pulling in the
// huaweicloud-sdk-go-v3 module tree (700+ packages, ~80MB compiled).
// Same architectural choice the Hetzner adapter makes against
// internal/hetzner/client.go — both providers stay SDK-free.

package huawei

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// hwAlgorithm is the canonical AK/SK Signature v3 algorithm string.
	hwAlgorithm = "SDK-HMAC-SHA256"

	// hwTimeFormat matches the `X-Sdk-Date` ISO-8601 basic format
	// (no separators) Huawei's signing spec requires.
	hwTimeFormat = "20060102T150405Z"
)

// hwCreds is the minimal credential bag needed to sign a single
// request. The catalyst-api passes these through from
// providers.ProviderCreds at every call site.
type hwCreds struct {
	AccessKey string
	SecretKey string
	ProjectID string // Region-scoped; threaded into X-Project-Id.
}

// signRequest mutates `req` in place by injecting the X-Sdk-Date,
// Authorization, and (when ProjectID is non-empty) X-Project-Id
// headers. The body must be a non-nil io.Reader that supports Read
// + Seek so we can compute the SHA256 hash without consuming the
// request body. For empty-body requests pass http.NoBody (which
// implements both interfaces and returns 0 bytes).
//
// Returns the signed Authorization header value alongside the
// mutation so tests can pin the exact string format.
func signRequest(req *http.Request, creds hwCreds, body []byte) (string, error) {
	if req == nil {
		return "", fmt.Errorf("nil request")
	}
	if creds.AccessKey == "" || creds.SecretKey == "" {
		return "", fmt.Errorf("access_key + secret_key required to sign request")
	}

	// 1. Stamp X-Sdk-Date (used both as a signed header and as the
	//    timestamp inside the credential scope).
	now := time.Now().UTC().Format(hwTimeFormat)
	req.Header.Set("X-Sdk-Date", now)
	if creds.ProjectID != "" && req.Header.Get("X-Project-Id") == "" {
		req.Header.Set("X-Project-Id", creds.ProjectID)
	}
	// Host is mandatory per the signing spec; net/http omits it from
	// req.Header by default.
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}
	if body != nil && len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// 2. Canonical headers — alphabetised key:value list of lower-case
	//    header names + trimmed values, joined by \n.
	signedHeaders, canonicalHeaders := canonicalHeaders(req.Header)

	// 3. Canonical query string — alphabetised key=val pairs.
	canonicalQuery := canonicalQueryString(req.URL.Query())

	// 4. Hex(SHA256(body)).
	bodyHash := sha256Hex(body)

	// 5. Canonical request.
	canonicalReq := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		bodyHash,
	}, "\n")

	// 6. String-to-sign.
	stringToSign := strings.Join([]string{
		hwAlgorithm,
		now,
		sha256Hex([]byte(canonicalReq)),
	}, "\n")

	// 7. Signature = HMAC-SHA256(secret_key, stringToSign).
	mac := hmac.New(sha256.New, []byte(creds.SecretKey))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	// 8. Authorization header.
	auth := fmt.Sprintf(
		"%s Access=%s, SignedHeaders=%s, Signature=%s",
		hwAlgorithm,
		creds.AccessKey,
		signedHeaders,
		signature,
	)
	req.Header.Set("Authorization", auth)
	return auth, nil
}

// canonicalHeaders returns (signedHeadersString, canonicalHeadersString)
// for the headers of one request. Header names are lower-cased,
// values trimmed of surrounding whitespace.
func canonicalHeaders(h http.Header) (string, string) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)

	var signed []string
	var canonical []string
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		signed = append(signed, k)
		// Multi-value headers join with commas.
		values := h.Values(http.CanonicalHeaderKey(k))
		if len(values) == 0 {
			// Fallback to the raw lower-case lookup in case canonical
			// key resolution drops the entry (e.g. `Host` was set
			// via Header.Set with the canonical case).
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

// canonicalURI returns the path component encoded per the signing
// spec. Spec is AWS SigV4 compatible with one HCS divergence: HCS
// expects the canonical URI to ALWAYS end with `/` (even when the
// request URI itself does not), per the empirical error response
// returned by HCS endpoints:
//
//	{"error_msg":"...verify aksk signature fail, canonical_request:
//	GET|/v1/<project>/publicips/||host:vpc.<region>.kom4dc...|x-sdk-
//	date:<ts>||host;x-sdk-date|<bodyHash>"}
//
// Note the trailing `/` after `publicips` in the server's expected
// canonical-request — even though the actual request URI was
// `/v1/<project>/publicips` without trailing slash. Pre-Wave-5.91
// this function returned `u.EscapedPath()` verbatim, missing the
// trailing slash, causing every Huawei orphan-sweep API call to
// fail with HTTP 401 verify-aksk-signature-fail. Surfaced live on
// the d3c6268c wipe (#2423 / #2428 Bug 2) — wipe handler's orphan
// sweep silently no-op'd because every ECS/VPC/EIP/SG list call
// 401'd, leaving Huawei resources alive after the wipe.
//
// Spec reference: AWS SigV4 + HCS-specific trailing-slash convention
// verified by 7+ successful API calls from a Python reference
// implementation (see /tmp/huawei-sigv3.py in the #2423 walk).
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

// canonicalQueryString builds the sorted key=val canonical form.
func canonicalQueryString(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// sha256Hex returns the lowercase hex SHA256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// doSignedRequest builds + signs + executes one HTTP request against
// a Huawei service endpoint. Returns the response body bytes + the
// status code (so callers can branch on 4xx vs 5xx without a second
// network round-trip).
func doSignedRequest(client *http.Client, creds hwCreds, method, urlStr string, body []byte) ([]byte, int, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, urlStr, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	if _, err := signRequest(req, creds, body); err != nil {
		return nil, 0, fmt.Errorf("sign request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}
	return respBody, resp.StatusCode, nil
}
