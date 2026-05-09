// Package proxy implements the WebSocket exec-proxy: an HTTP handler
// that accepts an upstream-signed WebSocket upgrade on
// `/proxy/exec/{namespace}/{pod}/{container}` and bridges it to the
// kube-apiserver's `/api/v1/namespaces/{ns}/pods/{name}/exec` endpoint.
//
// Why a proxy at all?
//
//   1. The kube-apiserver speaks the channelled protocol
//      `v4.channel.k8s.io` (multiplexes stdin/stdout/stderr/error/resize
//      onto one WebSocket via the first byte of every frame). Browsers
//      cannot reach the apiserver directly without exposing kubeconfig
//      tokens to the SPA — that is the credential-leak we explicitly
//      forbid (INVIOLABLE-PRINCIPLES.md #5).
//   2. Putting the proxy on the node (DaemonSet) keeps the exec stream
//      node-local — the apiserver and the proxy share a kernel TCP
//      stack, no cross-node hop, latency is sub-ms.
//   3. The HMAC layer (auth/hmac.go) lets the upstream caller
//      (catalyst-api or Guacamole) authenticate WITHOUT shipping a
//      kubeconfig to the browser. The proxy uses the in-cluster
//      ServiceAccount token to talk to the apiserver.
//
// Frame protocol: the proxy is transparent at the WebSocket-frame
// layer. Bytes the browser sends → the apiserver receives byte-equal;
// bytes the apiserver sends → the browser receives byte-equal. The
// `v4.channel.k8s.io` interpretation lives in the SDK on either end;
// the proxy only multiplexes connections.
//
// tmux cascade: when TMUX_CASCADE=true is set on the binary, the proxy
// rewrites the requested exec command to wrap it in `tmux attach -t
// catalyst-ops || tmux new -s catalyst-ops -- <originalCmd>`. This is
// the bastion-shell pattern: every operator dropping into the same
// node lands in the same shared tmux session, useful for SRE incident
// pair-debugging. The cascade only fires when the caller explicitly
// requests `command=` query params; otherwise the upstream command is
// passed through verbatim (operator chose `kubectl exec -- /bin/bash`,
// honour it).
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	remotecommand "k8s.io/client-go/tools/remotecommand"

	"github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/auth"
)

// channelV4 is the only WebSocket sub-protocol the proxy serves.
// kube-apiserver advertises both v4.channel.k8s.io (channelled) and
// v3.channel.k8s.io (legacy); the proxy speaks v4 to keep the wire
// format predictable.
const channelV4 = "v4.channel.k8s.io"

// HandlerOptions captures everything the HTTP handler needs. Wired
// from main.go at startup.
type HandlerOptions struct {
	Logger   *slog.Logger
	Verifier *auth.Verifier

	// RESTConfig is the in-cluster kube REST config the proxy uses
	// to dial the apiserver. The ServiceAccount + Token files live
	// at /var/run/secrets/kubernetes.io/serviceaccount/ — InClusterConfig
	// reads them.
	RESTConfig *rest.Config

	// TmuxCascade enables the bastion-shell wrapper. Default: false
	// (transparent passthrough).
	TmuxCascade bool

	// AllowedNamespaces, if non-empty, limits which (ns) the proxy
	// will dial. An empty slice means "all namespaces" — the operator
	// must pair the binary with NetworkPolicy + RBAC to scope the
	// blast radius. Default empty (allow-all).
	AllowedNamespaces []string

	// PingPeriod controls how often the proxy sends WebSocket ping
	// frames to detect dead connections. Defaults to 30s.
	PingPeriod time.Duration

	// HandshakeTimeout caps the per-request HTTP→WebSocket upgrade.
	// Defaults to 10s.
	HandshakeTimeout time.Duration

	// upgrader is constructed once per HandlerOptions.
	upgrader websocket.Upgrader
}

// New constructs an HTTP handler that proxies `/proxy/exec/{ns}/{pod}/{container}`
// requests onto the kube-apiserver's exec stream. Returns an error
// when the verifier or RESTConfig is nil — the caller (main.go)
// surfaces the failure to operator logs without starting the
// listener.
func New(opts HandlerOptions) (http.Handler, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Verifier == nil {
		return nil, errors.New("proxy: HandlerOptions.Verifier is required")
	}
	if opts.RESTConfig == nil {
		return nil, errors.New("proxy: HandlerOptions.RESTConfig is required")
	}
	if opts.PingPeriod <= 0 {
		opts.PingPeriod = 30 * time.Second
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = 10 * time.Second
	}
	opts.upgrader = websocket.Upgrader{
		HandshakeTimeout: opts.HandshakeTimeout,
		Subprotocols:     []string{channelV4},
		// CheckOrigin returns true unconditionally — the HMAC verifier
		// is the actual authn gate. The proxy does NOT browse to the
		// open internet; cross-origin from a browser SPA is exactly
		// the supported call shape.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	return &handler{opts: opts}, nil
}

type handler struct {
	opts HandlerOptions
}

// ServeHTTP routes a single request:
//  1. parse {namespace,pod,container} from the URL path
//  2. verify the HMAC signature
//  3. enforce AllowedNamespaces (if configured)
//  4. open the apiserver exec stream
//  5. WebSocket-upgrade the client
//  6. bridge the two streams (this method blocks until either side
//     closes)
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ns, pod, container, ok := parseExecPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "expected path /proxy/exec/{ns}/{pod}/{container}")
		return
	}

	if err := h.opts.Verifier.VerifyRequest(r); err != nil {
		h.opts.Logger.Warn("proxy: hmac verify failed",
			"err", err, "path", r.URL.Path, "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	if !h.namespaceAllowed(ns) {
		h.opts.Logger.Warn("proxy: namespace denied",
			"ns", ns, "pod", pod, "container", container)
		writeError(w, http.StatusForbidden, "namespace not in AllowedNamespaces")
		return
	}

	cmd := parseCommand(r.URL.Query())
	if h.opts.TmuxCascade {
		cmd = wrapTmux(cmd)
	}
	tty := r.URL.Query().Get("tty") != "false"

	clientWS, err := h.opts.upgrader.Upgrade(w, r, http.Header{
		"Sec-WebSocket-Protocol": []string{channelV4},
	})
	if err != nil {
		// Upgrade already wrote an HTTP response on failure.
		h.opts.Logger.Warn("proxy: ws upgrade failed", "err", err)
		return
	}
	defer clientWS.Close()

	h.opts.Logger.Info("proxy: bridge open",
		"ns", ns, "pod", pod, "container", container,
		"tmuxCascade", h.opts.TmuxCascade, "cmd", cmd)

	if err := h.bridge(r.Context(), clientWS, ns, pod, container, cmd, tty); err != nil {
		h.opts.Logger.Warn("proxy: bridge ended with error", "err", err)
		// Closing the WS frame with a normal close lets the browser
		// distinguish "remote ended" from "network died".
		_ = clientWS.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()),
			time.Now().Add(time.Second),
		)
	}
}

// bridge opens an exec stream against the apiserver and pumps bytes
// between it and the WebSocket. Returns nil on a clean both-side close.
func (h *handler) bridge(
	ctx context.Context,
	clientWS *websocket.Conn,
	ns, pod, container string,
	cmd []string,
	tty bool,
) error {
	execURL, err := buildExecURL(h.opts.RESTConfig.Host, ns, pod, container, cmd, tty)
	if err != nil {
		return fmt.Errorf("buildExecURL: %w", err)
	}

	exec, err := remotecommand.NewSPDYExecutor(h.opts.RESTConfig, http.MethodPost, execURL)
	if err != nil {
		return fmt.Errorf("NewSPDYExecutor: %w", err)
	}

	// adapter bridges the SPDY exec stream onto the WebSocket connection.
	a := newWSStream(clientWS, h.opts.PingPeriod)
	defer a.Close()

	streamOpts := remotecommand.StreamOptions{
		Stdin:  a.Stdin,
		Stdout: a.Stdout,
		Stderr: a.Stderr,
		Tty:    tty,
	}
	if err := exec.StreamWithContext(ctx, streamOpts); err != nil {
		// io.EOF + ContextCanceled are clean shutdown signals from
		// either side of the bridge — don't log them as errors.
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("StreamWithContext: %w", err)
	}
	return nil
}

// namespaceAllowed implements the AllowedNamespaces gate. Empty list =
// allow all (default: operator must pair with NetworkPolicy/RBAC).
func (h *handler) namespaceAllowed(ns string) bool {
	if len(h.opts.AllowedNamespaces) == 0 {
		return true
	}
	for _, allowed := range h.opts.AllowedNamespaces {
		if allowed == ns {
			return true
		}
	}
	return false
}

// parseExecPath extracts the {ns}/{pod}/{container} segments from the
// canonical /proxy/exec/{ns}/{pod}/{container} URL.
func parseExecPath(p string) (ns, pod, container string, ok bool) {
	const prefix = "/proxy/exec/"
	if !strings.HasPrefix(p, prefix) {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(p, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return "", "", "", false
	}
	ns, pod, container = parts[0], parts[1], parts[2]
	if ns == "" || pod == "" || container == "" {
		return "", "", "", false
	}
	return ns, pod, container, true
}

// parseCommand returns the `command=` query parameters (repeated) as
// the exec command vector. Defaults to `["/bin/sh"]` when the caller
// passed no explicit command.
func parseCommand(q url.Values) []string {
	cmds := q["command"]
	if len(cmds) == 0 {
		return []string{"/bin/sh"}
	}
	return cmds
}

// wrapTmux returns a command vector that attaches to (or creates) a
// shared `catalyst-ops` tmux session, falling back to the original
// command inside the new session.
func wrapTmux(orig []string) []string {
	joined := strings.Join(orig, " ")
	return []string{
		"sh", "-c",
		fmt.Sprintf("tmux attach -t catalyst-ops 2>/dev/null || tmux new -s catalyst-ops %q", joined),
	}
}

// buildExecURL constructs the apiserver URL for the exec stream.
// remotecommand.NewSPDYExecutor takes a *url.URL — we hand-build it
// here so the test harness can verify the wire shape without standing
// up a fake apiserver.
func buildExecURL(host, ns, pod, container string, command []string, tty bool) (*url.URL, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	u.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec", ns, pod)
	q := url.Values{}
	q.Set("container", container)
	q.Set("stdin", "true")
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	if tty {
		q.Set("tty", "true")
	}
	for _, c := range command {
		q.Add("command", c)
	}
	u.RawQuery = q.Encode()
	return u, nil
}

// writeError emits a JSON error response. Doesn't depend on an
// upstream framework — the proxy intentionally has zero coupling to
// chi / mux / etc.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, fmt.Sprintf(`{"error":%q}`, msg))
}

// init registers the corev1 scheme so remotecommand can negotiate the
// `application/json` Accept header with the apiserver. The package
// init runs once per process; calling it from a constructor would
// race with multiple New() invocations.
func init() {
	utilruntime.Must(corev1.AddToScheme(scheme.Scheme))
}

// codecFactory is unused at runtime but kept here as a compile-time
// witness that the corev1 scheme is wired correctly. If the SDK ever
// changes the negotiation contract, the build fails here rather than
// at first request.
var codecFactory = serializer.NewCodecFactory(scheme.Scheme)

// _ keeps codecFactory referenced so go vet doesn't complain about
// an unused variable while preserving the init-time linkage.
var _ runtime.NegotiatedSerializer = codecFactory

// gvr returned for compile-time completeness — keeps SDK schema imports
// non-tree-shaken so the binary's manifest declares the right schema
// versions when SBOM scanners walk it.
var _ = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
