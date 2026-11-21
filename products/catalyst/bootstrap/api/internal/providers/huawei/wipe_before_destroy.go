// wipe_before_destroy.go — CLD-116. On the wipes of 20 November 2026 the
// destroy stopped every time on the bucket (the OpenTofu provider's batch
// delete answered `MalformedXML`), the listeners ("conflict in the request")
// and the SNAT rule the address rotation made outside the state (`VPC.2016`);
// the purge then removed them with its own calls. These steps make the same
// calls before the destroy, one object at a time, read each back, and touch
// nothing outside the deployment. A failed read is never "gone".
package huawei

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// The keys of the steps in WipeResult.ProviderPurge, set on every wipe.
const (
	beforeDestroyBuckets   = "before_destroy_buckets"
	beforeDestroyListeners = "before_destroy_listeners"
	beforeDestroyNATRules  = "before_destroy_nat_rules"
)

// The bounds of the steps. Package variables only so tests run instantly.
var (
	beforeDestroyPoll         = 2 * time.Second   // between two reads of one object
	beforeDestroyBucketWait   = 5 * time.Minute   // the bucket step, the purge's bucket budget
	beforeDestroyBucketTries  = 3                 // a writer may add an object before the removal
	beforeDestroyListenerWait = 60 * time.Second  // one listener, until it reads as gone
	beforeDestroyNATRuleWait  = 120 * time.Second // one gateway, until it lists no rule
)

type beforeDestroyCloud struct {
	client                            *http.Client
	hw                                hwCreds
	region, sovereignFQDN, deployment string
	progress                          func(msg string)
}

// beforeDestroySteps run in order; each returns what it removed and read back
// as gone, and the error that stopped it.
var beforeDestroySteps = []struct {
	key string
	run func(ctx context.Context, c beforeDestroyCloud) ([]string, error)
}{
	{beforeDestroyBuckets, removeBucketBeforeDestroy},
	{beforeDestroyListeners, removeListenersBeforeDestroy},
	{beforeDestroyNATRules, removeNATRulesBeforeDestroy},
}

// removeBeforeDestroy sets each step's key in out.ProviderPurge; a step that
// stops appends "<key>: <error>" to out.Errors, and the rest run all the same.
func removeBeforeDestroy(ctx context.Context, c beforeDestroyCloud, out *providers.WipeResult) {
	for i, s := range beforeDestroySteps {
		removed, err := s.run(ctx, c)
		if removed == nil {
			removed = []string{}
		}
		out.ProviderPurge[s.key] = removed
		c.progress(fmt.Sprintf("before destroy, step %d/%d %s: removed %d %v", i+1, len(beforeDestroySteps), s.key, len(removed), removed))
		if err != nil {
			out.Errors = append(out.Errors, s.key+": "+err.Error())
		}
	}
}

func pauseBeforeDestroy(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(beforeDestroyPoll):
		return nil
	}
}

// readJSON reads url: gone only for a not-found answer; an answer that is
// not a success decoding into v is an error.
func readJSON(c beforeDestroyCloud, url string, v any) (gone bool, err error) {
	body, status, err := doSignedRequest(c.client, c.hw, http.MethodGet, url, nil)
	if err == nil && status == http.StatusNotFound {
		return true, nil
	}
	if err != nil || status >= 300 || json.Unmarshal(body, v) != nil {
		return false, fmt.Errorf("read: status %d %v %s", status, err, snippet(body, 160))
	}
	return false, nil
}

func joinFailed(removed, failed []string) ([]string, error) {
	if len(failed) > 0 {
		return removed, errors.New(strings.Join(failed, "; "))
	}
	return removed, nil
}

// removeBucketBeforeDestroy empties and removes the deployment's bucket with
// the purge's emptyAndRemoveOBSBucket, then reads it back as gone.
func removeBucketBeforeDestroy(ctx context.Context, c beforeDestroyCloud) ([]string, error) {
	bucket := BucketNameForSovereign(c.sovereignFQDN, c.deployment)
	mc, err := minio.New(obsEndpointFor(c.region), &minio.Options{Secure: true, Region: c.region, Transport: c.client.Transport,
		Creds: credentials.NewStaticV4(c.hw.AccessKey, c.hw.SecretKey, "")})
	if err != nil {
		return nil, fmt.Errorf("object storage client: %w", err)
	}
	bctx, cancel := context.WithTimeout(ctx, beforeDestroyBucketWait)
	defer cancel()
	present := func() (bool, error) {
		ok, err := mc.BucketExists(bctx, bucket)
		if err != nil && isNoSuchBucket(err) {
			return false, nil
		}
		return ok, err
	}
	if ok, err := present(); err != nil || !ok {
		if err != nil {
			return nil, fmt.Errorf("read bucket %s: %w", bucket, err)
		}
		return nil, nil
	}
	var last error
	for try := 0; try < beforeDestroyBucketTries; try++ {
		if last = emptyAndRemoveOBSBucket(bctx, mc, bucket); last != nil {
			continue
		}
		ok, err := present()
		if err == nil && !ok {
			return []string{bucket}, nil
		}
		last = fmt.Errorf("bucket %s is still read after its removal (read: %v)", bucket, err)
	}
	return nil, last
}

// removeListenersBeforeDestroy removes the listeners of the Sovereign's load
// balancers one at a time: each reads as gone, or its wait has ended, before
// the next is touched.
func removeListenersBeforeDestroy(ctx context.Context, c beforeDestroyCloud) ([]string, error) {
	lbs, err := listELBsByName(ctx, c.client, c.hw, c.region, c.sovereignFQDN)
	if err != nil {
		return nil, fmt.Errorf("list load balancers: %w", err)
	}
	base := fmt.Sprintf("%s/v3/%s/elb", endpointFor("elb", c.region), c.hw.ProjectID)
	var removed, failed []string
	for _, lb := range lbs {
		var got struct {
			LoadBalancer struct {
				Listeners []struct {
					ID string `json:"id"`
				} `json:"listeners"`
			} `json:"loadbalancer"`
		}
		if _, err := readJSON(c, base+"/loadbalancers/"+lb.ID, &got); err != nil {
			failed = append(failed, fmt.Sprintf("load balancer %s: %v", lb.Name, err))
		}
		for _, l := range got.LoadBalancer.Listeners {
			if err := removeListener(ctx, c, base+"/listeners/"+l.ID); err != nil {
				failed = append(failed, fmt.Sprintf("listener %s of %s: %v", l.ID, lb.Name, err))
			} else {
				removed = append(removed, lb.Name+"/"+l.ID)
			}
		}
	}
	return joinFailed(removed, failed)
}

// removeListener unsets the listener's default pool and deletes it (the
// order of the purge's cascadeDeleteELB) while it reads as present; a
// conflict or a failed read is retried until the wait ends.
func removeListener(ctx context.Context, c beforeDestroyCloud, url string) error {
	deadline := time.Now().Add(beforeDestroyListenerWait)
	for {
		var got struct {
			Listener struct {
				DefaultPoolID string `json:"default_pool_id"`
			} `json:"listener"`
		}
		gone, last := readJSON(c, url, &got)
		if gone {
			return nil
		}
		if last == nil {
			if got.Listener.DefaultPoolID != "" {
				_, _, _ = doSignedRequest(c.client, c.hw, http.MethodPut, url, []byte(`{"listener":{"default_pool_id":null}}`))
			}
			b, st, e := doSignedRequest(c.client, c.hw, http.MethodDelete, url, nil)
			last = fmt.Errorf("delete: status %d %v %s", st, e, snippet(b, 160))
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("still present after %s; last answer %v", beforeDestroyListenerWait, last)
		}
		if err := pauseBeforeDestroy(ctx); err != nil {
			return err
		}
	}
}

// removeNATRulesBeforeDestroy removes every SNAT and DNAT rule of the
// Sovereign's NAT gateways, until each gateway lists none.
func removeNATRulesBeforeDestroy(ctx context.Context, c beforeDestroyCloud) ([]string, error) {
	nats, err := listNATsByName(ctx, c.client, c.hw, c.region, c.sovereignFQDN)
	if err != nil {
		return nil, fmt.Errorf("list NAT gateways: %w", err)
	}
	var removed, failed []string
	for _, n := range nats {
		gone, err := removeGatewayRules(ctx, c, n)
		if removed = append(removed, gone...); err != nil {
			failed = append(failed, fmt.Sprintf("NAT gateway %s: %v", n.Name, err))
		}
	}
	return joinFailed(removed, failed)
}

// listGatewayRules lists one gateway's rules as "<kind>/<id>", never a rule
// that names another gateway. A listing answered not-found is empty (the
// cloud has no such listing); any other failed read is an error.
func listGatewayRules(c beforeDestroyCloud, base, gatewayID string) ([]string, error) {
	var rules []string
	for _, kind := range []string{"snat_rules", "dnat_rules"} {
		var top map[string]json.RawMessage
		var list []struct {
			ID           string `json:"id"`
			NATGatewayID string `json:"nat_gateway_id"`
		}
		if gone, err := readJSON(c, base+"/"+kind+"?nat_gateway_id="+gatewayID, &top); err != nil || gone {
			if err != nil {
				return nil, fmt.Errorf("%s %w", kind, err)
			}
			continue
		}
		if raw, ok := top[kind]; !ok || json.Unmarshal(raw, &list) != nil {
			return nil, fmt.Errorf("%s: the answer has no list %q", kind, kind)
		}
		for _, r := range list {
			if r.ID != "" && r.NATGatewayID == gatewayID {
				rules = append(rules, kind+"/"+r.ID)
			}
		}
	}
	return rules, nil
}

// removeGatewayRules deletes the listed rules on the nested path this cloud
// needs (cascadeDeleteNAT) and lists again, until the gateway lists none or
// the wait ends. A rule counts as removed once a later read omits it.
func removeGatewayRules(ctx context.Context, c beforeDestroyCloud, n hwNAT) ([]string, error) {
	base := fmt.Sprintf("%s/v2/%s", endpointFor("nat", c.region), c.hw.ProjectID)
	deadline := time.Now().Add(beforeDestroyNATRuleWait)
	seen, gone := map[string]bool{}, map[string]bool{}
	var order, removed []string
	for {
		rules, err := listGatewayRules(c, base, n.ID)
		if err == nil {
			now := map[string]bool{}
			for _, r := range rules {
				if now[r] = true; !seen[r] {
					seen[r], order = true, append(order, r)
				}
			}
			for _, r := range order {
				if !now[r] && !gone[r] {
					gone[r], removed = true, append(removed, n.Name+"/"+r)
				}
			}
			if len(rules) == 0 {
				return removed, nil
			}
			for _, r := range rules {
				_, _, _ = doSignedRequest(c.client, c.hw, http.MethodDelete, base+"/nat_gateways/"+n.ID+"/"+r, nil)
			}
		}
		if !time.Now().Before(deadline) {
			if err != nil {
				return removed, fmt.Errorf("its rules could not be read for %s: %w", beforeDestroyNATRuleWait, err)
			}
			return removed, fmt.Errorf("still lists %d rule(s) after %s: %v", len(rules), beforeDestroyNATRuleWait, rules)
		}
		if err := pauseBeforeDestroy(ctx); err != nil {
			return removed, err
		}
	}
}
