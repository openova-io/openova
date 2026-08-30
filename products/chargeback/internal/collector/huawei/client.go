package huawei

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
)

// Gateway error codes the Kom4DC API gateway and services return.
const (
	CodeNotPublished = "APIGW.0101" // 404: the API is not published on this gateway
	CodeNoAuth       = "APIGW.0301" // 401: missing or invalid authentication
)

// DefaultTimeout bounds one list call.
const DefaultTimeout = 15 * time.Second

// GatewayError is a non-2xx response with the gateway/service error envelope
// decoded when present. It never carries credentials.
type GatewayError struct {
	Status  int
	Code    string
	Message string
	Service string
}

func (e *GatewayError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: HTTP %d %s: %s", e.Service, e.Status, e.Code, truncate(e.Message, 200))
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Service, e.Status, truncate(e.Message, 200))
}

// Unauthorized reports whether the gateway or service rejected the credentials.
func (e *GatewayError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// NotPublished reports the 404 APIGW.0101 case.
func (e *GatewayError) NotPublished() bool {
	return e.Status == http.StatusNotFound && e.Code == CodeNotPublished
}

// Client executes signed requests against per-service, per-region endpoints.
type Client struct {
	HTTP     *http.Client
	Signer   Signer
	Template string // e.g. https://%s.%s.kom4dc.nationalcloud.om (service, region)
	Metrics  *metrics.Registry
}

// NewClient builds a client with the on-prem TLS posture requested.
func NewClient(template string, insecureTLS bool, timeout time.Duration, reg *metrics.Registry) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	}
	if insecureTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // on-prem CA, HUAWEI_INSECURE_TLS
	}
	if reg == nil {
		reg = metrics.Default
	}
	return &Client{HTTP: &http.Client{Timeout: timeout, Transport: tr}, Template: template, Metrics: reg}
}

// Endpoint renders the base URL for a service in a region.
func (c *Client) Endpoint(service, region string) string {
	return fmt.Sprintf(c.Template, service, region)
}

// Get performs one signed GET and decodes a 2xx JSON body into out (out may
// be nil). Non-2xx responses return *GatewayError.
func (c *Client) Get(ctx context.Context, creds Credentials, service, region, path string, query url.Values, out any) error {
	u := c.Endpoint(service, region) + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("%s: build request: %w", service, err)
	}
	if _, err := c.Signer.Sign(req, creds, nil); err != nil {
		return fmt.Errorf("%s: sign: %w", service, err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.count(service, "transport")
		return fmt.Errorf("%s: %w", service, sanitizeErr(err, creds))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		c.count(service, "read")
		return fmt.Errorf("%s: read body: %w", service, err)
	}
	c.count(service, fmt.Sprint(resp.StatusCode))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeGatewayError(service, resp.StatusCode, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: decode: %w", service, err)
	}
	return nil
}

func (c *Client) count(service, status string) {
	if c.Metrics == nil {
		return
	}
	c.Metrics.Inc("chargeback_cloud_api_calls_total", "Cloud API calls by service and HTTP status", map[string]string{"service": service, "status": status}, 1)
}

// decodeGatewayError parses the {"error_code","error_msg"} envelope (gateway
// and most services) and the nested {"error":{"code","message"}} form some
// services use.
func decodeGatewayError(service string, status int, body []byte) *GatewayError {
	ge := &GatewayError{Status: status, Service: service}
	var flat struct {
		Code string `json:"error_code"`
		Msg  string `json:"error_msg"`
		Err  *struct {
			Code string `json:"code"`
			Msg  string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &flat) == nil {
		ge.Code = flat.Code
		ge.Message = flat.Msg
		if ge.Code == "" && flat.Err != nil {
			ge.Code = flat.Err.Code
			ge.Message = flat.Err.Msg
		}
	}
	if ge.Code == "" && ge.Message == "" {
		ge.Message = strings.TrimSpace(truncate(string(body), 200))
	}
	return ge
}

// sanitizeErr strips the secret key from transport errors as a belt-and-braces
// measure; Go transport errors never include it, but a URL error could echo a
// query string, so the check is cheap insurance.
func sanitizeErr(err error, creds Credentials) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if creds.SecretKey != "" && strings.Contains(msg, creds.SecretKey) {
		return errors.New(strings.ReplaceAll(msg, creds.SecretKey, "[redacted]"))
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
