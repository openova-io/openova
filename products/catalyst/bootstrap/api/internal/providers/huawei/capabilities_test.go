package huawei

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

func TestCapabilities_ReachedThroughTheRegistry(t *testing.T) {
	if _, ok, err := providers.As[providers.PostApplier](Name); !ok || err != nil {
		t.Errorf("As[PostApplier](%q) = %v, %v; want the adapter", Name, ok, err)
	}
	if _, ok, err := providers.As[providers.ApplyErrorClassifier](Name); !ok || err != nil {
		t.Errorf("As[ApplyErrorClassifier](%q) = %v, %v; want the adapter", Name, ok, err)
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
