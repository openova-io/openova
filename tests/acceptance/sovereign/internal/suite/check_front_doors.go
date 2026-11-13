package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Check FND-G06, Front doors: each public host of the Sovereign answers an
// anonymous GET of its root, four times, over a certificate that a client
// trusts with the public roots of the machine it runs on. A redirect is not
// followed: the answer of the host itself is judged.
//
//   - An open host passes with 200, 301, 302, 303, 307 or 308.
//   - A closed host, one that must refuse anonymous access, passes with 401
//     or 403. Its step names it as closed.
//   - Every other answer fails, 404 among them, and so does a certificate
//     that is not publicly trusted or a host that does not answer.
//
// The name of the Sovereign is the value sovereignFQDN that step D0.1 of the
// discovery recorded. The check runs only in a cloud run: on the local pack no
// host has a publicly trusted certificate.
const frontDoorsID = "FND-G06"

// frontDoorSamples is how many times each host is asked.
const frontDoorSamples = 4

// frontDoorTimeout is the time limit of one request.
const frontDoorTimeout = 45 * time.Second

// frontDoor is one public host of the Sovereign, <Name>.<sovereign FQDN>.
type frontDoor struct {
	Name   string
	Closed bool // it must refuse anonymous access: 401 or 403
}

// frontDoors are the public hosts that the check asks: the hosts of layer
// L5 of scripts/sovereign-dod-verify.sh. Each of them serves a page or a
// redirect at its root, so none is closed.
var frontDoors = []frontDoor{
	{Name: "console"}, {Name: "api"}, {Name: "auth"}, {Name: "gitea"},
	{Name: "harbor"}, {Name: "bao"}, {Name: "marketplace"}, {Name: "pdns"},
}

// The answers that pass.
var (
	openCodes   = []int{200, 301, 302, 303, 307, 308}
	closedCodes = []int{401, 403}
)

type frontDoorsCheck struct {
	doors     []frontDoor       // nil: frontDoors
	transport http.RoundTripper // nil: http.DefaultTransport, with the public roots
}

func init() { RegisterCheck(frontDoorsCheck{}) }

func (frontDoorsCheck) ID() string { return frontDoorsID }

func (c frontDoorsCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	fqdn := sovereignFQDN(cc.Prior)
	if fqdn == "" {
		return errors.New("the run did not learn the name of the Sovereign: step D0.1 recorded no sovereignFQDN")
	}
	doors := c.doors
	if doors == nil {
		doors = frontDoors
	}
	client := &http.Client{
		Transport: c.transport,
		Timeout:   frontDoorTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for i, door := range doors {
		url := "https://" + door.Name + "." + fqdn + "/"
		want, codes := "200 or a redirect", openCodes
		if door.Closed {
			want, codes = "401 or 403 (closed to anonymous access)", closedCodes
		}
		rec.Step(frontDoorsID+"."+strconv.Itoa(i+1), url+" answers "+want+" with a trusted certificate, "+
			strconv.Itoa(frontDoorSamples)+" times", func(ev Evidence) error {
			ev["host"], ev["closed"] = door.Name+"."+fqdn, door.Closed
			samples := []Evidence{}
			var bad error
			for n := 1; n <= frontDoorSamples; n++ {
				code, issuer, err := askFrontDoor(ctx, client, url)
				sample := Evidence{"code": code, "issuer": issuer}
				switch {
				case err != nil:
					sample["error"] = err.Error()
					err = fmt.Errorf("sample %d of %s: %w", n, url, err)
				case !slices.Contains(codes, code):
					err = fmt.Errorf("sample %d of %s: HTTP %d; it must answer %s", n, url, code, want)
				}
				samples = append(samples, sample)
				if bad == nil {
					bad = err
				}
			}
			ev["samples"] = samples
			return bad
		})
	}
	return nil
}

// askFrontDoor makes one anonymous GET and returns the HTTP code and the
// issuer of the certificate the host showed.
func askFrontDoor(ctx context.Context, client *http.Client, url string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	issuer := ""
	if res.TLS != nil && len(res.TLS.PeerCertificates) > 0 {
		issuer = res.TLS.PeerCertificates[0].Issuer.String()
	}
	return res.StatusCode, issuer, nil
}

// sovereignFQDN is the value sovereignFQDN that step D0.1 recorded, or "".
func sovereignFQDN(prior []*Step) string {
	for _, s := range prior {
		if s != nil && s.Journey == "D0" && s.ID == "D0.1" {
			if v, ok := s.Evidence["sovereignFQDN"].(string); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}
