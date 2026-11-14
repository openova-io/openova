package seedrepo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/core/cmd/sovereign-ops/internal/ops"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
)

// serviceAccount is where Kubernetes mounts the Pod's token and the
// cluster's certificate authority.
const serviceAccount = "/var/run/secrets/kubernetes.io/serviceaccount"

// Main parses the flags of seed-repo and runs Seed against the cluster the
// Pod runs in and the Gitea server at --git-url.
func Main(ctx context.Context, args []string, stderr io.Writer) ([]string, error) {
	fs := flag.NewFlagSet("seed-repo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	region := fs.String("region", "", "region ID, the name of the region folder (required)")
	repo := fs.String("repo", "openova/sovereign-regions", "the Sovereign's repository, <owner>/<name>")
	gitURL := fs.String("git-url", "", "base URL of the Gitea server (required)")
	tokenFile := fs.String("git-token-file", "", "file that holds the Gitea access token (required)")
	err := fs.Parse(args)
	owner, name, _ := strings.Cut(*repo, "/")
	if err != nil || *region == "" || *gitURL == "" || *tokenFile == "" || fs.NArg() != 0 || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, errors.New("usage: seed-repo --region <id> --git-url <url> --git-token-file <file> [--repo <owner>/<name>]")
	}
	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(token)) == "" {
		return nil, fmt.Errorf("the token file %s is empty", *tokenFile)
	}
	c, err := inCluster()
	if err != nil {
		return nil, err
	}
	newWriter := func(fqdn string) (gitwrite.Writer, error) {
		return gitwrite.New(gitwrite.Config{BaseURL: *gitURL, Tokens: gitwrite.SharedTokenSource(strings.TrimSpace(string(token))),
			RobotDomain: "robots." + fqdn})
	}
	return Seed(ctx, c, newWriter, *region, gitwrite.Repo{Owner: owner, Name: name})
}

// apiServer reads objects from the Kubernetes API with the Pod's service
// account.
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
		return nil, errors.New("seed-repo runs in a Pod of the region's cluster: no Kubernetes service address or service account")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return &apiServer{base: "https://" + net.JoinHostPort(host, port), token: strings.TrimSpace(string(token)),
		client: &http.Client{Transport: tr, Timeout: 30 * time.Second}}, nil
}

// plurals maps the kinds seed-repo reads to their resource names.
var plurals = map[string]string{"ConfigMap": "configmaps", "Kustomization": "kustomizations"}

func (a *apiServer) Get(ctx context.Context, apiVersion, kind, namespace, name string) (map[string]interface{}, error) {
	prefix := "/apis/"
	if apiVersion == "v1" {
		prefix = "/api/"
	}
	path := prefix + apiVersion + "/namespaces/" + namespace + "/" + plurals[kind] + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, ops.Transient(err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, ops.Transient(fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	// UseNumber keeps every number as the API server wrote it.
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var obj map[string]interface{}
	if err := dec.Decode(&obj); err != nil {
		return nil, ops.Transient(fmt.Errorf("GET %s: %w", path, err))
	}
	return obj, nil
}
