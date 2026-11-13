package cloudprofile

import (
	"fmt"
	"strconv"

	"sigs.k8s.io/yaml"
)

// Where an installation document is kept on a cluster: one ConfigMap per
// document, in InstallationNamespace, named InstallationConfigMapPrefix
// followed by the document's metadata.name, labelled InstallationLabel with
// the value "true", and holding the document, unchanged, under the key
// InstallationKey. A reader lists the ConfigMaps of the namespace by the label
// and reads the key. The document itself is never stored in this repository.
const (
	InstallationNamespace       = "catalyst-system"
	InstallationLabel           = "catalyst.openova.io/cloud-installation"
	InstallationKey             = "profile.yaml"
	InstallationConfigMapPrefix = "cloud-installation-"
)

// maxConfigMapName is the longest name a ConfigMap may have.
const maxConfigMapName = 253

// CheckInstallation reads the pack profiles below dir with Load, parses the
// installation document data, and merges it over the pack profile that its
// spec.extends names. It returns the effective profile, or the first error
// (a *FieldError for every fault of the document).
func CheckInstallation(dir string, data []byte) (*Profile, error) {
	inst, err := Parse(data)
	if err != nil {
		return nil, err
	}
	packs, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if inst.Spec.Extends == "" {
		return nil, &FieldError{Err: ErrMissingField, Path: "spec.extends"}
	}
	pack, ok := packs[inst.Spec.Extends]
	if !ok {
		return nil, &FieldError{Err: ErrUnknownPack, Path: "spec.extends", Detail: strconv.Quote(inst.Spec.Extends)}
	}
	return Merge(pack, inst)
}

// InstallationConfigMap checks the installation document data with
// CheckInstallation and returns, as YAML, the ConfigMap that carries it on a
// cluster. It returns no ConfigMap when the document fails the check.
func InstallationConfigMap(dir string, data []byte) ([]byte, error) {
	eff, err := CheckInstallation(dir, data)
	if err != nil {
		return nil, err
	}
	name := InstallationConfigMapPrefix + eff.Metadata.Name
	if len(name) > maxConfigMapName {
		return nil, &FieldError{Err: ErrBadValue, Path: "metadata.name", Detail: fmt.Sprintf("the ConfigMap name %q is longer than %d characters", name, maxConfigMapName)}
	}
	cm := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      name,
			"namespace": InstallationNamespace,
			"labels":    map[string]any{InstallationLabel: "true"},
		},
		"data": map[string]any{InstallationKey: string(data)},
	}
	return yaml.Marshal(cm)
}
