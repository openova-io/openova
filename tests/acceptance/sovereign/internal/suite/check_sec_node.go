package suite

import (
	"context"
	"errors"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SEC-G02, node hygiene. On every control-plane node of every region
// the probe (sec_probe.go, namespace `sec-node-<run>`) reads three things,
// and the step of a node fails when one is wrong. The token in
// /var/lib/rancher/k3s/server/token (its part after `::server:`, or the whole
// file) must not be 64 lower-case hexadecimal characters, the form of a token
// that is the SHA-256 of identifiers; the probe prints its class, never the
// token. /etc/rancher/k3s/k3s.yaml must exist with mode 600. No file
// /var/lib/catalyst/*-secret.yaml or /var/lib/catalyst/flux-bootstrap.yaml
// may remain.
const secNodeID = "SEC-G02"

type secNodeCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(secNodeCheck{kubectl: kube.Region.Run}) }

func (secNodeCheck) ID() string { return secNodeID }

func (c secNodeCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	return secProbe{id: secNodeID, prefix: "sec-node", mode: "node", kubectl: c.kubectl,
		what:  "the k3s token is not of the derived form, the kubeconfig has mode 600 and no bootstrap file remains",
		judge: judgeNode}.run(ctx, cc, rec)
}

// judgeNode judges what the probe printed in the mode node. It names every fault.
func judgeNode(v probeValues, ev Evidence) error {
	token, mode, files := v.one("token"), v.one("kubeconfig_mode"), v["bootstrap_file"]
	ev["token"], ev["kubeconfig_mode"], ev["bootstrap_files"] = token, mode, files
	faults := []string{}
	switch token {
	case "other":
	case "hex64":
		faults = append(faults, "the server part of the k3s token is 64 lower-case hexadecimal characters, the form of a derived token")
	default:
		faults = append(faults, "the k3s token file holds no token ("+token+")")
	}
	if mode != "600" {
		faults = append(faults, "/etc/rancher/k3s/k3s.yaml has mode "+mode+", want 600")
	}
	if len(files) > 0 {
		faults = append(faults, "bootstrap files remain: "+strings.Join(files, ", "))
	}
	if v.one("bootstrap_files") == "" {
		faults = append(faults, "the probe did not finish")
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}
