package crossregion

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kfake "k8s.io/client-go/kubernetes/fake"
)

const tokenKeySecret = "harbor-token-key"

// TestInputSync_OneRegistryTokenKeyForTheSovereign is #7908. On the fresh
// two-region installation of 15 November 2026 each region's Harbor signed
// registry tokens with its own key, while the registry's public name sent the
// requests of one push to both regions: a token from one region was dropped
// by the other and the push was refused. The follower must hold the
// primary's key pair, as cert-manager wrote it there (a TLS Secret).
func TestInputSync_OneRegistryTokenKeyForTheSovereign(t *testing.T) {
	ctx := context.Background()
	newPrimary := func() *kfake.Clientset {
		primary := kfake.NewSimpleClientset()
		seedSecret(t, primary, "harbor", tokenKeySecret, map[string]string{"tls.key": "primary-key", "tls.crt": "primary-crt", "ca.crt": "primary-crt"})
		s, err := primary.CoreV1().Secrets("harbor").Get(ctx, tokenKeySecret, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get primary Secret: %v", err)
		}
		s.Type = corev1.SecretTypeTLS
		if _, err := primary.CoreV1().Secrets("harbor").Update(ctx, s, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("set the primary Secret's type: %v", err)
		}
		return primary
	}

	t.Run("a-follower-without-a-key-receives-the-primarys", func(t *testing.T) {
		// The follower's chart issues no key; its core waits for this copy.
		primary, follower := newPrimary(), kfake.NewSimpleClientset()
		if _, err := follower.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "harbor"}}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create namespace: %v", err)
		}
		d := &Deliveries{}
		newSync(primary, follower, fakeRegion(t), d).Run(ctx)
		got, err := follower.CoreV1().Secrets("harbor").Get(ctx, tokenKeySecret, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("the follower has no harbor/%s after a pass (%v): its core cannot start, or keeps refusing the primary's tokens", tokenKeySecret, err)
		}
		if string(got.Data["tls.key"]) != "primary-key" || string(got.Data["tls.crt"]) != "primary-crt" {
			t.Errorf("follower key pair = %q/%q, want the primary's", got.Data["tls.key"], got.Data["tls.crt"])
		}
		if got.Type != corev1.SecretTypeTLS {
			t.Errorf("follower Secret type = %q, want %q as the primary's", got.Type, corev1.SecretTypeTLS)
		}
		if d.At("dep-7902", testRegion, "harbor/"+tokenKeySecret).IsZero() {
			t.Errorf("no delivery recorded for harbor/%s", tokenKeySecret)
		}
	})

	t.Run("a-follower-key-of-its-own-is-replaced", func(t *testing.T) {
		// A region whose role was not stamped issues its own key.
		primary, follower := newPrimary(), kfake.NewSimpleClientset()
		seedSecret(t, follower, "harbor", tokenKeySecret, map[string]string{"tls.key": "follower-own-key", "tls.crt": "follower-own-crt"})
		newSync(primary, follower, fakeRegion(t), &Deliveries{}).Run(ctx)
		if got, _ := secretValue(t, follower, "harbor", tokenKeySecret, "tls.key"); got != "primary-key" {
			t.Errorf("follower tls.key = %q, want the primary's", got)
		}
		if got, _ := secretValue(t, primary, "harbor", tokenKeySecret, "tls.key"); got != "primary-key" {
			t.Errorf("the primary's key changed to %q: the primary is the source and is never written", got)
		}
	})
}

// TestInputSync_OneUploadSecretAndCSRFKeyForTheSovereign: the registry's
// public name sends one client's requests to both regions, so a blob upload
// begun in one region continues in the other, and a CSRF token one region
// issued is checked by the other. The follower must hold the primary's
// REGISTRY_HTTP_SECRET and CSRF_KEY; its chart writes neither.
func TestInputSync_OneUploadSecretAndCSRFKeyForTheSovereign(t *testing.T) {
	ctx := context.Background()
	primary, follower := kfake.NewSimpleClientset(), kfake.NewSimpleClientset()
	seedSecret(t, primary, "harbor", "harbor-shared-keys", map[string]string{"REGISTRY_HTTP_SECRET": "primary-upload", "CSRF_KEY": "primary-csrf"})
	newSync(primary, follower, fakeRegion(t), &Deliveries{}).Run(ctx)
	for key, want := range map[string]string{"REGISTRY_HTTP_SECRET": "primary-upload", "CSRF_KEY": "primary-csrf"} {
		if got, ok := secretValue(t, follower, "harbor", "harbor-shared-keys", key); !ok || got != want {
			t.Errorf("follower harbor/harbor-shared-keys %s = %q (present %v), want the primary's %q: an upload or a write that crosses regions is refused", key, got, ok, want)
		}
	}
}
