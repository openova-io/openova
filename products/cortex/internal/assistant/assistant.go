// Package assistant answers with the assistants of the definitions: one retrieval over the
// assistant's sources, trimmed to the caller's own principals, then one generation through the
// chat alias of the model gateway, with the passages it was given as citations. There is no
// tool loop.
//
// The definitions are the Application's parameters, the document that the chart writes into the
// definitions ConfigMap; the query process reads it as a mounted file. This package reads three
// of its keys: models.chat.alias, policy.allowExternalModels and assistants. File reads the file
// on every call and parses it again only when it changed; a version that is refused leaves the
// last valid one in use.
//
// Residency: unless policy.allowExternalModels is true, an answer is refused with ErrExternal
// when the gateway lists the chat alias with owned_by "simulation", with no owned_by, or not at
// all. No error of this package carries the text of a message.
package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/cortex/internal/gateway"
	"github.com/openova-io/openova/products/cortex/internal/search"
)

// Errors of an answer. A call the gateway refused with 429 returns a *gateway.RateLimited.
var (
	ErrInvalid  = errors.New("invalid-request")
	ErrNotFound = errors.New("assistant-not-found")
	ErrExternal = errors.New("external-models-not-allowed")
)

var (
	idForm   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	userForm = regexp.MustCompile(`^(user|group|agent):[^\s]{1,200}$|^org:members$`)
	tiers    = []string{"primary", "secondary", "fallback"}
)

// Source is one source of an assistant and its tier.
type Source struct{ ID, Tier string }

// Assistant is one entry of assistants, with the schema's defaults applied.
type Assistant struct {
	ID, Title, Instructions string
	Sources                 []Source
	TopK                    int
	Rerank                  bool
	Users                   []string
}

// Config is what the assistants read from the definitions.
type Config struct {
	ChatAlias           string
	AllowExternalModels bool
	Assistants          []Assistant
}

// Parse returns the Config of a definitions document. It refuses the whole document on the first
// entry that breaks a rule of the schema.
func Parse(doc []byte) (Config, error) {
	j, err := yaml.YAMLToJSON(doc)
	if err != nil {
		return Config{}, fmt.Errorf("assistants: not YAML: %w", err)
	}
	var d struct {
		Models struct {
			Chat struct{ Alias string } `json:"chat"`
		} `json:"models"`
		Policy struct {
			AllowExternalModels bool `json:"allowExternalModels"`
		} `json:"policy"`
		Assistants []struct {
			ID, Title, Instructions string
			Sources                 []struct{ ID, Tier string }
			Retrieval               struct {
				TopK   *int  `json:"topK"`
				Rerank *bool `json:"rerank"`
			}
			Users []string
		} `json:"assistants"`
	}
	if err := json.Unmarshal(j, &d); err != nil {
		return Config{}, fmt.Errorf("assistants: %w", err)
	}
	cfg := Config{ChatAlias: d.Models.Chat.Alias, AllowExternalModels: d.Policy.AllowExternalModels}
	if cfg.ChatAlias == "" || len(d.Assistants) > 200 {
		return Config{}, errors.New("assistants: models.chat.alias is missing or there are more than 200 assistants")
	}
	for i, e := range d.Assistants {
		a := Assistant{ID: e.ID, Title: e.Title, Instructions: e.Instructions, TopK: 8, Rerank: true, Users: e.Users}
		if e.Retrieval.TopK != nil {
			a.TopK = *e.Retrieval.TopK
		}
		if e.Retrieval.Rerank != nil {
			a.Rerank = *e.Retrieval.Rerank
		}
		n := utf8.RuneCountInString(a.Title)
		bad := !idForm.MatchString(a.ID) || slices.ContainsFunc(cfg.Assistants, func(b Assistant) bool { return b.ID == a.ID }) ||
			n < 1 || n > 120 || utf8.RuneCountInString(a.Instructions) > 8000 || len(e.Sources) == 0 || a.TopK < 1 || a.TopK > 50
		for _, s := range e.Sources {
			if s.Tier == "" {
				s.Tier = "primary"
			}
			bad = bad || s.ID == "" || !slices.Contains(tiers, s.Tier)
			a.Sources = append(a.Sources, Source{s.ID, s.Tier})
		}
		for _, u := range a.Users {
			bad = bad || !userForm.MatchString(u)
		}
		if bad {
			return Config{}, fmt.Errorf("assistants[%d] %q breaks a rule of the schema (id, title, instructions, sources, tier, topK or users)", i, a.ID)
		}
		cfg.Assistants = append(cfg.Assistants, a)
	}
	return cfg, nil
}

// File is the definitions document mounted as a file.
type File struct {
	Path string
	Log  *slog.Logger // one line for each version refused after a valid one; nil: none

	mu  sync.Mutex
	raw []byte
	cfg *Config
}

// Config returns the newest valid version of the file. It fails only while no version was valid.
func (f *File) Config() (Config, error) {
	raw, err := os.ReadFile(f.Path)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil && f.cfg != nil && bytes.Equal(raw, f.raw) {
		return *f.cfg, nil
	}
	var cfg Config
	if err == nil {
		f.raw = raw
		cfg, err = Parse(raw)
	}
	switch {
	case err == nil:
		f.cfg = &cfg
	case f.cfg == nil:
		return Config{}, err
	case f.Log != nil:
		f.Log.Warn("assistants: the definitions file was refused; the last valid version stays", "error", err.Error())
	}
	return *f.cfg, nil
}

// Gateway is the part of the model gateway an answer uses; *gateway.Client is one.
type Gateway interface {
	Chat(ctx context.Context, alias string, messages []gateway.Message) (gateway.Completion, error)
	Owners(ctx context.Context) (map[string]string, error)
}

// Service answers with the assistants of Config. Search runs every retrieval.
type Service struct {
	Config  func() (Config, error)
	Search  *search.Service
	Gateway Gateway
}

func (a Assistant) usedBy(principals []string) bool {
	return slices.ContainsFunc(a.Users, func(u string) bool { return slices.Contains(principals, u) })
}

// Usable returns the assistants that one of the principals may use, in the order of the definitions.
func (s *Service) Usable(principals []string) ([]Assistant, error) {
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	out := []Assistant{}
	for _, a := range cfg.Assistants {
		if a.usedBy(principals) {
			out = append(out, a)
		}
	}
	return out, nil
}

// Citation names the passage [Index] of the prompt an answer was given.
type Citation struct {
	Index      int    `json:"index"`
	DocumentID string `json:"documentId"`
	Title      string `json:"title"`
	URI        string `json:"uri"`
	Page       *int   `json:"page"`
}

// Answer is a generation and the passages it was given.
type Answer struct {
	gateway.Completion
	Citations []Citation
}

// Answer asks the assistant id for the principals. The last message is the user's question and
// the text of the retrieval; the assistant's instructions and the passages go first, as a system
// message. An assistant that does not exist or that none of the principals may use is ErrNotFound.
func (s *Service) Answer(ctx context.Context, principals []string, id string, messages []gateway.Message) (Answer, error) {
	if len(messages) == 0 || messages[len(messages)-1].Role != "user" ||
		slices.ContainsFunc(messages, func(m gateway.Message) bool { return !slices.Contains([]string{"system", "user", "assistant"}, m.Role) }) {
		return Answer{}, ErrInvalid
	}
	cfg, err := s.Config()
	if err != nil {
		return Answer{}, err
	}
	i := slices.IndexFunc(cfg.Assistants, func(a Assistant) bool { return a.ID == id && a.usedBy(principals) })
	if i < 0 {
		return Answer{}, ErrNotFound
	}
	if !cfg.AllowExternalModels {
		owners, err := s.Gateway.Owners(ctx)
		if err != nil {
			return Answer{}, err
		}
		if o := owners[cfg.ChatAlias]; o == "" || o == "simulation" {
			return Answer{}, ErrExternal
		}
	}
	query := []rune(strings.TrimSpace(messages[len(messages)-1].Content))
	hits, err := s.retrieve(ctx, principals, cfg.Assistants[i], string(query[:min(len(query), search.MaxQuery)]))
	if err != nil {
		return Answer{}, err
	}
	prompt := cfg.Assistants[i].Instructions + "\n\nPassages:"
	cites := make([]Citation, 0, len(hits))
	for n, h := range hits {
		prompt += "\n\n[" + fmt.Sprint(n+1) + "] " + h.Title + "\n" + h.Chunk.Text
		cites = append(cites, Citation{n + 1, h.DocumentID, h.Title, h.URI, h.Chunk.Page})
	}
	if len(hits) == 0 {
		prompt += " none was found."
	}
	c, err := s.Gateway.Chat(ctx, cfg.ChatAlias, append([]gateway.Message{{Role: "system", Content: prompt}}, messages...))
	if err != nil {
		return Answer{}, err
	}
	return Answer{c, cites}, nil
}

// retrieve searches the tiers in order, each over its sources that the principals may read: the
// secondary sources only when the primary ones gave fewer than topK passages, to fill up to topK,
// and the fallback sources only when the other two gave none.
func (s *Service) retrieve(ctx context.Context, principals []string, a Assistant, query string) ([]search.Result, error) {
	readable, err := s.Search.Store.ReadableSources(ctx, principals)
	if err != nil {
		return nil, err
	}
	out := []search.Result{}
	for _, tier := range tiers {
		if (tier == "secondary" && len(out) >= a.TopK) || (tier == "fallback" && len(out) > 0) {
			break
		}
		var ids []string
		for _, src := range a.Sources {
			if src.Tier == tier && slices.Contains(readable, src.ID) && !slices.Contains(ids, src.ID) {
				ids = append(ids, src.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		n := a.TopK - len(out)
		found, err := s.Search.Search(ctx, principals, search.Request{Query: query, Sources: ids, TopK: &n, Rerank: &a.Rerank})
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}
