package gitops

// The chart source and the pull Secret of every bp-* HelmRepository this
// module emits come from the variables document of the Sovereign (SOV-183):
// the ConfigMap flux-system/sovereign-variables of contract C5
// (docs/contracts/c5-upstream-variables.schema.json). The service reads it
// once at start and then watches it, so a change of the document changes the
// next render without a restart.
//
// The lines catalogSourceLines renders:
//
//	url        oci://<REGISTRY_HOST>/<REGISTRY_PATH_CHARTS>
//	insecure   true, only when REGISTRY_SCHEME is http
//	secretRef  <REGISTRY_PULL_SECRET>
//
// A key that is absent, blank or not allowed gives the contract default:
// REGISTRY_PATH_CHARTS openova-io, REGISTRY_PULL_SECRET ghcr-pull,
// REGISTRY_SCHEME https. REGISTRY_HOST has no default. Without a usable
// REGISTRY_HOST the url is resolved as before (catalogOCIBase,
// cutover_aware_5527.go): a Sovereign whose document holds only the kit
// keys, and a cluster without the document, render what they rendered
// before. The cutover's explicit override CATALYST_LOCAL_REGISTRY_URL still
// comes first, because the hand-over still pivots the live sources by that
// stamp.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The ConfigMap that holds the variables document, and the defaults of the
// keys read here (contract C5).
const (
	variablesNamespace      = "flux-system"
	variablesName           = "sovereign-variables"
	defaultChartsPath       = "openova-io"
	defaultPullSecret       = "ghcr-pull"
	serviceAccountDir       = "/var/run/secrets/kubernetes.io/serviceaccount"
	watchTimeoutSeconds     = 300
	variablesRequestTimeout = 10 * time.Second
	maxWatchBackoff         = time.Minute
)

var (
	registryHostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]+)?$`)
	chartsPathRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)*$`)
	secretNameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
)

// variablesStore holds the last data of the ConfigMap that was read. Empty
// means not read yet, or read and absent; both resolve as before.
type variablesStore struct {
	mu   sync.RWMutex
	data map[string]string
}

var sovereignVariables variablesStore

func (s *variablesStore) set(data map[string]string) {
	c := make(map[string]string, len(data))
	for k, v := range data {
		c[k] = v
	}
	s.mu.Lock()
	s.data = c
	s.mu.Unlock()
}

func (s *variablesStore) get(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.data[key])
}

// catalogSource is what one emitted bp-* HelmRepository declares.
type catalogSource struct {
	URL        string
	Insecure   bool
	PullSecret string
}

// resolveCatalogSource is the pure core: get reads one key of the document,
// override is CATALYST_LOCAL_REGISTRY_URL and legacy the url resolved as
// before.
func resolveCatalogSource(get func(string) string, override string, legacy func() string) catalogSource {
	src := catalogSource{PullSecret: defaultPullSecret}
	if s := get("REGISTRY_PULL_SECRET"); secretNameRE.MatchString(s) {
		src.PullSecret = s
	}
	host := get("REGISTRY_HOST")
	if strings.TrimSpace(override) == "" && registryHostRE.MatchString(host) {
		path := get("REGISTRY_PATH_CHARTS")
		if !chartsPathRE.MatchString(path) {
			path = defaultChartsPath
		}
		src.URL = "oci://" + host + "/" + path
		src.Insecure = get("REGISTRY_SCHEME") == "http"
		return src
	}
	src.URL = legacy()
	return src
}

// catalogSourceLines renders the url, insecure and secretRef lines of an
// emitted bp-* HelmRepository spec, two spaces deep, without a final newline.
func catalogSourceLines() string {
	src := resolveCatalogSource(sovereignVariables.get, os.Getenv("CATALYST_LOCAL_REGISTRY_URL"), catalogOCIBase)
	var b strings.Builder
	b.WriteString("  url: " + src.URL + "\n")
	if src.Insecure {
		b.WriteString("  insecure: true\n")
	}
	b.WriteString("  secretRef:\n    name: " + src.PullSecret)
	return b.String()
}

// kubeAPI is the part of the Kubernetes API this file calls.
type kubeAPI struct {
	base   string
	token  func() (string, error)
	client *http.Client // reads, with a time limit
	stream *http.Client // watches; the server ends each after watchTimeoutSeconds
}

// inClusterAPI returns the API of the cluster the process runs in, as the
// service account of the Pod.
func inClusterAPI() (*kubeAPI, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a cluster")
	}
	pem, err := os.ReadFile(serviceAccountDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("read the cluster certificate authority: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("the cluster certificate authority holds no certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return &kubeAPI{
		base: "https://" + host + ":" + port,
		token: func() (string, error) {
			b, err := os.ReadFile(serviceAccountDir + "/token")
			return strings.TrimSpace(string(b)), err
		},
		client: &http.Client{Timeout: variablesRequestTimeout, Transport: transport},
		stream: &http.Client{Transport: transport},
	}, nil
}

func (a *kubeAPI) get(ctx context.Context, c *http.Client, query url.Values) (*http.Response, error) {
	query.Set("fieldSelector", "metadata.name="+variablesName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.base+"/api/v1/namespaces/"+variablesNamespace+"/configmaps?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	tok, err := a.token()
	if err != nil {
		return nil, fmt.Errorf("read the service account token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// configMap is the part of a ConfigMap this file reads.
type configMap struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

// variablesWatch keeps store equal to the ConfigMap.
type variablesWatch struct {
	api    *kubeAPI
	store  *variablesStore
	onRead func() // called after each change of store; tests wait on it
}

func (w *variablesWatch) apply(data map[string]string) {
	w.store.set(data)
	if w.onRead != nil {
		w.onRead()
	}
}

// list reads the ConfigMap into the store and returns the resource version
// to watch from. An absent ConfigMap is an empty document.
func (w *variablesWatch) list(ctx context.Context) (string, error) {
	resp, err := w.api.get(ctx, w.api.client, url.Values{})
	if err != nil {
		return "", fmt.Errorf("list %s/%s: %w", variablesNamespace, variablesName, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var l struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Items []configMap `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return "", fmt.Errorf("list %s/%s: %w", variablesNamespace, variablesName, err)
	}
	var data map[string]string
	if len(l.Items) > 0 {
		data = l.Items[0].Data
	}
	w.apply(data)
	return l.Metadata.ResourceVersion, nil
}

// errRelist means the watch cannot go on from its resource version.
var errRelist = errors.New("the watch must start again from a new list")

// watch follows the ConfigMap from resource version rv until the server ends
// the watch. It returns the last resource version it saw.
func (w *variablesWatch) watch(ctx context.Context, rv string) (string, error) {
	q := url.Values{}
	q.Set("watch", "true")
	q.Set("resourceVersion", rv)
	q.Set("allowWatchBookmarks", "true")
	q.Set("timeoutSeconds", fmt.Sprint(watchTimeoutSeconds))
	resp, err := w.api.get(ctx, w.api.stream, q)
	if err != nil {
		return rv, fmt.Errorf("watch %s/%s: %w", variablesNamespace, variablesName, err)
	}
	defer func() { _ = resp.Body.Close() }()
	dec := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var ev struct {
			Type   string          `json:"type"`
			Object json.RawMessage `json:"object"`
		}
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return rv, nil
			}
			return rv, fmt.Errorf("watch %s/%s: %w", variablesNamespace, variablesName, err)
		}
		if ev.Type == "ERROR" {
			return "", errRelist
		}
		var cm configMap
		if err := json.Unmarshal(ev.Object, &cm); err != nil {
			return "", errRelist
		}
		if cm.Metadata.ResourceVersion != "" {
			rv = cm.Metadata.ResourceVersion
		}
		switch ev.Type {
		case "ADDED", "MODIFIED":
			w.apply(cm.Data)
		case "DELETED":
			w.apply(nil)
		}
	}
}

// run lists and watches until ctx ends. rv is the resource version of a list
// already done, or empty.
func (w *variablesWatch) run(ctx context.Context, rv string) {
	backoff := time.Second
	for ctx.Err() == nil {
		var err error
		if rv == "" {
			rv, err = w.list(ctx)
		}
		if err == nil {
			rv, err = w.watch(ctx, rv)
		}
		if err == nil {
			backoff = time.Second
			continue
		}
		if !errors.Is(err, errRelist) {
			slog.Warn("provisioning/gitops: the variables document could not be read; the last one read stays in use", "error", err)
		}
		rv = ""
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxWatchBackoff {
			backoff = maxWatchBackoff
		}
	}
}

// StartVariablesWatch reads the variables document once and then keeps it
// current until ctx ends. Outside a cluster it does nothing, and the chart
// sources resolve as before.
func StartVariablesWatch(ctx context.Context) {
	api, err := inClusterAPI()
	if err != nil {
		slog.Info("provisioning/gitops: the variables document is not read; chart sources resolve as before", "reason", err)
		return
	}
	w := &variablesWatch{api: api, store: &sovereignVariables}
	first, cancel := context.WithTimeout(ctx, variablesRequestTimeout)
	rv, err := w.list(first)
	cancel()
	if err != nil {
		slog.Warn("provisioning/gitops: the first read of the variables document failed; chart sources resolve as before until a read succeeds", "error", err)
		rv = ""
	}
	go w.run(ctx, rv)
}
