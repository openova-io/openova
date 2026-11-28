// Package config reads the configuration of the session agent: the value of
// the session pod's annotation access.openova.io/config, which the downward API
// writes to /etc/access/config.json (ADR-0033). It holds no secret; the
// credentials are files under /var/run/secrets/openova that ParamFiles names.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	// Path is where the pod mounts the configuration.
	Path = "/etc/access/config.json"
	// Schema is the only schema this agent reads.
	Schema = "openova.io/access-session-config/v1"
	// MaxChunkBytes is the largest chunk the controller's ingest route takes.
	MaxChunkBytes = 4 << 20
)

// Kinds are the kinds whose credential identifier the agent knows (ADR-0033).
var Kinds = []string{"pod", "ssh", "database"}

// Protocols are the protocols of guacd 1.6.0.
var Protocols = []string{"kubernetes", "ssh", "telnet", "vnc", "rdp"}

// AgentParams are the guacd parameters the agent itself sets for every
// connection (ADR-0033). The configuration may not set them, nor any other
// parameter of their families (Reserved).
var AgentParams = []string{"recording-path", "recording-name", "create-recording-path", "recording-include-keys",
	"typescript-path", "typescript-name", "create-typescript-path", "disable-copy", "disable-paste"}

// Reserved says whether the configuration may not set the guacd parameter k:
// one of AgentParams, or any parameter named recording-*, typescript-* or
// create-*-path. Those families decide what is recorded and where files are
// written, so only the agent sets them (none while recording is off).
func Reserved(k string) bool {
	return slices.Contains(AgentParams, k) || strings.HasPrefix(k, "recording-") || strings.HasPrefix(k, "typescript-") ||
		(strings.HasPrefix(k, "create-") && strings.HasSuffix(k, "-path"))
}

// Controls are the session's controls.
type Controls struct {
	Keystrokes     bool `json:"keystrokes"`
	ClipboardCopy  bool `json:"clipboardCopy"`
	ClipboardPaste bool `json:"clipboardPaste"`
}

// Config is the agent's configuration. Every field is required.
type Config struct {
	Schema       string            `json:"schema"`
	SessionUID   string            `json:"sessionUID"`
	Kind         string            `json:"kind"`
	Subject      string            `json:"subject"`
	Protocol     string            `json:"protocol"`
	Params       map[string]string `json:"params"`
	ParamFiles   map[string]string `json:"paramFiles"`
	Controls     Controls          `json:"controls"`
	Terminal     bool              `json:"terminal"`
	Ingest       string            `json:"ingest"`
	ChunkBytes   int               `json:"chunkBytes"`
	ChunkSeconds int               `json:"chunkSeconds"`
}

// Recording says whether the session is recorded: chunkBytes 0 switches the
// recording off, and then the agent sends no recording parameter and ships
// nothing.
func (c *Config) Recording() bool { return c.ChunkBytes > 0 }

var (
	uidRE     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	subjectRE = regexp.MustCompile(`^[^\s@]{1,64}@[^\s@]{1,190}$`)
	nameRE    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,62}[a-z0-9])?$`)
)

// Load reads and checks the file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads one JSON object that names exactly the fields of Config, each
// with a value that is not null, and checks the values.
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := strict(b, &c, "schema", "sessionUID", "kind", "subject", "protocol", "params", "paramFiles",
		"controls", "terminal", "ingest", "chunkBytes", "chunkSeconds"); err != nil {
		return nil, err
	}
	if err := c.check(); err != nil {
		return nil, err
	}
	return &c, nil
}

// strict decodes b into out after it checked that b is one JSON object whose
// keys are exactly keys (case counts) with no null value; the object controls
// is checked the same way.
func strict(b []byte, out any, keys ...string) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return fmt.Errorf("the configuration is not a JSON object: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("the configuration has text after its object")
	}
	if err := exact("", top, keys); err != nil {
		return err
	}
	var controls map[string]json.RawMessage
	if err := json.Unmarshal(top["controls"], &controls); err != nil {
		return fmt.Errorf("controls is not an object: %w", err)
	}
	if err := exact("controls.", controls, []string{"keystrokes", "clipboardCopy", "clipboardPaste"}); err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("the configuration does not decode: %w", err)
	}
	return nil
}

func exact(where string, m map[string]json.RawMessage, keys []string) error {
	var errs []error
	for k, v := range m {
		switch {
		case !slices.Contains(keys, k):
			errs = append(errs, fmt.Errorf("unknown field %s%s", where, k))
		case string(bytes.TrimSpace(v)) == "null":
			errs = append(errs, fmt.Errorf("field %s%s is null", where, k))
		}
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			errs = append(errs, fmt.Errorf("missing field %s%s", where, k))
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errors.Join(errs...)
}

func (c *Config) check() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if c.Schema != Schema {
		bad("schema is %q, want %q", c.Schema, Schema)
	}
	if !uidRE.MatchString(c.SessionUID) {
		bad("sessionUID %q is not a lower-case UUID", c.SessionUID)
	}
	if !slices.Contains(Kinds, c.Kind) {
		bad("kind %q is not one of %v", c.Kind, Kinds)
	}
	if !subjectRE.MatchString(c.Subject) {
		bad("subject %q is not an e-mail address", c.Subject)
	}
	if !slices.Contains(Protocols, c.Protocol) {
		bad("protocol %q is not one of %v", c.Protocol, Protocols)
	}
	for _, m := range []struct {
		what string
		m    map[string]string
	}{{"params", c.Params}, {"paramFiles", c.ParamFiles}} {
		for k := range m.m {
			if !nameRE.MatchString(k) || Reserved(k) {
				bad("%s names %q, which is not a parameter the configuration may set", m.what, k)
			}
		}
	}
	for k, f := range c.ParamFiles {
		if _, ok := c.Params[k]; ok {
			bad("parameter %q is in params and in paramFiles", k)
		}
		if !nameRE.MatchString(f) {
			bad("paramFiles names the file %q, which is not a plain file name", f)
		}
	}
	if u, err := url.Parse(c.Ingest); err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		bad("ingest %q is not the http base address of a host", c.Ingest)
	}
	if c.ChunkBytes < 0 || c.ChunkBytes > MaxChunkBytes {
		bad("chunkBytes %d is not between 0 and %d", c.ChunkBytes, MaxChunkBytes)
	}
	if c.ChunkSeconds < 0 || c.ChunkSeconds > 3600 || (c.ChunkBytes > 0 && c.ChunkSeconds == 0) {
		bad("chunkSeconds %d is not between 0 and 3600, or is 0 while chunkBytes is not", c.ChunkSeconds)
	}
	return errors.Join(errs...)
}
