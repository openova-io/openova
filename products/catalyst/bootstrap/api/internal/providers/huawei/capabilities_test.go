package huawei

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// stubCloud stands in for the cloud calls of the capabilities for one test.
func stubCloud(t *testing.T, quota func(n int) (int, int, error), sweep func(region string, protect map[string]struct{}, destructive bool) (int, error)) *[]string {
	t.Helper()
	origQuota, origSweep := vpcQuota, sweepVPCs
	t.Cleanup(func() { vpcQuota, sweepVPCs = origQuota, origSweep })
	var calls []string
	n := 0
	vpcQuota = func(_ *Provider, _ context.Context, ak, sk, projectID, region string) (int, int, error) {
		n++
		calls = append(calls, "quota "+ak+" "+sk+" "+projectID+" "+region)
		return quota(n)
	}
	sweepVPCs = func(_ *Provider, _ context.Context, ak, sk, projectID, region string, protect map[string]struct{}, destructive bool, progress func(string)) (int, error) {
		calls = append(calls, "sweep "+region)
		progress("deleted catalyst-0000aaaa-vpc")
		return sweep(region, protect, destructive)
	}
	return &calls
}

func preflightInput(creds map[string]string, regions int, progress func(string)) providers.PreflightInput {
	in := providers.PreflightInput{
		Spec:         providers.ProvisionSpec{DeploymentID: "aaaaaaaa11112222", Creds: providers.ProviderCreds{Raw: creds}},
		ProtectedIDs: map[string]struct{}{"aaaaaaaa": {}, "91dc0591": {}},
		Progress:     progress,
	}
	for i := 0; i < regions; i++ {
		in.Spec.Regions = append(in.Spec.Regions, providers.RegionSpec{Code: "r" + string(rune('a'+i))})
	}
	return in
}

func TestCapabilities_ReachedThroughTheRegistry(t *testing.T) {
	if _, ok, err := providers.As[providers.Preflighter](Name); !ok || err != nil {
		t.Errorf("As[Preflighter](%q) = %v, %v; want the adapter", Name, ok, err)
	}
	if _, ok, err := providers.As[providers.PostApplier](Name); !ok || err != nil {
		t.Errorf("As[PostApplier](%q) = %v, %v; want the adapter", Name, ok, err)
	}
	if _, ok, err := providers.As[providers.ApplyErrorClassifier](Name); !ok || err != nil {
		t.Errorf("As[ApplyErrorClassifier](%q) = %v, %v; want the adapter", Name, ok, err)
	}
}

// Over the cap, the reclaim runs destructive with the engine's protected
// prefixes, and a project still full after it is refused with the reading
// taken after the reclaim.
func TestPreflight_ReclaimsWithTheProtectedIDsThenRefuses(t *testing.T) {
	var gotProtect map[string]struct{}
	var gotDestructive bool
	calls := stubCloud(t, func(n int) (int, int, error) {
		if n == 1 {
			return 5, 5, nil
		}
		return 4, 5, nil
	}, func(_ string, protect map[string]struct{}, destructive bool) (int, error) {
		gotProtect, gotDestructive = protect, destructive
		return 1, nil
	})
	var msgs []string
	in := preflightInput(map[string]string{"access_key": "AK", "secret_key": "SK", "project_id": "P", "region": " me-east-216 "}, 2, func(m string) { msgs = append(msgs, m) })
	err := New().Preflight(context.Background(), in)
	want := "HCS VPC quota exhausted: project at 4/5 after orphan reclaim, this prov requests 2 more — a concurrent Sovereign genuinely occupies the project; wipe one before retrying"
	if err == nil || err.Error() != want {
		t.Fatalf("Preflight error = %v; want %q", err, want)
	}
	if !reflect.DeepEqual(gotProtect, in.ProtectedIDs) || !gotDestructive {
		t.Errorf("reclaim got protect=%v destructive=%v; want the engine's ProtectedIDs and destructive", gotProtect, gotDestructive)
	}
	if want := []string{"quota AK SK P me-east-216", "sweep me-east-216", "quota AK SK P me-east-216"}; !reflect.DeepEqual(*calls, want) {
		t.Errorf("cloud calls = %q; want %q", *calls, want)
	}
	if want := []string{
		"HCS VPC quota: used 5 / 5, requesting 2",
		"HCS VPC quota over budget (5/5, need 2) — reclaiming orphaned catalyst VPCs before failing",
		"vpc-reclaim: deleted catalyst-0000aaaa-vpc",
		"HCS orphan-VPC reclaim freed 1 VPC(s); re-reading quota",
		"HCS VPC quota after reclaim: used 4 / 5, requesting 2",
	}; !reflect.DeepEqual(msgs, want) {
		t.Errorf("progress = %q; want %q", msgs, want)
	}
}

// A reclaim that freed room lets the prov proceed; the region falls back to
// the adapter's default when the credentials name none.
func TestPreflight_ProceedsWhenTheReclaimFreedRoom(t *testing.T) {
	calls := stubCloud(t, func(n int) (int, int, error) {
		if n == 1 {
			return 5, 5, nil
		}
		return 3, 5, nil
	}, func(string, map[string]struct{}, bool) (int, error) { return 2, nil })
	if err := New().Preflight(context.Background(), preflightInput(map[string]string{"access_key": "AK", "secret_key": "SK", "project_id": "P"}, 2, nil)); err != nil {
		t.Fatalf("Preflight = %v; want nil", err)
	}
	if got := (*calls)[0]; got != "quota AK SK P "+defaultRegion {
		t.Errorf("first call = %q; want the default region %s", got, defaultRegion)
	}
}

// A failed quota read never blocks the prov and reclaims nothing (#4431);
// a project that already fits is not swept.
func TestPreflight_ProceedsWithoutAReclaim(t *testing.T) {
	for name, quota := range map[string]func(int) (int, int, error){
		"quota API error": func(int) (int, int, error) { return 0, 0, errors.New("VPC quota GET: status 503") },
		"project fits":    func(int) (int, int, error) { return 3, 5, nil },
	} {
		calls := stubCloud(t, quota, func(string, map[string]struct{}, bool) (int, error) {
			t.Errorf("%s: the reclaim ran", name)
			return 0, nil
		})
		var msgs []string
		if err := New().Preflight(context.Background(), preflightInput(map[string]string{"access_key": "AK"}, 2, func(m string) { msgs = append(msgs, m) })); err != nil {
			t.Errorf("%s: Preflight = %v; want nil", name, err)
		}
		if len(*calls) != 1 || len(msgs) != 1 {
			t.Errorf("%s: calls %q, progress %q; want one quota read and one line", name, *calls, msgs)
		}
	}
}

// The NAT EIP rotation gets the deployment, its credentials and region; its
// failure is reported and never fails the prov (Wave 5.93).
func TestPostApply_RotationFailureDoesNotFailTheProv(t *testing.T) {
	orig := rotateNATEIPs
	t.Cleanup(func() { rotateNATEIPs = orig })
	var got []string
	rotateNATEIPs = func(_ context.Context, provider, deploymentID, sovereignFQDN, ak, sk, projectID, region string, _ func(string)) (int, error) {
		got = []string{provider, deploymentID, sovereignFQDN, ak, sk, projectID, region}
		return 0, errors.New("huawei: access_key is required")
	}
	var msgs []string
	res, err := New().PostApply(context.Background(), providers.PostApplyInput{
		Spec: providers.ProvisionSpec{DeploymentID: "d1", SovereignFQDN: "t1.omani.works",
			Creds: providers.ProviderCreds{Raw: map[string]string{"access_key": "AK", "secret_key": "SK", "project_id": "P", "region": "me-east-216"}}},
		Progress: func(m string) { msgs = append(msgs, m) },
	})
	if err != nil || res.ReapplyNeeded {
		t.Fatalf("PostApply = %+v, %v; want no error and no re-apply", res, err)
	}
	if want := []string{Name, "d1", "t1.omani.works", "AK", "SK", "P", "me-east-216"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rotation args = %q; want %q", got, want)
	}
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], "EIP pre-flight check failed (non-fatal") {
		t.Errorf("progress = %q; want the non-fatal failure line", msgs)
	}
}

func TestClassifyApplyError_Matrix(t *testing.T) {
	const addrs = "Error: Common.0021\n  with huaweicloud_compute_instance.control_plane[0],\n  with huaweicloud_compute_instance.worker[\"me-east-215-a-3\"],\n"
	cases := []struct {
		stderr string
		want   providers.ApplyErrorClass
	}{
		{addrs, providers.ApplyErrorClass{Retryable: true, Reason: "HCS Common.0021 (CollectInfoTask-fail)",
			Addresses: []string{"control_plane[0]", "worker[\"me-east-215-a-3\"]"}, StopOnRepeat: true}},
		{"CollectInfoTask-fail: Sub job fail!", providers.ApplyErrorClass{Retryable: true, Reason: "HCS Common.0021 (CollectInfoTask-fail)"}},
		{"error allocating EIP: conflict in the request", providers.ApplyErrorClass{Retryable: true, Reason: "HCS EIP-conflict (quota / propagation)"}},
		{"error creating VPC: conflict in the request", providers.ApplyErrorClass{Retryable: true, Reason: "HCS VPC-conflict (quota / propagation)"}},
		{"error creating NAT gateway: conflict in the request", providers.ApplyErrorClass{Retryable: true, Reason: "HCS resource-conflict (quota / propagation)"}},
		{"VPC.2030 EIP not found", providers.ApplyErrorClass{Retryable: true, Reason: "HCS Common.0021 (CollectInfoTask-fail)"}},
		{"Provider produced inconsistent result after apply\n  with huaweicloud_compute_instance.worker[\"w1\"],", providers.ApplyErrorClass{Retryable: true,
			Reason: "HCS Common.0021 (CollectInfoTask-fail)", Addresses: []string{"worker[\"w1\"]"}}},
		{"error creating VPC: quota exceeded", providers.ApplyErrorClass{}},
		{"ELB.8959 listener port in use\n  with huaweicloud_compute_instance.control_plane[0],", providers.ApplyErrorClass{}},
	}
	for _, c := range cases {
		if got := New().ClassifyApplyError(c.stderr); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ClassifyApplyError(%q) = %+v; want %+v", c.stderr, got, c.want)
		}
	}
}
