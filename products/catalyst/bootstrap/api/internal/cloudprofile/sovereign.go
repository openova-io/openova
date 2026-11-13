package cloudprofile

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// The Sovereign an API process runs on. The platform chart sets both
// variables on the catalyst-api Deployment from global.cloud.provider and
// global.cloud.installation.
const (
	// ProviderEnv names the provider pack, a directory below the providers
	// directory.
	ProviderEnv = "CLOUD_PROVIDER"
	// InstallationEnv names the installation document laid over the pack
	// (its metadata.name). Empty: the pack profile alone.
	InstallationEnv = "CLOUD_INSTALLATION"
	// DefaultDir is the providers directory in the catalyst-api image.
	DefaultDir = "/infra/providers"
)

// InstallationReader returns the installation document named name.
type InstallationReader func(ctx context.Context, name string) ([]byte, error)

// ForSovereign returns the effective cloud profile of the Sovereign this
// process runs on: the pack profile below dir that CLOUD_PROVIDER names,
// merged (Merge) with the installation document that CLOUD_INSTALLATION
// names when that is set; read fetches the document. An empty or unknown
// pack gives ErrUnknownPack at CLOUD_PROVIDER; a document whose
// metadata.name is not CLOUD_INSTALLATION gives ErrBadValue at
// metadata.name.
func ForSovereign(ctx context.Context, dir string, read InstallationReader) (*Profile, error) {
	name := strings.TrimSpace(os.Getenv(ProviderEnv))
	if name == "" {
		return nil, &FieldError{Err: ErrUnknownPack, Path: ProviderEnv, Detail: "not set"}
	}
	packs, err := Load(dir)
	if err != nil {
		return nil, err
	}
	pack, ok := packs[name]
	if !ok {
		return nil, &FieldError{Err: ErrUnknownPack, Path: ProviderEnv, Detail: strconv.Quote(name)}
	}
	inst := strings.TrimSpace(os.Getenv(InstallationEnv))
	if inst == "" {
		return pack, nil
	}
	if read == nil {
		return nil, fmt.Errorf("cloudprofile: %s is %q and there is no installation reader", InstallationEnv, inst)
	}
	data, err := read(ctx, inst)
	if err != nil {
		return nil, fmt.Errorf("cloudprofile: installation %q: %w", inst, err)
	}
	doc, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if doc.Metadata.Name != inst {
		return nil, &FieldError{Err: ErrBadValue, Path: "metadata.name", Detail: fmt.Sprintf("%q, %s is %q", doc.Metadata.Name, InstallationEnv, inst)}
	}
	return Merge(pack, doc)
}

// PackName returns the pack of a profile: spec.extends of an effective
// profile, else metadata.name.
func PackName(p *Profile) string {
	switch {
	case p == nil:
		return ""
	case p.Spec.Extends != "":
		return p.Spec.Extends
	}
	return p.Metadata.Name
}

// InstallationFromCluster reads an installation document from its ConfigMap
// (see InstallationNamespace) through client.
func InstallationFromCluster(client dynamic.Interface) InstallationReader {
	return func(ctx context.Context, name string) ([]byte, error) {
		cmName := InstallationConfigMapPrefix + name
		gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
		cm, err := client.Resource(gvr).Namespace(InstallationNamespace).Get(ctx, cmName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		data, found, err := unstructured.NestedString(cm.Object, "data", InstallationKey)
		if err != nil || !found {
			return nil, fmt.Errorf("ConfigMap %s/%s: no key %s (%v)", InstallationNamespace, cmName, InstallationKey, err)
		}
		return []byte(data), nil
	}
}
