package lifecycle

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The upstream credential of a delivery.
//
// After cutover the Sovereign holds no credential for the upstream registry
// (cutover step 06 strips it), and its surfaces are UI, Git and API only: no
// sovereign-admin writes a Secret with kubectl. So the credential travels IN
// THE REQUEST, from the signed-in sovereign-admin over the console's TLS, as
// {"credential": {"registry": "ghcr.io", "username": ..., "password": ...}}
// (a token goes in password). The route signs in to the registry with it
// before anything is written, then makes the delivery's pull Secret itself:
// releasePullSecret in the cutover namespace, created with the field manager
// releaseFieldManager, labelled with the Job it is for and owned by it. The
// credential is in that Secret and in no other object of the route: not in the
// state, a ConfigMap, an event, a log line, a Job (the containers reference the
// Secret by name) or an answer.
//
// The Secret lives for the delivery only, and goes in every outcome:
//   - the release scripts delete it, with its copies in every follower region,
//     at every end of a delivery: the end of the prewarm, the start of
//     promote, a stage that fails or refuses, and the bootstrap's deliver
//     container whether the delivery completed or failed (its delivery Job's
//     Pod killed included);
//   - a bootstrap Job whose own Pod was killed runs none of that, and fails
//     (backoffLimit 0). The Secret is owned by that Job, so the garbage
//     collector deletes it when the Job is deleted: releaseTTLSeconds (three
//     days) after the Job ended, by its ttlSecondsAfterFinished;
//   - before that, the next request deletes it (removeReleasePullSecret), as no
//     release Job runs then. That is the primary's Secret only: copies the
//     killed Job's stage made in the followers stay until the next delivery's
//     end deletes the credential in every region.
//
// A read of the state (GET) changes nothing, this Secret included.
const (
	// releasePullSecret is the one name of the delivery's pull Secret: one
	// release Job runs at a time.
	releasePullSecret   = "sovereign-release-pull"
	releaseFieldManager = "catalyst-release"
	releaseJobLabel     = "catalyst.openova.io/release-job"
	// upstreamRegistry is the registry a delivery copies charts and images
	// from: the cutover chart's step 03 reads the auth entry of this host
	// (.auths["ghcr.io"]) from the pull Secret.
	upstreamRegistry = "ghcr.io"
	// registryCheckDeadline bounds the sign-in, inside the request's own
	// releaseReadTimeout, so that a slow registry leaves the writes time.
	registryCheckDeadline = 10 * time.Second
)

// registryCredential is the field credential of a delivery request.
type registryCredential struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// credentialProblem answers why the credential of a delivery cannot be used,
// before the registry is asked, or "".
func credentialProblem(c *registryCredential) (code, detail string) {
	const shape = `{"registry": "` + upstreamRegistry + `", "username": "<user>", "password": "<password or token>"}`
	if c == nil {
		return "registry-credential-missing", "a delivery copies charts and images from " + upstreamRegistry + ", and after cutover the Sovereign holds no credential for it (cutover step 06 strips it): send one in the request as credential " + shape +
			"; the route keeps it in the Secret " + releasePullSecret + " for this delivery only and deletes it in every region when the delivery ends; nothing was staged"
	}
	var missing []string
	for _, f := range []struct{ name, value string }{{"registry", c.Registry}, {"username", c.Username}, {"password", c.Password}} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return "registry-credential-missing", "the credential of the request has no " + strings.Join(missing, ", ") + ": send credential " + shape + "; nothing was staged"
	}
	if strings.ToLower(strings.TrimSpace(c.Registry)) != upstreamRegistry {
		return "registry-credential-registry", fmt.Sprintf("credential.registry is %q, and a delivery copies from %s: send the credential for %s; nothing was staged", c.Registry, upstreamRegistry, upstreamRegistry)
	}
	return "", ""
}

// errCredentialRefused is the registry's refusal of the credential.
type errCredentialRefused struct{ status int }

func (e *errCredentialRefused) Error() string {
	return fmt.Sprintf("%s refused the credential (HTTP %d)", upstreamRegistry, e.status)
}

// registryProbe signs in to a registry with a credential the way a client of
// the distribution API does: GET <base>/v2/ with the credential; a registry
// that takes it answers 200. A registry with token auth answers 401 with a
// Bearer challenge; the credential then asks the challenge's realm for a token,
// which a registry that takes it answers 200 and one that refuses it 401 or
// 403. It proves the sign-in only, not read access to the packages a release
// pins (the token is asked with the challenge's own scope): a credential that
// signs in but may not read them fails the prewarm, which names the package and
// deletes the credential. The token, the answers' bodies and the credential
// are never kept, logged or returned.
type registryProbe struct {
	client *http.Client
	base   string // https://<registry>; a test's server otherwise
}

func (p registryProbe) check(ctx context.Context, c registryCredential) error {
	ctx, cancel := context.WithTimeout(ctx, registryCheckDeadline)
	defer cancel()
	base := p.base
	if base == "" {
		base = "https://" + strings.ToLower(strings.TrimSpace(c.Registry))
	}
	code, challenge, err := p.get(ctx, base+"/v2/", c)
	if err != nil {
		return err
	}
	switch {
	case code == http.StatusOK:
		return nil
	case code != http.StatusUnauthorized:
		return fmt.Errorf("GET %s/v2/ answered HTTP %d", base, code)
	}
	realm, params, ok := bearerChallenge(challenge)
	if !ok {
		return &errCredentialRefused{status: code}
	}
	u, err := url.Parse(realm)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("%s names a token realm that is not https, and the credential is sent over TLS only", base)
	}
	q := u.Query()
	for _, k := range []string{"service", "scope"} {
		if v := params[k]; v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	code, _, err = p.get(ctx, u.String(), c)
	switch {
	case err != nil:
		return err
	case code == http.StatusOK:
		return nil
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return &errCredentialRefused{status: code}
	}
	return fmt.Errorf("the token realm of %s answered HTTP %d", base, code)
}

// get sends one GET with the credential as basic auth; the body is drained
// unread.
func (p registryProbe) get(ctx context.Context, target string, c registryCredential) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, "", err
	}
	req.SetBasicAuth(c.Username, c.Password)
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: registryCheckDeadline}
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return 0, "", fmt.Errorf("GET %s: %v", ue.URL, ue.Err)
		}
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
}

// bearerChallenge reads `Bearer realm="...",service="...",scope="..."`.
func bearerChallenge(h string) (realm string, params map[string]string, ok bool) {
	scheme, rest, found := strings.Cut(strings.TrimSpace(h), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", nil, false
	}
	params = map[string]string{}
	for rest != "" {
		var kv string
		rest = strings.TrimLeft(rest, " ,")
		k, after, hasEq := strings.Cut(rest, "=")
		if !hasEq {
			break
		}
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				break
			}
			kv, rest = after[1:1+end], after[2+end:]
		} else {
			kv, rest, _ = strings.Cut(after, ",")
		}
		params[strings.ToLower(strings.TrimSpace(k))] = kv
	}
	return params["realm"], params, params["realm"] != ""
}

// pullSecretFor is the delivery's pull Secret: a docker configuration with
// one auth entry, for the upstream registry, labelled with the Job it is for.
func pullSecretFor(ns, job, candidate string, c registryCredential) (*corev1.Secret, error) {
	cfg, err := json.Marshal(map[string]any{"auths": map[string]map[string]string{upstreamRegistry: {
		"username": c.Username, "password": c.Password,
		"auth": base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password)),
	}}})
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: releasePullSecret, Namespace: ns, Labels: map[string]string{
			"app.kubernetes.io/component": releaseComponent,
			releaseCandidateLabel:         candidate,
			releaseJobLabel:               job,
		}},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}, nil
}

// makeReleasePullSecret creates the delivery's pull Secret as the route's own
// field manager: the one Secret the release route writes (guard G5 allows this
// file one write call; the Secret's entry in the exception register is
// release-pull-credential).
func makeReleasePullSecret(ctx context.Context, cs kubernetes.Interface, s *corev1.Secret) error {
	_, err := cs.CoreV1().Secrets(s.Namespace).Create(ctx, s, metav1.CreateOptions{FieldManager: releaseFieldManager})
	return err
}

// removeReleasePullSecret deletes the delivery's pull Secret in namespace ns,
// when it exists and no release Job runs (one that runs may still read it).
// It answers whether it deleted one.
func removeReleasePullSecret(ctx context.Context, cs kubernetes.Interface, ns string) (bool, error) {
	if _, err := cs.CoreV1().Secrets(ns).Get(ctx, releasePullSecret, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if running, err := activeReleaseJob(ctx, cs, ns); err != nil || running != "" {
		return false, err
	}
	err := cs.CoreV1().Secrets(ns).Delete(ctx, releasePullSecret, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
