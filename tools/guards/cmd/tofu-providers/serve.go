package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// registry answers the read side of the OCI distribution API for image layouts:
// one repository name per layout folder. It is what tofu reads from an OCI
// provider mirror: the tag list, a manifest by tag or by digest, a blob.
type registry struct {
	repos map[string]string // repository name -> layout folder
}

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (r registry) fail(w http.ResponseWriter, code int, what string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": "NOT_FOUND", "message": what}}})
}

func (r registry) ServeHTTP(w http.ResponseWriter, q *http.Request) {
	if q.Method != http.MethodGet && q.Method != http.MethodHead {
		r.fail(w, http.StatusMethodNotAllowed, "read only")
		return
	}
	p := q.URL.Path
	if p == "/v2/" || p == "/v2" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
		return
	}
	rest := strings.TrimPrefix(p, "/v2/")
	if rest == p {
		r.fail(w, http.StatusNotFound, p)
		return
	}
	var name, kind, ref string
	if strings.HasSuffix(rest, "/tags/list") {
		name, kind = strings.TrimSuffix(rest, "/tags/list"), "tags"
	} else if i := strings.LastIndex(rest, "/manifests/"); i > 0 {
		name, kind, ref = rest[:i], "manifests", rest[i+len("/manifests/"):]
	} else if i := strings.LastIndex(rest, "/blobs/"); i > 0 {
		name, kind, ref = rest[:i], "blobs", rest[i+len("/blobs/"):]
	}
	dir, ok := r.repos[name]
	if !ok {
		r.fail(w, http.StatusNotFound, "repository "+name+" is not served")
		return
	}
	tags, err := readLayout(dir)
	if err != nil {
		r.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch kind {
	case "tags":
		var list []string
		for t := range tags {
			list = append(list, t)
		}
		sort.Strings(list)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "tags": list})
		return
	case "manifests":
		d := ref
		if t, ok := tags[ref]; ok {
			d = t.Digest
		}
		b, ok := r.blob(dir, d)
		if !ok {
			r.fail(w, http.StatusNotFound, "manifest "+ref)
			return
		}
		var m manifest
		if json.Unmarshal(b, &m) != nil || (m.MediaType != mtIndex && m.MediaType != mtManifest) {
			r.fail(w, http.StatusNotFound, "manifest "+ref)
			return
		}
		r.write(w, q, m.MediaType, d, b)
	case "blobs":
		b, ok := r.blob(dir, ref)
		if !ok {
			r.fail(w, http.StatusNotFound, "blob "+ref)
			return
		}
		r.write(w, q, "application/octet-stream", ref, b)
	default:
		r.fail(w, http.StatusNotFound, p)
	}
}

func (r registry) blob(dir, d string) ([]byte, bool) {
	if !digestRE.MatchString(d) {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(d, "sha256:"))))
	return b, err == nil && digestOf(b) == d
}

func (r registry) write(w http.ResponseWriter, q *http.Request, mediaType, d string, b []byte) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", d)
	w.Header().Set("Content-Length", fmt.Sprint(len(b)))
	if q.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(b)
}

// selfSigned makes a CA and a server certificate for 127.0.0.1 and localhost,
// valid for one day. It returns the server's TLS certificate and the CA in PEM.
func selfSigned() (tls.Certificate, []byte, error) {
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tofu-providers serve CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}

// startServe listens over TLS on 127.0.0.1 at a free port, writes the CA to caOut
// and returns the listener's address and the server. The caller serves and closes.
func startServe(repos map[string]string, caOut string) (net.Listener, *http.Server, error) {
	for name, dir := range repos {
		if _, err := readLayout(dir); err != nil {
			return nil, nil, fmt.Errorf("repository %s: %v", name, err)
		}
	}
	cert, caPEM, err := selfSigned()
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(caOut, caPEM, 0o600); err != nil {
		return nil, nil, err
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, nil, err
	}
	return ln, &http.Server{Handler: registry{repos: repos}, ReadHeaderTimeout: 10 * time.Second}, nil
}
