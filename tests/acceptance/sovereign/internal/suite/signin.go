package suite

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/session"
)

// The suite's sign-in. scripts/acceptance-sovereign.sh and
// scripts/gate-parent-job.sh mint the owner's console session just before the
// run (sovereign-acceptance mint-session, lifetime 3 hours by default) and give
// it in CATALYST_SESSION, or the gate's secret ACCEPTANCE_CATALYST_SESSION
// gives one. The suite keeps it in memory only, in its console client
// (api.Client), and never prints it. A run that waits for a reboot window
// lasts longer than that: gate run 44952720479 reached NET-G07 3 h 16 min after
// its start and the console answered HTTP 401 unauthenticated.
//
// So the console client renews it (api.Renewal): on an answer HTTP 401 it mints
// a new session once, the way the scripts mint the first one, and sends the
// call once more. The new session has the issuer and the e-mail of the session
// it replaces and is signed with the Sovereign's own handover signer
// (CATALYST_HANDOVER_SIGNER_PATH of Deployment catalyst-system/catalyst-api),
// which `kubectl exec` reads from the first region that gives it. The key and
// the session stay in memory; an error names the region and the step only.

// renewTTL is the lifetime of a renewed session, the default of mint-session.
const renewTTL = 3 * time.Hour

// defaultSignerPath is the signer of catalyst-api when its env names none, as
// the scripts default it.
const defaultSignerPath = "/var/lib/catalyst/handover-jwt-private.pem"

// ownerRenewal renews previous from the regions that regions returns when the
// renewal runs (the discovery names them after the client is built).
func ownerRenewal(regions func() []kube.Region, previous string, kubectl kubectlFunc) *api.Renewal {
	return &api.Renewal{Mint: func(ctx context.Context) (string, error) {
		return mintLike(ctx, regions(), previous, kubectl)
	}}
}

// mintLike mints a session with the issuer and the e-mail of previous.
func mintLike(ctx context.Context, regions []kube.Region, previous string, kubectl kubectlFunc) (string, error) {
	issuer, email, err := sessionClaims(previous)
	if err != nil {
		return "", err
	}
	failed := []string{}
	for _, region := range regions {
		key, err := signerKey(ctx, region, kubectl)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		tok, err := session.Mint(key, session.Claims{Issuer: issuer, Email: email, TTL: renewTTL})
		if err != nil {
			return "", fmt.Errorf("[%s] %w", region.ID, err)
		}
		return tok, nil
	}
	if len(failed) == 0 {
		return "", errors.New("no region to read the session signer from")
	}
	return "", fmt.Errorf("no region gave the session signer: %s", strings.Join(failed, "; "))
}

// sessionClaims reads the issuer and the e-mail of a session. It reads them
// only: the console, not the suite, judges the signature.
func sessionClaims(tok string) (issuer, email string, err error) {
	const cannot = "the session of the run cannot be renewed: "
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", "", errors.New(cannot + "it is not a JWT")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", errors.New(cannot + "its claims are not base64url")
	}
	var c struct {
		Iss   string `json:"iss"`
		Email string `json:"email"`
		Sub   string `json:"sub"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return "", "", errors.New(cannot + "its claims are not JSON")
	}
	email = nonEmpty(c.Email, c.Sub)
	if c.Iss == "" || email == "" {
		return "", "", errors.New(cannot + "it names no issuer or no e-mail")
	}
	return c.Iss, email, nil
}

// signerKey reads the handover signer of catalyst-api in region.
func signerKey(ctx context.Context, region kube.Region, kubectl kubectlFunc) (*rsa.PrivateKey, error) {
	out, err := kubectl(region, ctx, nil, "get", "deployments", "-n", "catalyst-system", "--field-selector", "metadata.name=catalyst-api", "-o", "json")
	if err != nil {
		return nil, err
	}
	var l kube.List
	if err := json.Unmarshal(out, &l); err != nil {
		return nil, fmt.Errorf("[%s] the Deployment catalyst-system/catalyst-api is not JSON: %v", region.ID, err)
	}
	if len(l.Items) == 0 {
		return nil, fmt.Errorf("[%s] no Deployment catalyst-system/catalyst-api", region.ID)
	}
	path := ""
	for _, c := range l.Items[0].Objects("spec", "template", "spec", "containers") {
		for _, e := range c.Objects("env") {
			if e.Str("name") == "CATALYST_HANDOVER_SIGNER_PATH" && path == "" {
				path = e.Str("value")
			}
		}
	}
	pemBytes, err := kubectl(region, ctx, nil, "-n", "catalyst-system", "exec", "deploy/catalyst-api", "--", "cat", nonEmpty(path, defaultSignerPath))
	if err != nil {
		return nil, err
	}
	key, err := session.ParseKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("[%s] the session signer of catalyst-api: %v", region.ID, err)
	}
	return key, nil
}
