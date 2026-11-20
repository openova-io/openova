package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The media and artifact types of an OpenTofu provider in an OCI registry
// (OpenTofu 1.10, website/docs/cli/oci_registries/provider-mirror.mdx).
const (
	mtIndex          = "application/vnd.oci.image.index.v1+json"
	mtManifest       = "application/vnd.oci.image.manifest.v1+json"
	mtEmpty          = "application/vnd.oci.empty.v1+json"
	mtZip            = "archive/zip"
	atProvider       = "application/vnd.opentofu.provider"
	atProviderTarget = "application/vnd.opentofu.provider-target"
	refNameKey       = "org.opencontainers.image.ref.name"
)

type descriptor struct {
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Platform     *platform         `json:"platform,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

type platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	ArtifactType  string       `json:"artifactType,omitempty"`
	Config        *descriptor  `json:"config,omitempty"`
	Layers        []descriptor `json:"layers,omitempty"`
	Manifests     []descriptor `json:"manifests,omitempty"`
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// layout collects the blobs of an OCI image layout before it is written.
type layout map[string][]byte

func (l layout) add(b []byte) string {
	d := digestOf(b)
	l[d] = b
	return d
}

var zipNameRE = regexp.MustCompile(`^terraform-provider-([a-z0-9-]+)_([0-9]+\.[0-9]+\.[0-9]+)_([a-z0-9]+)_([a-z0-9]+)\.zip$`)

// packLayout writes, into the empty or absent folder out, an OCI image layout
// that holds one OpenTofu provider version: an index of artifact type
// application/vnd.opentofu.provider with one image manifest per zip (artifact type
// application/vnd.opentofu.provider-target, its platform from the zip's name, an
// empty config and exactly one layer archive/zip, the zip as it is). The layout's
// own index tags the provider index with the version. No manifest carries an
// annotation or a time stamp, so the index digest follows from the zips alone and
// is the same on every run. It returns that digest.
func packLayout(out, ver string, zips []string) (string, error) {
	if _, _, err := parseVersion(ver); err != nil {
		return "", err
	}
	if len(zips) == 0 {
		return "", fmt.Errorf("pack needs at least one zip")
	}
	blobs := layout{}
	emptyDigest := blobs.add([]byte("{}"))
	var descs []descriptor
	seen := map[string]bool{}
	for _, z := range zips {
		m := zipNameRE.FindStringSubmatch(filepath.Base(z))
		if m == nil {
			return "", fmt.Errorf("%s: the name is not terraform-provider-<type>_<version>_<os>_<arch>.zip", z)
		}
		if m[2] != ver {
			return "", fmt.Errorf("%s: the name gives version %s, not %s", z, m[2], ver)
		}
		key := m[3] + "/" + m[4]
		if seen[key] {
			return "", fmt.Errorf("two zips for the platform %s", key)
		}
		seen[key] = true
		b, err := os.ReadFile(filepath.Clean(z))
		if err != nil {
			return "", err
		}
		layer := descriptor{MediaType: mtZip, Digest: blobs.add(b), Size: int64(len(b))}
		mb, err := json.Marshal(manifest{
			SchemaVersion: 2, MediaType: mtManifest, ArtifactType: atProviderTarget,
			Config: &descriptor{MediaType: mtEmpty, Digest: emptyDigest, Size: 2},
			Layers: []descriptor{layer},
		})
		if err != nil {
			return "", err
		}
		descs = append(descs, descriptor{MediaType: mtManifest, ArtifactType: atProviderTarget,
			Digest: blobs.add(mb), Size: int64(len(mb)), Platform: &platform{Architecture: m[4], OS: m[3]}})
	}
	sort.Slice(descs, func(i, j int) bool {
		return descs[i].Platform.OS+"/"+descs[i].Platform.Architecture < descs[j].Platform.OS+"/"+descs[j].Platform.Architecture
	})
	ib, err := json.Marshal(manifest{SchemaVersion: 2, MediaType: mtIndex, ArtifactType: atProvider, Manifests: descs})
	if err != nil {
		return "", err
	}
	index := blobs.add(ib)
	top, err := json.Marshal(manifest{SchemaVersion: 2, MediaType: mtIndex, Manifests: []descriptor{{
		MediaType: mtIndex, ArtifactType: atProvider, Digest: index, Size: int64(len(ib)),
		Annotations: map[string]string{refNameKey: ver},
	}}})
	if err != nil {
		return "", err
	}
	if ents, err := os.ReadDir(out); err == nil && len(ents) > 0 {
		return "", fmt.Errorf("%s is not empty", out)
	}
	if err := os.MkdirAll(filepath.Join(out, "blobs", "sha256"), 0o750); err != nil {
		return "", err
	}
	for d, b := range blobs {
		if err := os.WriteFile(filepath.Join(out, "blobs", "sha256", strings.TrimPrefix(d, "sha256:")), b, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(out, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o600); err != nil {
		return "", err
	}
	return index, os.WriteFile(filepath.Join(out, "index.json"), top, 0o600)
}

// readLayout reads an image layout: its tags (the ref names of index.json) and
// the path of each blob by digest.
func readLayout(dir string) (tags map[string]descriptor, err error) {
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "index.json")))
	if err != nil {
		return nil, err
	}
	var top manifest
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, fmt.Errorf("%s/index.json: %v", dir, err)
	}
	tags = map[string]descriptor{}
	for _, d := range top.Manifests {
		if t := d.Annotations[refNameKey]; t != "" {
			tags[t] = d
		}
	}
	return tags, nil
}

var stubSourceRE = regexp.MustCompile(`^registry\.opentofu\.org/([a-z0-9-]+)/([a-z0-9-]+)$`)

// writeStub writes into dir a provider package for offline tests:
// terraform-provider-<type>_<version>_linux_amd64.zip holding one executable,
// terraform-provider-<type>_v<version>, a shell script that refuses to run. Its
// bytes are the same on every run. It returns the zip's path.
func writeStub(dir, source, ver string) (string, error) {
	m := stubSourceRE.FindStringSubmatch(source)
	if m == nil {
		return "", fmt.Errorf("source %q is not registry.opentofu.org/<namespace>/<type>", source)
	}
	if _, _, err := parseVersion(ver); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "terraform-provider-" + m[2] + "_v" + ver, Method: zip.Deflate,
		Modified: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	h.SetMode(0o755)
	f, err := w.CreateHeader(h)
	if err != nil {
		return "", err
	}
	if _, err := f.Write([]byte("#!/bin/sh\necho 'a stub of " + source + " " + ver + " for offline tests; it does not run' >&2\nexit 1\n")); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "terraform-provider-"+m[2]+"_"+ver+"_linux_amd64.zip")
	return p, os.WriteFile(p, buf.Bytes(), 0o600)
}
