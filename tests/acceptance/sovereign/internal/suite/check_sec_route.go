package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Check SEC-G04, OpenBao system paths are not public: from the machine that
// runs the suite, each system path prefix that the public route of OpenBao
// closes answers with a redirect to the user interface, and the rest of the
// OpenBao API still answers from OpenBao.
//
// The host is bao.<sovereign FQDN>. The name of the Sovereign is the value
// sovereignFQDN that step D0.1 of the discovery recorded. Each path is asked
// four times with an anonymous GET, because the public address can send the
// requests to more than one region. A redirect is not followed.
//
//   - Step SEC-G04.1 is the control. GET /v1/sys/seal-status answers 200 with
//     a JSON body that holds the boolean "sealed". It proves that the host
//     answers from here and reaches OpenBao, so that a redirect on the other
//     paths is the route and not a host that redirects everything. It also
//     proves that the prefix /v1/sys/seal does not take /v1/sys/seal-status.
//   - Steps SEC-G04.2 to SEC-G04.17: each of the 15 closed prefixes, and the
//     path /v1/sys/generate-root/attempt below the first of them, answers 302
//     with the Location https://bao.<sovereign FQDN>/ui/ (the port 443 may be
//     written). Every other answer fails, the answers of OpenBao among them.
//
// GET is used on every path because it changes nothing in OpenBao when the
// route is open: a PUT to /v1/sys/generate-root/attempt would start a
// root-token generation. The route matches paths and not methods, so a GET
// proves what a PUT would prove. The check runs only in a cloud run: on the
// local pack no host has a publicly trusted certificate.
const baoRouteID = "SEC-G04"

// baoRouteSamples is how many times each path is asked.
const baoRouteSamples = 4

// baoRouteTimeout is the time limit of one request.
const baoRouteTimeout = 45 * time.Second

// baoRouteControlPath is the path that must still reach OpenBao.
const baoRouteControlPath = "/v1/sys/seal-status"

// baoClosedPrefixes are the path prefixes that the public route of OpenBao
// answers with a redirect (platform/openbao/chart/templates/httproute.yaml).
var baoClosedPrefixes = []string{
	"/v1/sys/generate-root", "/v1/sys/generate-recovery-token", "/v1/sys/unseal",
	"/v1/sys/seal", "/v1/sys/init", "/v1/sys/rekey", "/v1/sys/rekey-recovery-key",
	"/v1/sys/step-down", "/v1/sys/raw", "/v1/sys/storage", "/v1/sys/rotate",
	"/v1/sys/pprof", "/v1/sys/audit", "/v1/sys/config", "/v1/sys/replication",
}

// baoRoutePaths are the paths that must be redirected: the closed prefixes and
// one path below the first of them.
func baoRoutePaths() []string {
	return append(append([]string{}, baoClosedPrefixes...), "/v1/sys/generate-root/attempt")
}

type baoRouteCheck struct {
	transport http.RoundTripper // nil: http.DefaultTransport, with the public roots
}

func init() { RegisterCheck(baoRouteCheck{}) }

func (baoRouteCheck) ID() string { return baoRouteID }

func (c baoRouteCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	fqdn := sovereignFQDN(cc.Prior)
	if fqdn == "" {
		return errors.New("the run did not learn the name of the Sovereign: step D0.1 recorded no sovereignFQDN")
	}
	host := "bao." + fqdn
	client := &http.Client{
		Transport: c.transport,
		Timeout:   baoRouteTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	times := strconv.Itoa(baoRouteSamples) + " times"

	control := "https://" + host + baoRouteControlPath
	rec.Step(baoRouteID+".1", control+" answers 200 from OpenBao, "+times, func(ev Evidence) error {
		ev["host"] = host
		return baoSamples(ev, func(n int) (Evidence, error) {
			a, err := askBao(ctx, client, control)
			sample := Evidence{"code": a.code}
			switch {
			case err != nil:
				sample["error"] = err.Error()
				return sample, fmt.Errorf("sample %d of %s: %w", n, control, err)
			case a.code != http.StatusOK:
				return sample, fmt.Errorf("sample %d of %s: HTTP %d; it must answer 200 from OpenBao", n, control, a.code)
			case !a.sealStatus:
				return sample, fmt.Errorf("sample %d of %s: the body is not the seal status of OpenBao", n, control)
			}
			return sample, nil
		})
	})

	want := "https://" + host + "/ui/"
	for i, path := range baoRoutePaths() {
		target := "https://" + host + path
		rec.Step(baoRouteID+"."+strconv.Itoa(i+2), target+" answers 302 to "+want+", "+times, func(ev Evidence) error {
			ev["path"] = path
			return baoSamples(ev, func(n int) (Evidence, error) {
				a, err := askBao(ctx, client, target)
				sample := Evidence{"code": a.code, "location": a.location}
				switch {
				case err != nil:
					sample["error"] = err.Error()
					return sample, fmt.Errorf("sample %d of %s: %w", n, target, err)
				case a.code != http.StatusFound:
					return sample, fmt.Errorf("sample %d of %s: HTTP %d; it must answer 302 to %s", n, target, a.code, want)
				case !baoUIRedirect(a.location, host):
					return sample, fmt.Errorf("sample %d of %s: redirects to %q; it must redirect to %s", n, target, a.location, want)
				}
				return sample, nil
			})
		})
	}
	return nil
}

// baoSamples asks baoRouteSamples times, keeps every sample in ev["samples"]
// and returns the first error.
func baoSamples(ev Evidence, ask func(n int) (Evidence, error)) error {
	samples := []Evidence{}
	var bad error
	for n := 1; n <= baoRouteSamples; n++ {
		sample, err := ask(n)
		samples = append(samples, sample)
		if bad == nil {
			bad = err
		}
	}
	ev["samples"] = samples
	return bad
}

// baoAnswer is what one request returned.
type baoAnswer struct {
	code       int
	location   string
	sealStatus bool // the body is a JSON object with the boolean "sealed"
}

// askBao makes one anonymous GET.
func askBao(ctx context.Context, client *http.Client, target string) (baoAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return baoAnswer{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return baoAnswer{}, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if err != nil {
		return baoAnswer{code: res.StatusCode}, err
	}
	var status map[string]any
	sealed := false
	if json.Unmarshal(body, &status) == nil {
		_, sealed = status["sealed"].(bool)
	}
	return baoAnswer{code: res.StatusCode, location: res.Header.Get("Location"), sealStatus: sealed}, nil
}

// baoUIRedirect is true when location is https://<host>/ui/ or
// https://<host>:443/ui/.
func baoUIRedirect(location, host string) bool {
	u, err := url.Parse(location)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.User == nil && u.Hostname() == host &&
		(u.Port() == "" || u.Port() == "443") && u.Path == "/ui/" && u.RawQuery == "" && u.Fragment == ""
}
