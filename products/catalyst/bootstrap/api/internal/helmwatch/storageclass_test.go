// storageclass_test.go — #3971 default-StorageClass durability probe.
// Proves DefaultStorageClassInfoFromList correctly classifies the cluster
// default: ephemeral local-path (the P0 failure), durable cloud CSI (pass),
// no default, and the multiple-defaults error state.

package helmwatch

import (
	"context"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
)

func sc(name, provisioner string, isDefault bool) *storagev1.StorageClass {
	ann := map[string]string{}
	if isDefault {
		ann[defaultStorageClassAnnotation] = "true"
	}
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name, Annotations: ann},
		Provisioner: provisioner,
	}
}

func TestDefaultStorageClassInfoFromList(t *testing.T) {
	tests := []struct {
		name         string
		objs         []*storagev1.StorageClass
		wantFound    bool
		wantName     string
		wantEphem    bool
		wantMultiple bool
	}{
		{
			name:      "ephemeral local-path default — the #3971 failure",
			objs:      []*storagev1.StorageClass{sc("local-path", LocalPathProvisioner, true)},
			wantFound: true,
			wantName:  "local-path",
			wantEphem: true,
		},
		{
			name: "durable hcloud-volumes default — pass",
			objs: []*storagev1.StorageClass{
				sc("local-path", LocalPathProvisioner, false),
				sc("hcloud-volumes", "csi.hetzner.cloud", true),
			},
			wantFound: true,
			wantName:  "hcloud-volumes",
			wantEphem: false,
		},
		{
			name:      "no default at all",
			objs:      []*storagev1.StorageClass{sc("local-path", LocalPathProvisioner, false)},
			wantFound: false,
		},
		{
			name: "multiple defaults flagged",
			objs: []*storagev1.StorageClass{
				sc("local-path", LocalPathProvisioner, true),
				sc("hcloud-volumes", "csi.hetzner.cloud", true),
			},
			wantFound:    true,
			wantMultiple: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := make([]runtime.Object, 0, len(tc.objs))
			for _, o := range tc.objs {
				objs = append(objs, o)
			}
			cs := kfake.NewSimpleClientset(objs...)
			info, err := DefaultStorageClassInfoFromList(context.Background(), cs.StorageV1().StorageClasses())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Found != tc.wantFound {
				t.Errorf("Found = %v, want %v", info.Found, tc.wantFound)
			}
			if tc.wantName != "" && info.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", info.Name, tc.wantName)
			}
			if info.Ephemeral != tc.wantEphem {
				t.Errorf("Ephemeral = %v, want %v", info.Ephemeral, tc.wantEphem)
			}
			if info.MultipleDefaults != tc.wantMultiple {
				t.Errorf("MultipleDefaults = %v, want %v", info.MultipleDefaults, tc.wantMultiple)
			}
		})
	}
}
