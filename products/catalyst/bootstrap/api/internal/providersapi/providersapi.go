// Package providersapi serves the read routes of the cloud profiles (design
// record 01, section 4.6) from the pack profiles of the image and the labelled
// installation ConfigMaps, and from nowhere else. No route writes. None returns
// a credential value: a profile only describes the credential fields, and a
// route answers the typed spec, never the document it read.
package providersapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
)

// Installations returns every installation document kept on the cluster.
type Installations func(ctx context.Context) ([][]byte, error)

// Routes serves the routes over the providers directory dir.
type Routes struct {
	dir  string
	list Installations
}

// New returns the routes over dir and the documents that list returns.
func New(dir string, list Installations) *Routes { return &Routes{dir: dir, list: list} }

// FromCluster lists the installation ConfigMaps through the client that client
// returns. A client that cannot be built, or a labelled ConfigMap without the
// key cloudprofile.InstallationKey, is an error, never an empty list.
func FromCluster(client func() (kubernetes.Interface, error)) Installations {
	return func(ctx context.Context) ([][]byte, error) {
		c, err := client()
		if err != nil {
			return nil, err
		}
		l, err := c.CoreV1().ConfigMaps(cloudprofile.InstallationNamespace).List(ctx, metav1.ListOptions{LabelSelector: cloudprofile.InstallationLabel + "=true"})
		if err != nil {
			return nil, err
		}
		docs := make([][]byte, 0, len(l.Items))
		for _, cm := range l.Items {
			data, ok := cm.Data[cloudprofile.InstallationKey]
			if !ok {
				return nil, fmt.Errorf("ConfigMap %s/%s has no key %s", cm.Namespace, cm.Name, cloudprofile.InstallationKey)
			}
			docs = append(docs, []byte(data))
		}
		return docs, nil
	}
}

// Entry is one entry of the provider list.
type Entry struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	Installation     string `json:"installation"`
	DisplayName      string `json:"displayName"`
	Lifecycle        string `json:"lifecycle"`
	CredentialSource string `json:"credentialSource"`
}

// invalid is the body of a 500 about the profile of provider.
func invalid(provider string, err error) map[string]string {
	return map[string]string{"error": "profile-invalid", "provider": provider, "detail": err.Error()}
}

// read returns the pack profiles by name and, when installations is true, the
// effective profile of every installation document; or the body of a 500.
func (rt *Routes) read(ctx context.Context, installations bool) (map[string]*cloudprofile.Profile, []*cloudprofile.Profile, map[string]string) {
	packs, err := cloudprofile.Load(rt.dir)
	if err != nil {
		// Load starts its error with the file <dir>/<pack>/profile.yaml.
		rest, _ := strings.CutPrefix(err.Error(), filepath.Clean(rt.dir)+string(filepath.Separator))
		name, _, _ := strings.Cut(rest, string(filepath.Separator))
		if rest == err.Error() || strings.HasPrefix(name, "_") || name == rest {
			name = ""
		}
		return nil, nil, invalid(name, err)
	}
	if !installations {
		return packs, nil, nil
	}
	docs, err := rt.list(ctx)
	if err != nil {
		return nil, nil, map[string]string{"error": "installations-unreadable", "detail": err.Error()}
	}
	var effective []*cloudprofile.Profile
	for _, data := range docs {
		doc, err := cloudprofile.Parse(data)
		if err != nil {
			return nil, nil, invalid("", err)
		}
		pack, ok := packs[doc.Spec.Extends]
		if !ok {
			return nil, nil, invalid(doc.Spec.Extends, &cloudprofile.FieldError{Err: cloudprofile.ErrUnknownPack, Path: "spec.extends", Detail: strconv.Quote(doc.Spec.Extends)})
		}
		eff, err := cloudprofile.Merge(pack, doc)
		if err != nil {
			return nil, nil, invalid(doc.Spec.Extends, err)
		}
		effective = append(effective, eff)
	}
	return packs, effective, nil
}

// List serves GET /api/v1/providers: an entry per pack profile without requiresInstallation
// and per installation document, by id; without ?include=all only those whose lifecycle is proven.
func (rt *Routes) List(w http.ResponseWriter, r *http.Request) {
	packs, installations, f := rt.read(r.Context(), true)
	if f != nil {
		write(w, http.StatusInternalServerError, f)
		return
	}
	all := r.URL.Query().Get("include") == "all"
	entries := []Entry{}
	add := func(id, provider, installation string, p *cloudprofile.Profile) {
		if all || p.Spec.Lifecycle == "proven" {
			entries = append(entries, Entry{ID: id, Provider: provider, Installation: installation,
				DisplayName: p.Spec.DisplayName, Lifecycle: p.Spec.Lifecycle, CredentialSource: p.Spec.Credentials.Source})
		}
	}
	for name, p := range packs {
		if !p.Spec.RequiresInstallation {
			add(name, name, "", p)
		}
	}
	for _, p := range installations {
		add(p.Spec.Extends+"/"+p.Metadata.Name, p.Spec.Extends, p.Metadata.Name, p)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	write(w, http.StatusOK, map[string][]Entry{"providers": entries})
}

// Profile serves GET /api/v1/providers/{provider}: the spec of the pack
// profile, merged with the installation document that ?installation= names.
func (rt *Routes) Profile(w http.ResponseWriter, r *http.Request) {
	name, installation := chi.URLParam(r, "provider"), r.URL.Query().Get("installation")
	packs, installations, f := rt.read(r.Context(), installation != "")
	switch pack, ok := packs[name]; {
	case f != nil:
		write(w, http.StatusInternalServerError, f)
	case !ok:
		write(w, http.StatusNotFound, map[string]string{"error": "unknown-provider"})
	case installation == "":
		write(w, http.StatusOK, pack.Spec)
	default:
		for _, p := range installations {
			if p.Spec.Extends == name && p.Metadata.Name == installation {
				write(w, http.StatusOK, p.Spec)
				return
			}
		}
		write(w, http.StatusNotFound, map[string]string{"error": "unknown-installation"})
	}
}

// Sovereign serves GET /api/v1/sovereign/cloud-profile: cloudprofile.ForSovereign.
func (rt *Routes) Sovereign(w http.ResponseWriter, r *http.Request) {
	p, err := cloudprofile.ForSovereign(r.Context(), rt.dir, func(ctx context.Context, name string) ([]byte, error) {
		docs, err := rt.list(ctx)
		for _, data := range docs {
			if doc, perr := cloudprofile.Parse(data); perr == nil && doc.Metadata.Name == name {
				return data, nil
			}
		}
		if err == nil {
			err = fmt.Errorf("no installation document named %q", name)
		}
		return nil, err
	})
	var fe *cloudprofile.FieldError
	switch {
	case errors.As(err, &fe) && fe.Path == cloudprofile.ProviderEnv:
		write(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud-profile-missing"})
	case err != nil:
		write(w, http.StatusInternalServerError, invalid(strings.TrimSpace(os.Getenv(cloudprofile.ProviderEnv)), err))
	default:
		write(w, http.StatusOK, p.Spec)
	}
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
