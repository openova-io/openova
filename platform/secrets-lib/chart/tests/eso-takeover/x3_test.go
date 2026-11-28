// SEC-228, fact X3 of the spike SEC-227: what External Secrets does with an
// ExternalSecret of creationPolicy Owner whose target is a Secret that Helm
// created and kept (helm.sh/resource-policy: keep), and with one whose store
// path is absent. run.sh copies this file into pkg/controllers/externalsecret
// of external-secrets at the version platform/external-secrets/chart pins and
// runs it in that package's envtest suite (a kube-apiserver and etcd, no
// garbage collector), against the reconciler as that suite starts it (Secret
// cache off, as the controller's default). The store is the suite's fake
// provider; the ExternalSecret has the fields the library renders.
package externalsecret

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	esv1beta1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1beta1"
	ctest "github.com/external-secrets/external-secrets/pkg/controllers/commontest"
)

var _ = Describe("SEC-228 X3", Label("sec228"), func() {
	const (
		name   = "kept"
		oldVal = "the-value-helm-wrote"
		newVal = "the-value-the-store-holds"
	)
	ctx := context.Background()
	var ns string

	BeforeEach(func() {
		var err error
		ns, err = ctest.CreateNamespace("x3", k8sClient)
		Expect(err).ToNot(HaveOccurred())
		fakeProvider.Reset()
		Expect(k8sClient.Create(ctx, &esv1beta1.SecretStore{
			ObjectMeta: metav1.ObjectMeta{Name: "openbao", Namespace: ns},
			Spec: esv1beta1.SecretStoreSpec{Provider: &esv1beta1.SecretStoreProvider{
				AWS: &esv1beta1.AWSProvider{Service: esv1beta1.AWSServiceSecretsManager},
			}},
		})).To(Succeed())
	})
	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
	})

	// keptSecret creates the Secret as Helm leaves it after an upgrade that no
	// longer renders it: Helm's field manager, label and annotations, keep.
	keptSecret := func(typ v1.SecretType, data map[string][]byte) *v1.Secret {
		s := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"},
				Annotations: map[string]string{
					"meta.helm.sh/release-name":      "fx",
					"meta.helm.sh/release-namespace": ns,
					"helm.sh/resource-policy":        "keep",
				},
			},
			Type: typ,
			Data: data,
		}
		Expect(k8sClient.Create(ctx, s, client.FieldOwner("helm"))).To(Succeed())
		return s
	}
	// externalSecret creates the library's ExternalSecret <name>: target <name>,
	// creationPolicy Owner, key password from openova/apps/<ns>/<name>/e1.
	externalSecret := func(tpl *esv1beta1.ExternalSecretTemplate) {
		Expect(k8sClient.Create(ctx, &esv1beta1.ExternalSecret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: esv1beta1.ExternalSecretSpec{
				RefreshInterval: &metav1.Duration{Duration: time.Hour},
				SecretStoreRef:  esv1beta1.SecretStoreRef{Kind: "SecretStore", Name: "openbao"},
				Target:          esv1beta1.ExternalSecretTarget{Name: name, CreationPolicy: esv1beta1.CreatePolicyOwner, Template: tpl},
				Data: []esv1beta1.ExternalSecretData{{
					SecretKey: "password",
					RemoteRef: esv1beta1.ExternalSecretDataRemoteRef{Key: "openova/apps/" + ns + "/" + name + "/e1", Property: "password"},
				}},
			},
		})).To(Succeed())
	}
	// ready is the Ready condition of the ExternalSecret, "<status>/<reason>".
	ready := func() string {
		var es esv1beta1.ExternalSecret
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &es); err != nil {
			return "get: " + err.Error()
		}
		c := GetExternalSecretCondition(es.Status, esv1beta1.ExternalSecretReady)
		if c == nil {
			return "no condition"
		}
		return string(c.Status) + "/" + c.Reason
	}
	esUID := func() types.UID {
		var es esv1beta1.ExternalSecret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &es)).To(Succeed())
		return es.UID
	}
	secret := func() *v1.Secret {
		var s v1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &s)).To(Succeed())
		return &s
	}
	// untouched: the kept Secret is still Helm's, with Helm's value.
	untouched := func(before *v1.Secret) {
		s := secret()
		Expect(s.UID).To(Equal(before.UID))
		Expect(metav1.GetControllerOf(s)).To(BeNil())
		Expect(s.Data).To(Equal(before.Data))
		Expect(s.Type).To(Equal(before.Type))
	}

	It("T1 takes the kept Secret over: the same object, owned by the ExternalSecret, the store's keys only; Helm's label and annotations stay", func() {
		before := keptSecret(v1.SecretTypeOpaque, map[string][]byte{"password": []byte(oldVal), "stale": []byte("x")})
		fakeProvider.WithGetSecret([]byte(newVal), nil)
		externalSecret(nil)
		Eventually(ready, timeout, interval).Should(Equal("True/SecretSynced"))
		s := secret()
		Expect(s.UID).To(Equal(before.UID))
		owner := metav1.GetControllerOf(s)
		Expect(owner).ToNot(BeNil())
		Expect([]string{owner.Kind, owner.Name, string(owner.UID)}).To(Equal([]string{"ExternalSecret", name, string(esUID())}))
		Expect(s.Data).To(Equal(map[string][]byte{"password": []byte(newVal)}))
		Expect(s.Type).To(Equal(v1.SecretTypeOpaque))
		Expect(s.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "Helm"))
		Expect(s.Labels).To(HaveKey(esv1beta1.LabelOwner))
		Expect(s.Annotations).To(HaveKeyWithValue("helm.sh/resource-policy", "keep"))
		Expect(s.Annotations).To(HaveKeyWithValue("meta.helm.sh/release-name", "fx"))
	})

	It("T2 with template data: the template's keys are set, a key only Helm wrote stays", func() {
		keptSecret(v1.SecretTypeOpaque, map[string][]byte{"password": []byte(oldVal), "stale": []byte("x")})
		fakeProvider.WithGetSecret([]byte(newVal), nil)
		externalSecret(&esv1beta1.ExternalSecretTemplate{
			EngineVersion: esv1beta1.TemplateEngineV2, Type: v1.SecretTypeOpaque,
			Data: map[string]string{"password": "{{ .password }}", "user": "app"},
		})
		Eventually(ready, timeout, interval).Should(Equal("True/SecretSynced"))
		Expect(secret().Data).To(Equal(map[string][]byte{"password": []byte(newVal), "user": []byte("app"), "stale": []byte("x")}))
	})

	It("T3 a template without the kept Secret's type is refused (a Secret's type is immutable): not ready, the Secret stays Helm's", func() {
		before := keptSecret(v1.SecretTypeBasicAuth, map[string][]byte{"username": []byte("app"), "password": []byte(oldVal)})
		fakeProvider.WithGetSecret([]byte(newVal), nil)
		externalSecret(&esv1beta1.ExternalSecretTemplate{
			EngineVersion: esv1beta1.TemplateEngineV2,
			Data:          map[string]string{"username": "app", "password": "{{ .password }}"},
		})
		Eventually(ready, timeout, interval).Should(Equal("False/SecretSyncedError"))
		Consistently(ready, 3*time.Second, interval).Should(Equal("False/SecretSyncedError"))
		untouched(before)
	})

	It("T4 a template with the kept Secret's type takes it over", func() {
		before := keptSecret(v1.SecretTypeBasicAuth, map[string][]byte{"username": []byte("app"), "password": []byte(oldVal)})
		fakeProvider.WithGetSecret([]byte(newVal), nil)
		externalSecret(&esv1beta1.ExternalSecretTemplate{
			EngineVersion: esv1beta1.TemplateEngineV2, Type: v1.SecretTypeBasicAuth,
			Data: map[string]string{"username": "app", "password": "{{ .password }}"},
		})
		Eventually(ready, timeout, interval).Should(Equal("True/SecretSynced"))
		s := secret()
		Expect(s.UID).To(Equal(before.UID))
		Expect(metav1.GetControllerOf(s)).ToNot(BeNil())
		Expect(s.Data).To(Equal(map[string][]byte{"username": []byte("app"), "password": []byte(newVal)}))
	})

	It("T5 an absent store path: not ready, the kept Secret keeps Helm's value and owner", func() {
		before := keptSecret(v1.SecretTypeOpaque, map[string][]byte{"password": []byte(oldVal)})
		fakeProvider.WithGetSecret(nil, esv1beta1.NoSecretError{})
		externalSecret(nil)
		Eventually(ready, timeout, interval).Should(Equal("False/SecretSyncedError"))
		Consistently(ready, 3*time.Second, interval).Should(Equal("False/SecretSyncedError"))
		untouched(before)
	})

	It("T6 an absent store path and no Secret: not ready, no Secret is created", func() {
		fakeProvider.WithGetSecret(nil, esv1beta1.NoSecretError{})
		externalSecret(nil)
		Eventually(ready, timeout, interval).Should(Equal("False/SecretSyncedError"))
		Consistently(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &v1.Secret{})
			return apierrors.IsNotFound(err)
		}, 3*time.Second, interval).Should(BeTrue())
	})
})
