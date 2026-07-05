// OpenClaw workspace controller — the multi-tenant front door for the
// per-user agentic workspace (epic openova-io/openova#795, ADR-0003).
//
// What this binary is (and why it exists): bp-openclaw's chart ships the
// RBAC (pods create/get/list/watch/delete in the tenant namespace), the
// per-user-pod ConfigMap template, the OIDC/LLM env wiring, and the idle
// reaper config — but NONE of that does anything without a process that
// (a) authenticates the Organization end-user against the per-tenant
// Keycloak realm, (b) spawns one identity-blind runtime pod per active
// session from the mounted pod-template, (c) reverse-proxies the user's
// traffic to that pod, and (d) reaps idle pods. THIS is that process.
//
// Identity boundary (the whole point of the workspace-controller shape,
// per README.md): the controller is the ONLY component that ever sees a
// JWT from the Organization's own Keycloak realm. The runtime pods are
// identity-blind — they read only NEWAPI_BASE_URL + NEWAPI_KEY from the
// per-user `newapi-key-{uuid}` Secret (ADR-0003 §3.3). NewAPI therefore
// only ever talks to its own per-user keys, never to a cross-realm JWT.
//
// Everything operationally meaningful is an env var (Inviolable
// Principle 4 — never hardcode). The chart's controller-deployment.yaml
// supplies them all:
//
//   OIDC_ISSUER_URL        per-tenant Keycloak realm issuer (iss claim)
//   OIDC_CLIENT_ID         expected aud/azp on inbound JWTs
//   TENANT_NAMESPACE       namespace where per-user pods + secrets live
//   POD_TEMPLATE_CONFIGMAP name of the pod-template ConfigMap (mounted)
//   IDLE_TIMEOUT_MINUTES   reaper deletes pods idle longer than this
//   PORT                   listen port (default 8080)
//
// The pod-template is mounted at /etc/openclaw/pod-template.yaml (a
// volume from the ConfigMap in controller-deployment.yaml). The
// controller substitutes ${USER_UUID} and ${SECRET_NAME} per session.
//
// HTTP surface:
//   GET  /healthz   200 once config is parsed (liveness)
//   GET  /readyz    200 once the K8s api-server + OIDC JWKS are reachable
//   GET  /metrics   Prometheus text-format counters
//   GET  /          minimal landing page (browser confirm-loop)
//   ANY  /*         authenticate (Bearer JWT) → ensure pod → proxy
//
// Stdlib only (no external Go deps) so the build is hermetic and the
// Dockerfile mirrors the runtime's COPY-and-build pattern.

package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultPort        = "8080"
	saTokenPath        = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCACertPath       = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	podTemplatePath    = "/etc/openclaw/pod-template.yaml"
	podReadyTimeout    = 60 * time.Second
	upstreamPort       = 8080 // per-user runtime listens on :8080
	jwksRefreshTimeout = 10 * time.Second
)

// config is the fully-resolved controller configuration, sourced
// entirely from the environment (Inviolable Principle 4).
type config struct {
	port              string
	issuerURL         string
	internalIssuerURL string // #4739: in-cluster issuer base for discovery/JWKS (avoids NAT-EIP hairpin)
	clientID          string
	tenantNS          string
	idleTimeout       time.Duration
	k8sAPIHost        string
	k8sAPIPort        string
	saToken           string
	httpClient        *http.Client // talks to the in-cluster api-server (CA-pinned)
	requireAuth       bool         // false only when OIDC is unconfigured (smoke)
}

func loadConfig() (*config, error) {
	c := &config{
		port:      getenvDefault("PORT", defaultPort),
		issuerURL: strings.TrimRight(os.Getenv("OIDC_ISSUER_URL"), "/"),
		// #4739: when set (e.g. http://keycloak.keycloak.svc.cluster.local/realms/<realm>),
		// discovery+JWKS are fetched from this in-cluster base instead of the public
		// issuer. The public issuer's discovery doc returns an EXTERNAL jwks_uri (the
		// gateway EIP), which a pod cannot reach on kom4dc (the NAT-EIP hairpin) — so
		// readyz's JWKS leg 503s and the app never goes Ready. iss-claim validation
		// still uses the PUBLIC issuerURL. Mirrors oidc-gate's keycloakInternalURL.
		internalIssuerURL: strings.TrimRight(os.Getenv("OIDC_INTERNAL_ISSUER_URL"), "/"),
		clientID:          os.Getenv("OIDC_CLIENT_ID"),
		tenantNS:          os.Getenv("TENANT_NAMESPACE"),
		k8sAPIHost:        getenvDefault("KUBERNETES_SERVICE_HOST", ""),
		k8sAPIPort:        getenvDefault("KUBERNETES_SERVICE_PORT", "443"),
		requireAuth:       true,
	}

	idleMin := getenvDefault("IDLE_TIMEOUT_MINUTES", "30")
	n, err := strconv.Atoi(idleMin)
	if err != nil || n < 1 {
		return nil, fmt.Errorf("invalid IDLE_TIMEOUT_MINUTES=%q: must be a positive integer", idleMin)
	}
	c.idleTimeout = time.Duration(n) * time.Minute

	// The placeholder OIDC issuer (chart smoke-render default) means OIDC
	// is not really configured. The controller still starts and serves
	// /healthz so the chart's smoke install / probe path works, but it
	// refuses to spawn pods (returns 503 on user traffic) rather than
	// trusting unsigned requests. A real overlay sets a non-placeholder
	// issuer.
	if c.issuerURL == "" || c.issuerURL == "https://keycloak.example.local/realms/example" {
		c.requireAuth = false
		log.Printf("WARN: OIDC_ISSUER_URL is unset/placeholder (%q) — running in unconfigured mode; user traffic returns 503 until a real issuer is supplied", c.issuerURL)
	}

	// In-cluster ServiceAccount token + CA. Absent outside a cluster
	// (e.g. local smoke run) — the controller degrades to readyz=false
	// for the K8s leg but still serves /healthz.
	if tok, err := os.ReadFile(saTokenPath); err == nil {
		c.saToken = strings.TrimSpace(string(tok))
	}
	c.httpClient = buildAPIClient()

	return c, nil
}

func getenvDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// buildAPIClient returns an http.Client trusting BOTH the public system
// root pool AND the in-cluster api-server CA. The same client is used for
// two distinct TLS peers: the in-cluster api-server (signed by the cluster
// CA at saCACertPath) and the PUBLIC OIDC issuer (auth.<sovereign-fqdn>,
// signed by Let's Encrypt). Earlier this REPLACED the system pool with
// only the cluster CA (`RootCAs = clusterPool`), which broke the OIDC
// discovery/JWKS fetch with `x509: certificate signed by unknown
// authority` because Go does not merge the system roots once RootCAs is
// set (#4407). We now clone the system pool and APPEND the cluster CA so
// both peers verify. Off-cluster (no CA file) the system pool stands
// alone, exactly as before.
func buildAPIClient() *http.Client {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	tlsCfg.RootCAs = buildRootPool()
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}

// buildRootPool returns a cert pool that trusts the public system roots
// (so the Let's-Encrypt-signed OIDC issuer verifies) with the in-cluster
// api-server CA appended (so api-server calls verify too).
//
//	system roots present → cloned pool + cluster CA appended
//	system roots absent   → fresh pool with just the cluster CA
//	neither available     → nil, so crypto/tls falls back to its default
//	                        (the host system roots) rather than trusting
//	                        nothing
func buildRootPool() *x509.CertPool {
	var clusterCA []byte
	if ca, err := os.ReadFile(saCACertPath); err == nil {
		clusterCA = ca
	}
	return rootPoolFrom(clusterCA)
}

// rootPoolFrom builds the trust pool from the (optional) in-cluster CA
// PEM, cloning the system root pool first so the public LE issuer stays
// trusted. Split out from buildRootPool for testability (the CA PEM is
// passed in rather than read from the const SA path).
func rootPoolFrom(clusterCAPEM []byte) *x509.CertPool {
	pool, err := x509.SystemCertPool()
	haveSystem := err == nil && pool != nil
	if !haveSystem {
		// SystemCertPool can fail on a scratch image with no CA bundle.
		// Start from an empty pool so we can still add the cluster CA.
		pool = x509.NewCertPool()
	}
	haveClusterCA := false
	if len(clusterCAPEM) > 0 {
		// AppendCertsFromPEM is additive — the system roots already in the
		// cloned pool are preserved, the cluster CA is added on top.
		haveClusterCA = pool.AppendCertsFromPEM(clusterCAPEM)
	}
	if !haveSystem && !haveClusterCA {
		// Nothing to seed the pool with — returning an empty pool would
		// trust NO root. Return nil so crypto/tls uses the host default.
		return nil
	}
	return pool
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("FATAL: %v", err)
	}

	verifier := newJWTVerifier(cfg.issuerURL, cfg.internalIssuerURL, cfg.clientID, cfg.httpClient)
	spawner := newPodSpawner(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/readyz", readyzHandler(cfg, verifier))
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/", rootHandler(cfg, verifier, spawner))

	// Idle reaper — background loop deleting per-user pods idle past the
	// configured timeout. Only runs when we have a usable api-server.
	if cfg.saToken != "" && cfg.k8sAPIHost != "" {
		go spawner.runReaper(context.Background())
	}

	addr := net.JoinHostPort("", cfg.port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("openclaw-controller listening on %s; issuer=%q tenantNS=%q idleTimeout=%s authRequired=%t",
		addr, cfg.issuerURL, cfg.tenantNS, cfg.idleTimeout, cfg.requireAuth)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("FATAL: server error: %v", err)
	}
}

// ─────────────────────────── HTTP handlers ───────────────────────────

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

// readyzHandler asserts the controller's external dependencies are
// reachable: the K8s api-server (for pod lifecycle) and the OIDC JWKS
// (for JWT validation). In unconfigured/off-cluster smoke mode it still
// reports ready so the chart's smoke probe passes.
func readyzHandler(cfg *config, v *jwtVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		// K8s api-server reachability (only asserted when we expect one).
		if cfg.saToken != "" && cfg.k8sAPIHost != "" {
			if err := pingAPIServer(cfg); err != nil {
				http.Error(w, fmt.Sprintf("readyz: api-server unreachable: %v", err), http.StatusServiceUnavailable)
				return
			}
		}
		// OIDC JWKS reachability (only when auth is required).
		if cfg.requireAuth {
			if err := v.ensureKeys(context.Background()); err != nil {
				http.Error(w, fmt.Sprintf("readyz: OIDC JWKS unreachable: %v", err), http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ready")
	}
}

func metricsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	mets.write(w)
}

func rootHandler(cfg *config, v *jwtVerifier, s *podSpawner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Browser landing page on bare GET / with no Authorization — lets
		// the operator confirm-loop the controller in a browser.
		if r.URL.Path == "/" && r.Header.Get("Authorization") == "" {
			landingPage(w)
			return
		}

		if !cfg.requireAuth {
			http.Error(w, "openclaw-controller is not yet configured for this Organization (OIDC issuer unset)", http.StatusServiceUnavailable)
			mets.inc(&mets.authUnconfigured)
			return
		}

		// Authenticate the inbound Organization end-user JWT.
		userUUID, err := authenticate(r, v)
		if err != nil {
			mets.inc(&mets.authFailures)
			http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		mets.inc(&mets.authSuccess)

		// Ensure a per-user runtime pod exists, then proxy to it.
		podIP, err := s.ensurePod(r.Context(), userUUID)
		if err != nil {
			mets.inc(&mets.spawnFailures)
			log.Printf("ensurePod(%s): %v", userUUID, err)
			http.Error(w, "could not provision your workspace pod: "+err.Error(), http.StatusBadGateway)
			return
		}

		proxyToPod(w, r, podIP)
	}
}

func landingPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html>
<html><head><title>OpenClaw workspace</title></head>
<body style="font-family:system-ui,sans-serif;max-width:680px;margin:2rem auto;color:#222">
<h1>OpenClaw workspace controller</h1>
<p>Multi-tenant agentic workspace. Authenticate via your Organization's
Keycloak realm; the controller spawns a private runtime pod for your
session and proxies your requests to it.</p>
<p>Send requests with <code>Authorization: Bearer &lt;your-OIDC-JWT&gt;</code>.</p>
<p>Health: <code>/healthz</code> · Readiness: <code>/readyz</code> · Metrics: <code>/metrics</code></p>
</body></html>
`)
}

// authenticate extracts + validates the bearer JWT and returns the
// user UUID (the `sub` claim, per ADR-0003 §3.3 the key to the per-user
// newapi-key-{sub} Secret).
func authenticate(r *http.Request, v *jwtVerifier) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", errors.New("missing Authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", errors.New("Authorization header must be a Bearer token")
	}
	raw := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	claims, err := v.verify(r.Context(), raw)
	if err != nil {
		return "", err
	}
	if claims.Subject == "" {
		return "", errors.New("token has no sub claim")
	}
	return claims.Subject, nil
}

func proxyToPod(w http.ResponseWriter, r *http.Request, podIP string) {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(podIP, strconv.Itoa(upstreamPort))}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// The runtime is identity-blind: strip the inbound Keycloak
			// JWT so it NEVER reaches the per-user pod or NewAPI. The pod
			// authenticates to NewAPI with its own mounted NEWAPI_KEY.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
		},
		ErrorLog: log.New(os.Stderr, "proxy: ", log.LstdFlags),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy: error forwarding %s %s to %s: %v", r.Method, r.URL.Path, podIP, err)
			http.Error(w, "workspace pod unreachable", http.StatusBadGateway)
		},
	}
	mets.inc(&mets.proxiedRequests)
	rp.ServeHTTP(w, r)
}

// ─────────────────────────── JWT verifier ────────────────────────────

type jwtClaims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  any    `json:"aud"` // string or []string
	AZP       string `json:"azp"`
	ExpiresAt int64  `json:"exp"`
	NotBefore int64  `json:"nbf"`
}

type jwtVerifier struct {
	issuerURL   string
	internalURL string // #4739: in-cluster base for discovery/JWKS; "" → use issuerURL
	clientID    string
	client      *http.Client

	mu       sync.RWMutex
	keys     map[string]*rsa.PublicKey // kid -> key
	jwksURL  string
	lastLoad time.Time
}

func newJWTVerifier(issuerURL, internalURL, clientID string, client *http.Client) *jwtVerifier {
	return &jwtVerifier{
		issuerURL:   issuerURL,
		internalURL: internalURL,
		clientID:    clientID,
		client:      client,
		keys:        map[string]*rsa.PublicKey{},
	}
}

type oidcDiscovery struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

// ensureKeys lazily loads (and refreshes if older than 5 min) the
// realm's JWKS from the OIDC discovery document.
func (v *jwtVerifier) ensureKeys(ctx context.Context) error {
	v.mu.RLock()
	fresh := time.Since(v.lastLoad) < 5*time.Minute && len(v.keys) > 0
	v.mu.RUnlock()
	if fresh {
		return nil
	}
	return v.loadKeys(ctx)
}

func (v *jwtVerifier) loadKeys(ctx context.Context) error {
	if v.issuerURL == "" {
		return errors.New("OIDC issuer URL not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, jwksRefreshTimeout)
	defer cancel()

	// 1. Discovery → JWKS URL.
	var jwksURL string
	if v.internalURL != "" {
		// #4739: an in-cluster issuer base is configured. Go DIRECTLY to the
		// realm's conventional JWKS path and SKIP discovery — the public
		// issuer's discovery doc advertises an EXTERNAL jwks_uri (the gateway
		// EIP), which a pod cannot reach on kom4dc (the NAT-EIP hairpin). This
		// mirrors oidc-gate's --skip-oidc-discovery + --oidc-jwks-url seam.
		jwksURL = v.internalURL + "/protocol/openid-connect/certs"
	} else {
		discURL := v.issuerURL + "/.well-known/openid-configuration"
		var disc oidcDiscovery
		if err := v.getJSON(ctx, discURL, &disc); err != nil {
			return fmt.Errorf("OIDC discovery %s: %w", discURL, err)
		}
		jwksURL = disc.JWKSURI
		if jwksURL == "" {
			// Keycloak's conventional JWKS path under the realm.
			jwksURL = v.issuerURL + "/protocol/openid-connect/certs"
		}
	}

	// 2. JWKS.
	var doc jwksDoc
	if err := v.getJSON(ctx, jwksURL, &doc); err != nil {
		return fmt.Errorf("JWKS fetch %s: %w", jwksURL, err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := jwkToRSA(k)
		if err != nil {
			log.Printf("WARN: skipping JWK kid=%s: %v", k.Kid, err)
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("JWKS contained no usable RSA keys")
	}

	v.mu.Lock()
	v.keys = keys
	v.jwksURL = jwksURL
	v.lastLoad = time.Now()
	v.mu.Unlock()
	return nil
}

func (v *jwtVerifier) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// verify validates the RS256 JWT signature against the realm JWKS and
// checks the standard claims (iss, aud/azp, exp, nbf), returning the
// decoded claims on success.
func (v *jwtVerifier) verify(ctx context.Context, token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed JWT (expected 3 segments)")
	}
	headerJSON, err := b64urlDecode(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if hdr.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported alg %q (only RS256)", hdr.Alg)
	}

	if err := v.ensureKeys(ctx); err != nil {
		return nil, fmt.Errorf("load signing keys: %w", err)
	}
	v.mu.RLock()
	key, ok := v.keys[hdr.Kid]
	v.mu.RUnlock()
	if !ok {
		// Unknown kid — force a refresh once (realm rotated keys).
		if err := v.loadKeys(ctx); err != nil {
			return nil, fmt.Errorf("refresh keys for kid %q: %w", hdr.Kid, err)
		}
		v.mu.RLock()
		key, ok = v.keys[hdr.Kid]
		v.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("no signing key for kid %q", hdr.Kid)
		}
	}

	signingInput := parts[0] + "." + parts[1]
	sig, err := b64urlDecode(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if err := verifyRS256(key, []byte(signingInput), sig); err != nil {
		return nil, fmt.Errorf("signature invalid: %w", err)
	}

	claimsJSON, err := b64urlDecode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	now := time.Now().Unix()
	if claims.ExpiresAt != 0 && now >= claims.ExpiresAt {
		return nil, errors.New("token expired")
	}
	if claims.NotBefore != 0 && now < claims.NotBefore {
		return nil, errors.New("token not yet valid")
	}
	if v.issuerURL != "" && strings.TrimRight(claims.Issuer, "/") != v.issuerURL {
		return nil, fmt.Errorf("issuer mismatch: token iss=%q want %q", claims.Issuer, v.issuerURL)
	}
	if v.clientID != "" && !audienceMatches(claims, v.clientID) {
		return nil, fmt.Errorf("audience mismatch: token not issued for client %q", v.clientID)
	}
	return &claims, nil
}

func audienceMatches(c jwtClaims, clientID string) bool {
	// Keycloak access tokens often carry the client in `azp` and a
	// generic `account` aud; accept a match on either azp or aud.
	if c.AZP == clientID {
		return true
	}
	switch a := c.Audience.(type) {
	case string:
		return a == clientID
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && s == clientID {
				return true
			}
		}
	}
	return false
}

func jwkToRSA(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := b64urlDecode(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	eBytes, err := b64urlDecode(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, errors.New("zero exponent")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}

func b64urlDecode(s string) ([]byte, error) {
	// JWT uses base64url without padding.
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.URLEncoding.DecodeString(s)
}

// ─────────────────────────── pod spawner ─────────────────────────────

type podSpawner struct {
	cfg      *config
	template string // raw pod-template (mounted ConfigMap), or "" if absent
}

func newPodSpawner(cfg *config) *podSpawner {
	s := &podSpawner{cfg: cfg}
	if data, err := os.ReadFile(podTemplatePath); err == nil {
		s.template = string(data)
	} else {
		log.Printf("WARN: pod-template not found at %s: %v — pod spawn disabled until the ConfigMap is mounted", podTemplatePath, err)
	}
	return s
}

// podName is the deterministic name for a user's session pod. ADR-0003
// keys per-user state by the OIDC sub claim (a UUID); a stable name lets
// us treat ensurePod idempotently (one pod per active user).
func podName(userUUID string) string {
	safe := strings.ToLower(userUUID)
	safe = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, safe)
	// RFC1123 label max is 63 chars; the "openclaw-user-" prefix is 14,
	// leaving 49 for the (already sanitized) user segment.
	const prefix = "openclaw-user-"
	if len(safe) > 63-len(prefix) {
		safe = safe[:63-len(prefix)]
	}
	return prefix + strings.Trim(safe, "-")
}

func secretName(userUUID string) string {
	return "newapi-key-" + userUUID
}

// ensurePod guarantees a runtime pod exists for the user and returns its
// IP. If a pod already exists and is Running, it's reused (and its
// last-seen annotation refreshed for the reaper); otherwise it's created
// from the mounted template and we wait for it to get an IP.
func (s *podSpawner) ensurePod(ctx context.Context, userUUID string) (string, error) {
	if s.template == "" {
		return "", errors.New("pod-template ConfigMap not mounted")
	}
	if s.cfg.saToken == "" || s.cfg.k8sAPIHost == "" {
		return "", errors.New("no in-cluster Kubernetes API access")
	}
	name := podName(userUUID)

	// Already exists?
	pod, err := s.getPod(ctx, name)
	if err == nil && pod != nil {
		if pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			_ = s.touchPod(ctx, name) // refresh idle timestamp; best-effort
			return pod.Status.PodIP, nil
		}
		if pod.Status.Phase == "Failed" || pod.Status.Phase == "Succeeded" {
			// Reap a terminated pod and recreate.
			_ = s.deletePod(ctx, name)
		} else {
			// Pending — wait for it to come up.
			return s.waitForIP(ctx, name)
		}
	}

	// Create from template.
	manifest := s.renderTemplate(userUUID)
	if err := s.createPod(ctx, manifest); err != nil {
		// Lost the create race? Re-fetch.
		if existing, gerr := s.getPod(ctx, name); gerr == nil && existing != nil {
			return s.waitForIP(ctx, name)
		}
		return "", fmt.Errorf("create pod: %w", err)
	}
	mets.inc(&mets.podsSpawned)
	return s.waitForIP(ctx, name)
}

func (s *podSpawner) renderTemplate(userUUID string) string {
	r := strings.NewReplacer(
		"${USER_UUID}", userUUID,
		"${SECRET_NAME}", secretName(userUUID),
	)
	out := r.Replace(s.template)
	// Stamp the reaper's last-seen annotation so a freshly-created pod
	// isn't immediately eligible for reaping.
	out = injectIdleAnnotation(out, time.Now().UTC().Format(time.RFC3339))
	return out
}

// injectIdleAnnotation inserts/updates the controller's last-seen
// annotation into the pod manifest's metadata. The template ConfigMap
// declares labels under metadata; we append an annotations block (or a
// single annotation) the reaper reads. Done textually to stay
// dependency-free, mirroring the template's ${VAR} substitution model.
func injectIdleAnnotation(manifest, ts string) string {
	const annKey = "catalyst.openova.io/openclaw-last-seen"
	// If the controller already added it (idempotent re-render), replace.
	if strings.Contains(manifest, annKey) {
		return manifest
	}
	// Insert an annotations block right after the first `metadata:` line.
	lines := strings.Split(manifest, "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "metadata:" {
			indent := ln[:len(ln)-len(strings.TrimLeft(ln, " "))]
			ann := indent + "  annotations:\n" +
				indent + "    " + annKey + ": \"" + ts + "\""
			out := append([]string{}, lines[:i+1]...)
			out = append(out, ann)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n")
		}
	}
	return manifest
}

// ─────────────────── Kubernetes REST plumbing ────────────────────────

type k8sPod struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
		Labels      map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Phase string `json:"phase"`
		PodIP string `json:"podIP"`
	} `json:"status"`
}

type k8sPodList struct {
	Items []k8sPod `json:"items"`
}

func (s *podSpawner) apiBase() string {
	host := net.JoinHostPort(s.cfg.k8sAPIHost, s.cfg.k8sAPIPort)
	return fmt.Sprintf("https://%s/api/v1/namespaces/%s/pods", host, s.cfg.tenantNS)
}

func (s *podSpawner) apiReq(ctx context.Context, method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.saToken)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (s *podSpawner) getPod(ctx context.Context, name string) (*k8sPod, error) {
	req, err := s.apiReq(ctx, http.MethodGet, s.apiBase()+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.cfg.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("get pod %s: HTTP %d: %s", name, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var pod k8sPod
	if err := json.NewDecoder(resp.Body).Decode(&pod); err != nil {
		return nil, err
	}
	return &pod, nil
}

func (s *podSpawner) createPod(ctx context.Context, manifestYAML string) error {
	// The api-server accepts YAML when Content-Type is application/yaml.
	req, err := s.apiReq(ctx, http.MethodPost, s.apiBase(), strings.NewReader(manifestYAML))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := s.cfg.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("conflict: %s", strings.TrimSpace(string(b)))
	}
	return fmt.Errorf("create pod: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

func (s *podSpawner) deletePod(ctx context.Context, name string) error {
	req, err := s.apiReq(ctx, http.MethodDelete, s.apiBase()+"/"+name, nil)
	if err != nil {
		return err
	}
	resp, err := s.cfg.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
		mets.inc(&mets.podsReaped)
		return nil
	}
	return fmt.Errorf("delete pod %s: HTTP %d", name, resp.StatusCode)
}

// touchPod refreshes the reaper's last-seen annotation via a strategic
// merge patch so an in-use pod isn't reaped.
func (s *podSpawner) touchPod(ctx context.Context, name string) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"catalyst.openova.io/openclaw-last-seen":%q}}}`, ts)
	req, err := s.apiReq(ctx, http.MethodPatch, s.apiBase()+"/"+name, strings.NewReader(patch))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	resp, err := s.cfg.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("touch pod %s: HTTP %d", name, resp.StatusCode)
	}
	return nil
}

func (s *podSpawner) listUserPods(ctx context.Context) ([]k8sPod, error) {
	u := s.apiBase() + "?labelSelector=" + url.QueryEscape("catalyst.openova.io/blueprint=bp-openclaw,app.kubernetes.io/component=per-user-pod")
	req, err := s.apiReq(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.cfg.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list pods: HTTP %d", resp.StatusCode)
	}
	var list k8sPodList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (s *podSpawner) waitForIP(ctx context.Context, name string) (string, error) {
	deadline := time.Now().Add(podReadyTimeout)
	for time.Now().Before(deadline) {
		pod, err := s.getPod(ctx, name)
		if err == nil && pod != nil && pod.Status.PodIP != "" &&
			(pod.Status.Phase == "Running" || pod.Status.Phase == "Pending") {
			if pod.Status.Phase == "Running" {
				return pod.Status.PodIP, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return "", fmt.Errorf("pod %s did not become Running within %s", name, podReadyTimeout)
}

// runReaper periodically deletes per-user pods whose last-seen annotation
// is older than the idle timeout.
func (s *podSpawner) runReaper(ctx context.Context) {
	interval := s.cfg.idleTimeout / 4
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("idle reaper started: timeout=%s sweepInterval=%s", s.cfg.idleTimeout, interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapOnce(ctx)
		}
	}
}

func (s *podSpawner) reapOnce(ctx context.Context) {
	pods, err := s.listUserPods(ctx)
	if err != nil {
		log.Printf("reaper: list pods: %v", err)
		return
	}
	cutoff := time.Now().Add(-s.cfg.idleTimeout)
	for _, p := range pods {
		seen := p.Metadata.Annotations["catalyst.openova.io/openclaw-last-seen"]
		if seen == "" {
			continue // never stamped → leave it (just created)
		}
		t, err := time.Parse(time.RFC3339, seen)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			log.Printf("reaper: deleting idle pod %s (last-seen %s)", p.Metadata.Name, seen)
			if derr := s.deletePod(ctx, p.Metadata.Name); derr != nil {
				log.Printf("reaper: delete %s: %v", p.Metadata.Name, derr)
			}
		}
	}
}

// ─────────────────────────── misc helpers ────────────────────────────

func pingAPIServer(cfg *config) error {
	host := net.JoinHostPort(cfg.k8sAPIHost, cfg.k8sAPIPort)
	u := fmt.Sprintf("https://%s/api/v1/namespaces/%s/pods?limit=1", host, cfg.tenantNS)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.saToken)
	resp, err := cfg.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("api-server HTTP %d", resp.StatusCode)
	}
	return nil
}

// ─────────────────────────── metrics ─────────────────────────────────

type metrics struct {
	mu               sync.Mutex
	authSuccess      int64
	authFailures     int64
	authUnconfigured int64
	podsSpawned      int64
	podsReaped       int64
	spawnFailures    int64
	proxiedRequests  int64
}

var mets = &metrics{}

func (m *metrics) inc(p *int64) {
	m.mu.Lock()
	*p++
	m.mu.Unlock()
}

func (m *metrics) write(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fmt.Fprintf(w, "# HELP openclaw_controller_auth_total Authenticated request outcomes.\n")
	fmt.Fprintf(w, "# TYPE openclaw_controller_auth_total counter\n")
	fmt.Fprintf(w, "openclaw_controller_auth_total{result=\"success\"} %d\n", m.authSuccess)
	fmt.Fprintf(w, "openclaw_controller_auth_total{result=\"failure\"} %d\n", m.authFailures)
	fmt.Fprintf(w, "openclaw_controller_auth_total{result=\"unconfigured\"} %d\n", m.authUnconfigured)
	fmt.Fprintf(w, "# HELP openclaw_controller_pods_spawned_total Per-user runtime pods created.\n")
	fmt.Fprintf(w, "# TYPE openclaw_controller_pods_spawned_total counter\n")
	fmt.Fprintf(w, "openclaw_controller_pods_spawned_total %d\n", m.podsSpawned)
	fmt.Fprintf(w, "# HELP openclaw_controller_pods_reaped_total Per-user runtime pods deleted (idle or terminated).\n")
	fmt.Fprintf(w, "# TYPE openclaw_controller_pods_reaped_total counter\n")
	fmt.Fprintf(w, "openclaw_controller_pods_reaped_total %d\n", m.podsReaped)
	fmt.Fprintf(w, "# HELP openclaw_controller_spawn_failures_total Pod-spawn failures.\n")
	fmt.Fprintf(w, "# TYPE openclaw_controller_spawn_failures_total counter\n")
	fmt.Fprintf(w, "openclaw_controller_spawn_failures_total %d\n", m.spawnFailures)
	fmt.Fprintf(w, "# HELP openclaw_controller_proxied_requests_total Requests proxied to a per-user pod.\n")
	fmt.Fprintf(w, "# TYPE openclaw_controller_proxied_requests_total counter\n")
	fmt.Fprintf(w, "openclaw_controller_proxied_requests_total %d\n", m.proxiedRequests)
}

// verifyRS256 verifies a RSASSA-PKCS1-v1_5 SHA-256 signature (JWT alg
// RS256) of signingInput against the public key.
func verifyRS256(key *rsa.PublicKey, signingInput, sig []byte) error {
	sum := sha256.Sum256(signingInput)
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig)
}
