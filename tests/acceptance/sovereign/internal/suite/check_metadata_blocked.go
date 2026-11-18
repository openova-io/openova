package suite

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G10, the instance metadata address is blocked: census block D1.5,
// a dial from a Pod. The chart bp-network-policies (kit slot 26) installs the
// CiliumClusterwideNetworkPolicy baseline-deny-metadata, which denies egress
// to 169.254.169.254/32 for every Pod whose label
// network.openova.io/metadata-access is not "true".
//
// In every region the check creates a namespace net-metadata-<run> of its own
// and starts two dial Pods in it with the Dialer of check_policy_dials.go, on
// the Pod network (never the host network, which the policy does not select):
// one without the label, and one with the label set to "true", the exception
// of the policy. Each Pod first dials the cluster DNS service on TCP port 53
// and then 169.254.169.254 on TCP port 80, with busybox nc. The Pod without
// the label passes when its dial times out: the deny drops the packets. The
// Pod with the label passes when its dial connects: something answers at the
// address, so the timeout of the first Pod is the deny and not an address
// that answers nobody. A refused dial fails both: it reached the address. A
// Pod whose dial of the DNS service does not connect has no working network,
// and its step fails, so a Pod without a network never passes.
//
// Per region the check records one step that starts the Pods, one step per
// Pod, and one step that deletes the namespace if it was created, also when a
// step failed.
const (
	metadataBlockedID = "NET-G10"
	metadataAddress   = "169.254.169.254"
	metadataPort      = 80
	// MetadataAccessLabel is the label that exempts a Pod from the deny when its value is "true".
	MetadataAccessLabel = "network.openova.io/metadata-access"
)

type metadataBlockedCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(metadataBlockedCheck{kubectl: kube.Region.Run}) }

func (metadataBlockedCheck) ID() string { return metadataBlockedID }

func (c metadataBlockedCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	run := strings.ToLower(NewRunID(time.Now()))
	ns := "net-metadata-" + run
	to := DialTarget{Address: metadataAddress, Port: metadataPort}
	pods := []struct {
		source DialSource
		expect string // the expect of a row of the dial table: deny or allow
		what   string
	}{
		{DialSource{Namespace: ns}, "deny", "a Pod without the label " + MetadataAccessLabel + " cannot reach " + to.String()},
		{DialSource{Namespace: ns, Labels: map[string]string{MetadataAccessLabel: "true"}}, "allow",
			"a Pod with the label " + MetadataAccessLabel + "=true reaches " + to.String() + ", so the deny above was measured"},
	}
	n := 0
	for _, region := range cc.Regions {
		d := &Dialer{Kubectl: c.kubectl, Region: region, Run: run}
		created := false
		n++
		started := checkStep(rec, metadataBlockedID, n, "D1.5: two dial Pods are Ready in the namespace "+ns+" of region "+region.ID, func(ev Evidence) error {
			ev["block"], ev["region"], ev["namespace"] = "D1.5", region.ID, ns
			if _, err := c.kubectl(region, ctx, nil, "create", "namespace", ns); err != nil {
				return err
			}
			created = true
			err := d.Start(ctx, []DialSource{pods[0].source, pods[1].source})
			ev["pods"] = d.Pods()
			return err
		})
		for _, p := range pods {
			if !started {
				break // no dial when a Pod is not Ready; the step above failed
			}
			n++
			checkStep(rec, metadataBlockedID, n, "D1.5: "+p.what+" in region "+region.ID, func(ev Evidence) error {
				ev["block"], ev["region"], ev["from"], ev["to"], ev["expect"] = "D1.5", region.ID, p.source.key(), to.String(), p.expect
				res, err := d.Dial(ctx, p.source, to)
				if err != nil {
					return err
				}
				ev["control"], ev["observed"], ev["output"] = res.Control, res.Outcome, res.Output
				return judgeDial(DialRow{Name: "metadata", From: p.source, To: to, Expect: p.expect}, res)
			})
		}
		if created {
			n++
			checkStep(rec, metadataBlockedID, n, "D1.5: the namespace "+ns+" of region "+region.ID+" is deleted", func(Evidence) error {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
				defer cancel()
				_, err := c.kubectl(region, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
				return err
			})
		}
	}
	return nil
}
