package sovereignobject

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/core/cmd/sovereign-ops/internal/ops"
)

// serviceAccount is where Kubernetes mounts the Pod's token and the
// cluster's certificate authority.
const serviceAccount = "/var/run/secrets/kubernetes.io/serviceaccount"

// Main runs sovereign-object against the cluster the Pod runs in. It takes
// no flags and no arguments.
func Main(ctx context.Context, args []string, _ io.Writer) ([]string, error) {
	if len(args) != 0 {
		return nil, errors.New("usage: sovereign-object")
	}
	c, err := inCluster()
	if err != nil {
		return nil, err
	}
	return Write(ctx, c)
}

// apiServer talks to the Kubernetes API with the Pod's service account.
type apiServer struct {
	base, token string
	client      *http.Client
}

func inCluster() (*apiServer, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	token, terr := os.ReadFile(serviceAccount + "/token")
	ca, cerr := os.ReadFile(serviceAccount + "/ca.crt")
	pool := x509.NewCertPool()
	if host == "" || port == "" || terr != nil || cerr != nil || !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("sovereign-object runs in a Pod of the region's cluster: no Kubernetes service address or service account")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return &apiServer{base: "https://" + net.JoinHostPort(host, port), token: strings.TrimSpace(string(token)),
		client: &http.Client{Transport: tr, Timeout: 30 * time.Second}}, nil
}

// Get returns the object at path, nil when the API server answers 404.
func (a *apiServer) Get(ctx context.Context, path string) (map[string]interface{}, error) {
	code, body, err := a.do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, nil
	}
	if err := status("GET", path, code, body); err != nil {
		return nil, err
	}
	// UseNumber keeps every number as the API server wrote it.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var obj map[string]interface{}
	if err := dec.Decode(&obj); err != nil {
		return nil, ops.Transient(fmt.Errorf("GET %s: %w", path, err))
	}
	return obj, nil
}

// Apply sends body as a server-side apply patch to path, under the field
// manager sovereign-ops, taking over fields another manager holds. A 404 means
// the kind is not served yet (its definition is still being installed), so
// the caller may retry.
func (a *apiServer) Apply(ctx context.Context, path string, body []byte) error {
	q := url.Values{"fieldManager": {FieldManager}, "force": {"true"}}
	code, out, err := a.do(ctx, http.MethodPatch, path+"?"+q.Encode(), "application/apply-patch+yaml", body)
	if err != nil {
		return err
	}
	if code == http.StatusNotFound {
		return ops.Transient(fmt.Errorf("PATCH %s: HTTP 404, the Sovereign kind is not served yet", path))
	}
	return status("PATCH", path, code, out)
}

func (a *apiServer) do(ctx context.Context, method, path, contentType string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, ops.Transient(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, nil, ops.Transient(fmt.Errorf("%s %s: %w", method, path, err))
	}
	return resp.StatusCode, out, nil
}

// status turns an answer that is not 2xx into an error: 429 and 5xx may be
// retried, every other code is a failure that names the API server's message.
func status(method, path string, code int, body []byte) error {
	if code >= 200 && code < 300 {
		return nil
	}
	var st struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &st)
	err := fmt.Errorf("%s %s: HTTP %d %s", method, path, code, st.Message)
	if code == http.StatusTooManyRequests || code >= 500 {
		return ops.Transient(err)
	}
	return err
}
