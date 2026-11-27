package suite

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SEC-G08, the injection negative. A value that the Secrets Store CSI
// driver (bootstrap-kit slot 15b, bp-secrets-store-csi) mounts into a Pod is
// in no Secret object, and no other service account can read it. In every
// region the check creates a namespace of its own and records five steps:
//
//  1. a canary, CANARY- and 40 random hexadecimal characters, is born in
//     OpenBao the way a tier B value is: a token of the namespace's
//     service account eso-reader logs in with role eso-ns (create only on
//     mounted/data/<namespace>/) and writes
//     mounted/data/<namespace>/sa/sec-g08-reader/canary/e1 with cas=0;
//  2. a Pod of service account sec-g08-reader mounts it through a
//     SecretProviderClass of provider openbao (role workload, audience
//     openbao, no secretObjects) and the mounted file holds the canary;
//  3. no Secret object of the region holds the canary in any data value;
//  4. the driver's service account cannot create a Secret cluster-wide
//     (kubectl auth can-i --all-namespaces prints no), the scope of the
//     ClusterRole the upstream chart renders when Secret sync is on;
//  5. a token of another service account, sec-g08-other, logged in with role
//     workload, gets 403 on the canary path, and a token of sec-g08-reader
//     reads it, so the 403 is measured and not a refusal of everything.
//
// Then it deletes the canary's key in OpenBao (whenever its put was sent)
// and the namespace (whenever it was created), also when a step failed. The OpenBao calls run inside an OpenBao server pod with
// `kubectl exec`, the script on standard input; no token and no canary is
// written to the evidence. The check runs only in a cloud run (runs_in of its
// gate in docs/ledger/gates.yaml), although the local pack's kit carries
// bp-openbao and slot 15b since SEC-218.
const secInjectID = "SEC-G08"

const (
	secInjectReader   = "sec-g08-reader"           // the service account whose Pod mounts the canary
	secInjectOther    = "sec-g08-other"            // a service account of the same namespace that must get 403
	secInjectSeeder   = "eso-reader"               // the namespace identity of role eso-ns
	secInjectObject   = "canary"                   // objectName in the SecretProviderClass and the file name
	secInjectDriverNS = "kube-system"              // namespace of slot 15b (the upstream default)
	secInjectDriverSA = "secrets-store-csi-driver" // service account of the upstream driver chart
	secInjectBaoAddr  = "http://openbao.openbao.svc.cluster.local:8200"
	secInjectMount    = "/var/run/secrets/openova"
	secInjectWait     = "180s" // the longest wait for the reader Pod to be Ready
)

// secInjectScript runs in an OpenBao server pod as `sh -s`, after the
// assignments jwt, role, op, path and (for op put) value. It prints name=value
// words and never a token or a value.
const secInjectScript = `class() {
	case "$1" in
	*"Code: 403"*) echo denied ;;
	*"No value found"*) echo absent ;;
	"") echo ok ;;
	*) echo error ;;
	esac
}
token=$(bao write -field=token auth/kubernetes/login role="$role" jwt="$jwt" 2>/dev/null) || token=""
if [ -z "$token" ]; then
	echo login=failed
	exit 0
fi
echo login=ok
export BAO_TOKEN="$token"
case "$op" in
put) echo "result=$(class "$(bao kv put -mount=mounted -cas=0 "$path" value="$value" 2>&1 >/dev/null)")" ;;
get) echo "result=$(class "$(bao kv get -mount=mounted "$path" 2>&1 >/dev/null)")" ;;
delete) echo "result=$(class "$(bao kv metadata delete -mount=mounted "$path" 2>&1 >/dev/null)")" ;;
esac
if bao token revoke -self >/dev/null 2>&1; then echo revoked=yes; else echo revoked=no; fi
`

type secInjectCheck struct {
	kubectl kubectlFunc
	canary  func() string
}

func init() { RegisterCheck(secInjectCheck{kubectl: kube.Region.Run, canary: newCanary}) }

func (secInjectCheck) ID() string { return secInjectID }

// newCanary is CANARY- and 40 random hexadecimal characters.
func newCanary() string {
	var b [20]byte
	_, _ = rand.Read(b[:])
	return "CANARY-" + hex.EncodeToString(b[:])
}

func (c secInjectCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 0
	step := func(name string, fn func(ev Evidence) error) bool {
		n++
		return checkStep(rec, secInjectID, n, name, fn)
	}
	for _, region := range cc.Regions {
		ns := "sec-g08-" + strings.ToLower(NewRunID(time.Now()))
		key := ns + "/sa/" + secInjectReader + "/" + secInjectObject + "/e1" // below mounted/data/
		canary := c.canary()
		// created: the namespace exists; written: the put was sent, so the key
		// may exist even when the answer was not ok. Both drive the cleanup.
		created, written, born := false, false, false
		born = step("a canary is born in OpenBao at mounted/data/"+key+" by role eso-ns in region "+region.ID, func(ev Evidence) error {
			ev["namespace"], ev["path"] = ns, "mounted/data/"+key
			if _, err := c.kubectl(region, ctx, nil, "create", "namespace", ns); err != nil {
				return err
			}
			created = true
			if err := c.serviceAccounts(ctx, region, ns); err != nil {
				return err
			}
			written = true
			return c.bao(ctx, region, ev, ns, secInjectSeeder, "", "eso-ns", "put", key, canary, "ok")
		})
		mounted := born && step("a Pod of "+secInjectReader+" mounts the canary through the CSI driver in region "+region.ID, func(ev Evidence) error {
			return c.mount(ctx, region, ev, ns, key, canary)
		})
		if mounted {
			step("no Secret object holds the canary in region "+region.ID, func(ev Evidence) error {
				return c.noSecret(ctx, region, ev, canary)
			})
		}
		step("the driver's service account cannot create a Secret in region "+region.ID, func(ev Evidence) error {
			return c.cannotCreate(ctx, region, ev)
		})
		if born {
			step("a token of "+secInjectOther+" gets 403 on the canary path and one of "+secInjectReader+" reads it in region "+region.ID, func(ev Evidence) error {
				other := Evidence{}
				if err := c.bao(ctx, region, other, ns, secInjectOther, "openbao", "workload", "get", key, "", "denied"); err != nil {
					ev["other"] = other
					return err
				}
				own := Evidence{}
				err := c.bao(ctx, region, own, ns, secInjectReader, "openbao", "workload", "get", key, "", "ok")
				ev["other"], ev["own"] = other, own
				return err
			})
		}
		if created {
			step("the canary and the namespace "+ns+" of region "+region.ID+" are deleted", func(ev Evidence) error {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
				defer cancel()
				var errs []error
				if written {
					errs = append(errs, c.bao(cleanup, region, ev, ns, secInjectSeeder, "", "eso-ns", "delete", key, "", "ok"))
				}
				_, err := c.kubectl(region, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
				return errors.Join(append(errs, err)...)
			})
		}
	}
	return nil
}

// serviceAccounts creates the three service accounts of the namespace.
func (c secInjectCheck) serviceAccounts(ctx context.Context, region kube.Region, ns string) error {
	for _, sa := range []string{secInjectSeeder, secInjectReader, secInjectOther} {
		if _, err := c.kubectl(region, ctx, nil, "-n", ns, "create", "serviceaccount", sa); err != nil {
			return err
		}
	}
	return nil
}

// bao logs in to OpenBao in a server pod with a 10-minute token of ns/sa
// (with the audience, when one is given) and runs op on the path below
// mounted/data/. It passes when the result is want.
func (c secInjectCheck) bao(ctx context.Context, region kube.Region, ev Evidence, ns, sa, audience, role, op, path, value, want string) error {
	out, err := c.kubectl(region, ctx, nil, "-n", secScopeBaoNS, "get", "pods", "-l", secScopeBaoPods,
		"--field-selector=status.phase=Running", "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return err
	}
	pods := strings.Fields(string(out))
	if len(pods) == 0 {
		return fmt.Errorf("no running OpenBao server pod in namespace %s", secScopeBaoNS)
	}
	args := []string{"-n", ns, "create", "token", sa, "--duration=10m"}
	if audience != "" {
		args = append(args, "--audience="+audience)
	}
	out, err = c.kubectl(region, ctx, nil, args...)
	if err != nil {
		return err
	}
	jwt := strings.TrimSpace(string(out))
	if strings.Count(jwt, ".") != 2 {
		return fmt.Errorf("the API server returned no token for %s/%s", ns, sa)
	}
	script := "jwt=" + shellQuote(jwt) + "\nrole=" + shellQuote(role) + "\nop=" + op + "\npath=" + shellQuote(path) +
		"\nvalue=" + shellQuote(value) + "\n" + secInjectScript
	out, err = c.kubectl(region, ctx, strings.NewReader(script), "-n", secScopeBaoNS, "exec", "-i", pods[0], "-c", secScopeBaoTarget, "--", "sh", "-s")
	if err != nil {
		return err
	}
	v := parseProbe(out)
	ev["pod"], ev["identity"], ev["role"], ev["op"], ev["login"], ev["result"] = pods[0], ns+"/"+sa, role, op, v.one("login"), v.one("result")
	switch {
	case v.one("login") != "ok":
		return fmt.Errorf("the token of %s/%s could not log in to OpenBao with role %s", ns, sa, role)
	case v.one("result") != want:
		return fmt.Errorf("%s of mounted/data/%s as %s/%s gave %q, not %q", op, path, ns, sa, v.one("result"), want)
	}
	return nil
}

// secInjectObjects is the SecretProviderClass and the reader Pod of ns as JSON.
func secInjectObjects(ns, key string) string {
	objects := fmt.Sprintf("- objectName: %s\n  secretPath: mounted/data/%s\n  secretKey: value\n", secInjectObject, key)
	spc := map[string]any{"apiVersion": "secrets-store.csi.x-k8s.io/v1", "kind": "SecretProviderClass",
		"metadata": map[string]any{"name": secInjectReader, "namespace": ns},
		"spec": map[string]any{"provider": "openbao", "parameters": map[string]any{
			"openbaoAddress": secInjectBaoAddr, "roleName": "workload", "audience": "openbao", "objects": objects}}}
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": secInjectReader, "namespace": ns},
		"spec": map[string]any{"serviceAccountName": secInjectReader, "restartPolicy": "Never",
			"terminationGracePeriodSeconds": 1, "automountServiceAccountToken": false, "enableServiceLinks": false,
			"containers": []any{map[string]any{"name": "reader", "image": secProbeImage, "command": []any{"sleep", "600"},
				"resources":       map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]any{"cpu": "100m", "memory": "32Mi"}},
				"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65534, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}},
				"volumeMounts":    []any{map[string]any{"name": "openova", "mountPath": secInjectMount, "readOnly": true}}}},
			"volumes": []any{map[string]any{"name": "openova", "csi": map[string]any{"driver": "secrets-store.csi.k8s.io", "readOnly": true,
				"volumeAttributes": map[string]any{"secretProviderClass": secInjectReader}}}}}}
	b, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{spc, pod}})
	return string(b)
}

// mount starts the reader Pod and compares its mounted file with the canary.
func (c secInjectCheck) mount(ctx context.Context, region kube.Region, ev Evidence, ns, key, canary string) error {
	if _, err := c.kubectl(region, ctx, strings.NewReader(secInjectObjects(ns, key)), "create", "-f", "-"); err != nil {
		return err
	}
	if _, err := c.kubectl(region, ctx, nil, "-n", ns, "wait", "--for=condition=Ready", "pod/"+secInjectReader, "--timeout="+secInjectWait); err != nil {
		return fmt.Errorf("the reader Pod is not Ready: %v", err)
	}
	out, err := c.kubectl(region, ctx, nil, "-n", ns, "exec", secInjectReader, "-c", "reader", "--", "cat", secInjectMount+"/"+secInjectObject)
	if err != nil {
		return err
	}
	ev["file"], ev["bytes"] = secInjectMount+"/"+secInjectObject, len(out)
	if strings.TrimRight(string(out), "\n") != canary {
		return fmt.Errorf("the mounted file holds %d bytes that are not the canary", len(out))
	}
	ev["matches_canary"] = true
	return nil
}

// noSecret decodes every data value of every Secret of the region and passes
// when none contains the canary. With no Secret listed nothing was measured.
func (c secInjectCheck) noSecret(ctx context.Context, region kube.Region, ev Evidence, canary string) error {
	out, err := c.kubectl(region, ctx, nil, "get", "secrets", "--all-namespaces", "-o", "json")
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			Metadata struct{ Namespace, Name string } `json:"metadata"`
			Data     map[string]string                `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return fmt.Errorf("the list of Secrets is not JSON: %v", err)
	}
	values, holders := 0, []string{}
	for _, s := range list.Items {
		for k, v := range s.Data {
			values++
			raw, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				return fmt.Errorf("the Secret %s/%s key %s is not base64: %v", s.Metadata.Namespace, s.Metadata.Name, k, err)
			}
			if strings.Contains(string(raw), canary) {
				holders = append(holders, s.Metadata.Namespace+"/"+s.Metadata.Name+" key "+k)
			}
		}
	}
	ev["secrets"], ev["values"], ev["holders"] = len(list.Items), values, holders
	switch {
	case values == 0:
		return fmt.Errorf("no Secret value was listed in region %s: nothing was measured", region.ID)
	case len(holders) > 0:
		return fmt.Errorf("the canary is in %d Secret values: %s", len(holders), strings.Join(holders, "; "))
	}
	return nil
}

// cannotCreate asks the API server whether the driver's service account may
// create a Secret cluster-wide (in all namespaces).
func (c secInjectCheck) cannotCreate(ctx context.Context, region kube.Region, ev Evidence) error {
	as := "system:serviceaccount:" + secInjectDriverNS + ":" + secInjectDriverSA
	ev["as"] = as
	if _, err := c.kubectl(region, ctx, nil, "-n", secInjectDriverNS, "get", "serviceaccount", secInjectDriverSA, "-o", "name"); err != nil {
		return fmt.Errorf("the driver's service account %s/%s is not there: %v", secInjectDriverNS, secInjectDriverSA, err)
	}
	// can-i exits 1 when it prints no; the printed word is the answer.
	out, _ := c.kubectl(region, ctx, nil, "auth", "can-i", "create", "secrets", "--as="+as, "--all-namespaces")
	answer := strings.TrimSpace(string(out))
	ev["answer"] = answer
	if answer != "no" {
		return fmt.Errorf("kubectl auth can-i create secrets as %s printed %q, not no", as, answer)
	}
	return nil
}
