// Package ovh — Registrar adapter for OVH.
//
// OVH's "API v1" uses a 3-part credential: applicationKey, applicationSecret
// and consumerKey. Authentication is by signed request:
//
//	X-Ovh-Application: <applicationKey>
//	X-Ovh-Consumer:    <consumerKey>
//	X-Ovh-Timestamp:   <unix seconds>
//	X-Ovh-Signature:   "$1$" + sha1(appSecret + "+" + consumerKey + "+" +
//	                                  method  + "+" + fullURL      + "+" +
//	                                  body    + "+" + timestamp)
//
// Token format we accept: "appKey:appSecret:consumerKey" (3-part).
//
// Endpoints used:
//
//   - GET  /domain                              — list of domain handles
//                                                 the consumer can see.
//                                                 Used as ValidateToken
//                                                 probe + domain-in-account
//                                                 check.
//   - GET  /domain/{domain}                     — read NS list (returns a
//                                                 small object including
//                                                 nameServerType + dnssecStatus).
//   - POST /domain/{domain}/nameServers/update  — replace nameservers
//                                                 atomically. Body shape:
//                                                 {"nameServers":[
//                                                   {"host":"ns1.openova.io"},
//                                                   {"host":"ns2.openova.io"}
//                                                 ]}
//   - GET  /domain/{domain}/nameServer          — list current NS IDs;
//                                                 then GET /domain/{domain}/
//                                                 nameServer/{id} per id
//                                                 to read the host. Done
//                                                 by GetNameservers below.
//
// Reference: https://eu.api.ovh.com/console/
//
// Note: OVH has multiple regional endpoints (eu, ca, us). Default is
// eu.api.ovh.com; tests + non-EU customers override BaseURL.
package ovh

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// Adapter implements registrar.Registrar for OVH.
type Adapter struct {
	BaseURL string
	HTTP    *http.Client

	// nowFn returns the current Unix timestamp; tests inject a fixed
	// time so signatures are deterministic.
	nowFn func() int64
}

// New returns an OVH adapter with the EU endpoint default.
func New() *Adapter {
	return &Adapter{
		BaseURL: "https://eu.api.ovh.com/1.0",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		nowFn:   func() int64 { return time.Now().Unix() },
	}
}

// Name returns "ovh".
func (a *Adapter) Name() string { return "ovh" }

// creds is the parsed token shape.
type creds struct {
	AppKey      string
	AppSecret   string
	ConsumerKey string
}

func parseToken(token string) (creds, error) {
	parts := strings.Split(strings.TrimSpace(token), ":")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return creds{}, fmt.Errorf("ovh: token must be 'appKey:appSecret:consumerKey': %w", registrar.ErrInvalidToken)
	}
	return creds{AppKey: parts[0], AppSecret: parts[1], ConsumerKey: parts[2]}, nil
}

// ovhError is the standard error envelope.
type ovhError struct {
	ErrorCode string `json:"errorCode"`
	HTTPCode  string `json:"httpCode"`
	Message   string `json:"message"`
}

// classifyHTTP maps HTTP-level status to typed errors.
func classifyHTTP(statusCode int) error {
	switch {
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden:
		return registrar.ErrInvalidToken
	case statusCode == http.StatusTooManyRequests:
		return registrar.ErrRateLimited
	case statusCode == http.StatusNotFound:
		return registrar.ErrDomainNotInAccount
	case statusCode >= 500:
		return registrar.ErrAPIUnavailable
	}
	return nil
}

// ValidateToken: GET /domain. Verifies auth + presence of domain.
func (a *Adapter) ValidateToken(ctx context.Context, token, domain string) error {
	c, err := parseToken(token)
	if err != nil {
		return err
	}
	body, err := a.do(ctx, c, http.MethodGet, "/domain", nil)
	if err != nil {
		return err
	}
	var domains []string
	if err := json.Unmarshal(body, &domains); err != nil {
		return fmt.Errorf("ovh: parse /domain: %w", err)
	}
	want := strings.ToLower(strings.TrimSpace(domain))
	for _, d := range domains {
		if strings.EqualFold(d, want) {
			return nil
		}
	}
	return fmt.Errorf("ovh: %q not in account: %w", domain, registrar.ErrDomainNotInAccount)
}

// SetNameservers: POST /domain/{domain}/nameServers/update.
func (a *Adapter) SetNameservers(ctx context.Context, token, domain string, ns []string) error {
	if len(ns) == 0 {
		return errors.New("ovh: nameservers list is empty")
	}
	c, err := parseToken(token)
	if err != nil {
		return err
	}
	type nsEntry struct {
		Host string `json:"host"`
	}
	entries := make([]nsEntry, 0, len(ns))
	for _, n := range ns {
		entries = append(entries, nsEntry{Host: n})
	}
	payload := map[string]any{"nameServers": entries}
	_, err = a.do(ctx, c, http.MethodPost, "/domain/"+domain+"/nameServers/update", payload)
	return err
}

// GetNameservers: GET /domain/{domain}/nameServer (list of IDs), then per-
// ID resolve to host via GET /domain/{domain}/nameServer/{id}.
func (a *Adapter) GetNameservers(ctx context.Context, token, domain string) ([]string, error) {
	c, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	body, err := a.do(ctx, c, http.MethodGet, "/domain/"+domain+"/nameServer", nil)
	if err != nil {
		return nil, err
	}
	var ids []int64
	if err := json.Unmarshal(body, &ids); err != nil {
		return nil, fmt.Errorf("ovh: parse nameServer ids: %w", err)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		path := fmt.Sprintf("/domain/%s/nameServer/%d", domain, id)
		one, err := a.do(ctx, c, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var ns struct {
			Host string `json:"host"`
		}
		if err := json.Unmarshal(one, &ns); err != nil {
			return nil, fmt.Errorf("ovh: parse nameServer/%d: %w", id, err)
		}
		if ns.Host != "" {
			out = append(out, ns.Host)
		}
	}
	return out, nil
}

// do performs one signed OVH request.
//
// Signature recipe (per OVH docs):
//
//	signature = "$1$" + sha1Hex(
//	  appSecret + "+" + consumerKey + "+" + method + "+" + fullURL +
//	  "+" + body + "+" + timestamp
//	)
//
// "fullURL" is the absolute URL including https:// and any query string.
// "body" is the empty string for GET; the JSON body otherwise. The
// timestamp is unix seconds (string).
func (a *Adapter) do(ctx context.Context, c creds, method, path string, payload any) ([]byte, error) {
	var bodyStr string
	var rdr io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("ovh: marshal payload: %w", err)
		}
		bodyStr = string(buf)
		rdr = bytes.NewReader(buf)
	}

	fullURL := a.BaseURL + path
	ts := strconv.FormatInt(a.nowFn(), 10)

	h := sha1.New()
	io.WriteString(h, c.AppSecret+"+"+c.ConsumerKey+"+"+method+"+"+fullURL+"+"+bodyStr+"+"+ts)
	sig := "$1$" + hex.EncodeToString(h.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, method, fullURL, rdr)
	if err != nil {
		return nil, fmt.Errorf("ovh: build request: %w", err)
	}
	req.Header.Set("X-Ovh-Application", c.AppKey)
	req.Header.Set("X-Ovh-Consumer", c.ConsumerKey)
	req.Header.Set("X-Ovh-Timestamp", ts)
	req.Header.Set("X-Ovh-Signature", sig)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ovh: %s: %w", err.Error(), registrar.ErrAPIUnavailable)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if e := classifyHTTP(resp.StatusCode); e != nil {
		var oerr ovhError
		_ = json.Unmarshal(respBody, &oerr)
		if oerr.Message != "" {
			return nil, fmt.Errorf("ovh api status %d (%s): %w", resp.StatusCode, oerr.Message, e)
		}
		return nil, fmt.Errorf("ovh api status %d: %w", resp.StatusCode, e)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ovh api unexpected status %d", resp.StatusCode)
	}
	return respBody, nil
}

var _ registrar.Registrar = (*Adapter)(nil)
