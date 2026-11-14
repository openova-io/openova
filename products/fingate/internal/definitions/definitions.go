// Package definitions reads the definitions of one installation of the open-banking product.
//
// The definitions are the Application's parameters: the profile, the public host of the
// resource server, the ledger target, the API families the installation serves and the
// partners with their applications. Parse reads them from one YAML or JSON document and
// checks every rule of the parameter schema; a document that breaks one is refused whole.
// A Holder keeps the definitions in use and answers the partner lookup of the token rules
// (rule T7) from them, so a change of a partner's state takes effect on the next call.
package definitions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openova-io/openova/products/fingate/internal/token"
)

// Released lists the API families an installation may serve. A family is added here only by
// the package that releases it.
var Released = []string{"accounts"}

// MaxPartners is the largest number of partners a document may hold.
const MaxPartners = 1000

// Definitions are the parameters of one installation.
type Definitions struct {
	Bindings map[string]any `yaml:"bindings"` // written by the install route; not read here
	Profile  string         `yaml:"profile"`
	Host     string         `yaml:"host"`
	Ledger   Ledger         `yaml:"ledger"`
	Products []string       `yaml:"products"` // nil: every released family
	Partners []Partner      `yaml:"partners"`
}

// Ledger is the address of the ledger adapter.
type Ledger struct {
	Target     string `yaml:"target"`
	ServerName string `yaml:"serverName"`
}

// Partner is one partner of the bank.
type Partner struct {
	ID           string        `yaml:"id"`
	LegalName    string        `yaml:"legalName"`
	Contacts     []string      `yaml:"contacts"`
	State        string        `yaml:"state"`
	Applications []Application `yaml:"applications"`
}

// Application is one application of a partner. Its client identifier is <partner id>.<application id>.
type Application struct {
	ID           string        `yaml:"id"`
	ClientAuth   string        `yaml:"clientAuth"`
	RedirectURIs []string      `yaml:"redirectUris"`
	JWKSURI      string        `yaml:"jwksUri"`
	Certificates []Certificate `yaml:"certificates"`
	Products     []string      `yaml:"products"`
	Webhook      string        `yaml:"webhook"`
}

// Certificate is one pinned certificate of an application.
type Certificate struct {
	SHA256   string `yaml:"sha256"`
	NotAfter string `yaml:"notAfter"`
}

var (
	profileRE  = regexp.MustCompile(`^[a-z0-9.-]{1,40}$`)
	hostRE     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	targetRE   = regexp.MustCompile(`^dns:///[a-z0-9.-]+:[0-9]{2,5}$`)
	idRE       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	pinRE      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	states     = []string{"approved", "suspended", "revoked"}
	clientAuth = []string{"tls_client_auth", "private_key_jwt"}
)

// Parse reads one definitions document and checks it. It returns every broken rule.
func Parse(raw []byte) (*Definitions, error) {
	var d Definitions
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("definitions: %w", err)
	}
	if err := d.check(); err != nil {
		return nil, fmt.Errorf("definitions: %w", err)
	}
	return &d, nil
}

func (d *Definitions) check() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !profileRE.MatchString(d.Profile) {
		bad("profile %q does not match %s", d.Profile, profileRE)
	}
	if len(d.Host) > 253 || !hostRE.MatchString(d.Host) {
		bad("host %q is not a host name", d.Host)
	}
	if !targetRE.MatchString(d.Ledger.Target) {
		bad("ledger.target %q does not match %s", d.Ledger.Target, targetRE)
	}
	if d.Products != nil {
		products("products", d.Products, bad)
	}
	if len(d.Partners) > MaxPartners {
		bad("partners holds %d entries, at most %d are allowed", len(d.Partners), MaxPartners)
	}
	seen := map[string]bool{}
	for i, p := range d.Partners {
		at := fmt.Sprintf("partners[%d]", i)
		if !idRE.MatchString(p.ID) || seen[p.ID] {
			bad("%s: id %q does not match %s or is not unique", at, p.ID, idRE)
		}
		seen[p.ID] = true
		if n := len([]rune(p.LegalName)); n < 1 || n > 200 {
			bad("%s: legalName must have 1 to 200 characters", at)
		}
		for _, c := range p.Contacts {
			if a, err := mail.ParseAddress(c); err != nil || a.Address != c {
				bad("%s: contact %q is not an e-mail address", at, c)
			}
		}
		if !slices.Contains(states, p.State) {
			bad("%s: state %q is not one of %s", at, p.State, strings.Join(states, ", "))
		}
		if p.Applications == nil {
			bad("%s: applications is missing", at)
		}
		apps := map[string]bool{}
		for j, a := range p.Applications {
			where := fmt.Sprintf("%s.applications[%d]", at, j)
			if !idRE.MatchString(a.ID) || apps[a.ID] {
				bad("%s: id %q does not match %s or is not unique", where, a.ID, idRE)
			}
			apps[a.ID] = true
			if !slices.Contains(clientAuth, a.ClientAuth) {
				bad("%s: clientAuth %q is not one of %s", where, a.ClientAuth, strings.Join(clientAuth, ", "))
			}
			if len(a.RedirectURIs) == 0 {
				bad("%s: redirectUris is empty", where)
			}
			for _, u := range a.RedirectURIs {
				if !strings.HasPrefix(u, "https://") {
					bad("%s: redirect address %q does not start with https://", where, u)
				}
			}
			if a.JWKSURI != "" && !strings.HasPrefix(a.JWKSURI, "https://") {
				bad("%s: jwksUri %q does not start with https://", where, a.JWKSURI)
			}
			if a.Webhook != "" && !strings.HasPrefix(a.Webhook, "https://") {
				bad("%s: webhook %q does not start with https://", where, a.Webhook)
			}
			for k, c := range a.Certificates {
				if !pinRE.MatchString(c.SHA256) {
					bad("%s.certificates[%d]: sha256 %q is not a base64url SHA-256", where, k, c.SHA256)
				}
				if _, err := time.Parse(time.DateOnly, c.NotAfter); err != nil {
					bad("%s.certificates[%d]: notAfter %q is not a date", where, k, c.NotAfter)
				}
			}
			if a.Products == nil {
				bad("%s: products is missing", where)
			}
			products(where+".products", a.Products, bad)
		}
	}
	return errors.Join(errs...)
}

func products(where string, list []string, bad func(string, ...any)) {
	for _, p := range list {
		if !slices.Contains(Released, p) {
			bad("%s: %q is not a released family (%s)", where, p, strings.Join(Released, ", "))
		}
	}
}

// Serves reports whether the installation serves the API family. Without a products list
// it serves every released family.
func (d *Definitions) Serves(family string) bool {
	if d.Products == nil {
		return slices.Contains(Released, family)
	}
	return slices.Contains(d.Products, family)
}

// Application returns the application whose client identifier is clientID, in the form the
// token rules read it.
func (d *Definitions) Application(clientID string) (token.Application, bool) {
	partnerID, appID, ok := strings.Cut(clientID, ".")
	if !ok {
		return token.Application{}, false
	}
	for _, p := range d.Partners {
		if p.ID != partnerID {
			continue
		}
		for _, a := range p.Applications {
			if a.ID != appID {
				continue
			}
			pins := make([]string, 0, len(a.Certificates))
			for _, c := range a.Certificates {
				pins = append(pins, c.SHA256)
			}
			return token.Application{PartnerState: p.State, Pins: pins, Products: slices.Clone(a.Products)}, true
		}
	}
	return token.Application{}, false
}

// Holder keeps the definitions in use. It is safe for concurrent use.
type Holder struct {
	current atomic.Pointer[Definitions]
}

// Set replaces the definitions in use.
func (h *Holder) Set(d *Definitions) { h.current.Store(d) }

// Current returns the definitions in use, or nil before the first Set.
func (h *Holder) Current() *Definitions { return h.current.Load() }

// ErrNoDefinitions is returned by a lookup before the first Set.
var ErrNoDefinitions = errors.New("definitions: none loaded yet")

// Application implements token.Partners.
func (h *Holder) Application(_ context.Context, clientID string) (token.Application, bool, error) {
	d := h.Current()
	if d == nil {
		return token.Application{}, false, ErrNoDefinitions
	}
	app, ok := d.Application(clientID)
	return app, ok, nil
}
