// Package gateway is the client of the platform's model gateway, the only way cortex reaches a
// model. It calls four routes of the gateway's address: POST <base>/embeddings in the OpenAI
// shape, POST <base>/rerank ({model, query, documents, top_n} -> {results: [{index,
// relevance_score}]}), POST <base>/chat/completions and GET <base>/models in the OpenAI shape,
// whose owned_by is the back-end key of each alias. The address is the chart value
// modelGateway.baseURL. Every call carries
// the virtual key of the Application as a bearer token; the key is read from its file on each
// call, so a rotated key is used as soon as the file changes. It is never a flag, an
// environment value or a log field.
//
// A failed call returns an error that wraps ErrUnavailable. Its text holds the route and the
// status, never the body of the answer, which may repeat the text that was sent. A call the
// gateway refused with 429 returns a *RateLimited as well, which carries its Retry-After.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

// DefaultBaseURL is the gateway's address inside every Organization's cluster.
const DefaultBaseURL = "http://model-gateway.model-gateway.svc.cluster.local:8080/v1"

// DefaultKeyFile is where the Application's virtual key is mounted.
const DefaultKeyFile = "/var/run/secrets/openova/model-key"

// ErrUnavailable is wrapped by every failed call.
var ErrUnavailable = errors.New("gateway: unavailable")

// maxAnswer bounds the size of an answer that is read.
const maxAnswer = 64 << 20

// Client calls the gateway.
type Client struct {
	base    string
	keyFile string
	http    *http.Client
}

// New returns a client of the gateway at baseURL that reads its key from keyFile.
func New(baseURL, keyFile string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return nil, fmt.Errorf("gateway: %q is not an http or https address", baseURL)
	}
	if keyFile == "" {
		return nil, errors.New("gateway: no key file")
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(baseURL, "/"), keyFile: keyFile, http: hc}, nil
}

// RateLimited is the error of a call the gateway refused with 429. It wraps ErrUnavailable.
type RateLimited struct{ Route, RetryAfter string }

func (e *RateLimited) Error() string {
	return fmt.Sprintf("%v: %s answered 429", ErrUnavailable, e.Route)
}
func (e *RateLimited) Unwrap() error { return ErrUnavailable }

// post sends in as JSON to route and decodes the answer into out; a nil in is a GET.
func (c *Client) post(ctx context.Context, route string, in, out any) error {
	key, err := os.ReadFile(c.keyFile)
	if err != nil || len(bytes.TrimSpace(key)) == 0 {
		return fmt.Errorf("%w: the key file %s cannot be read or is empty", ErrUnavailable, c.keyFile)
	}
	method, body := http.MethodGet, []byte(nil)
	if in != nil {
		method = http.MethodPost
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+route, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(key)))
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: the call failed", ErrUnavailable, route)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
		if resp.StatusCode == http.StatusTooManyRequests {
			return &RateLimited{Route: route, RetryAfter: resp.Header.Get("Retry-After")}
		}
		return fmt.Errorf("%w: %s answered %d", ErrUnavailable, route, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(out); err != nil {
		return fmt.Errorf("%w: %s: the answer is not the expected JSON", ErrUnavailable, route)
	}
	return nil
}

// Embedder embeds texts with one alias of the gateway. It is the Embedder port of ingestion.
type Embedder struct {
	c          *Client
	alias      string
	dimensions int
}

// Embedder returns the embedder of alias, whose vectors have dimensions values.
func (c *Client) Embedder(alias string, dimensions int) *Embedder {
	return &Embedder{c: c, alias: alias, dimensions: dimensions}
}

// Dimensions is the length of every vector Embed returns.
func (e *Embedder) Dimensions() int { return e.dimensions }

// Embed returns one vector per text, in the order of texts.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := e.c.post(ctx, "/embeddings", map[string]any{"model": e.alias, "input": texts}, &out); err != nil {
		return nil, err
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("%w: /embeddings returned %d vectors for %d texts", ErrUnavailable, len(out.Data), len(texts))
	}
	vectors := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) || vectors[d.Index] != nil || len(d.Embedding) != e.dimensions {
			return nil, fmt.Errorf("%w: /embeddings returned index %d with %d values; want each index once with %d values",
				ErrUnavailable, d.Index, len(d.Embedding), e.dimensions)
		}
		vectors[d.Index] = d.Embedding
	}
	return vectors, nil
}

// Ranked is one document of a rerank answer: its index in the request and its score.
type Ranked struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

// Reranker orders documents by their relevance to a query with one alias of the gateway.
type Reranker struct {
	c     *Client
	alias string
}

// Reranker returns the reranker of alias.
func (c *Client) Reranker(alias string) *Reranker { return &Reranker{c: c, alias: alias} }

// Rerank returns at most topN documents, best first.
func (r *Reranker) Rerank(ctx context.Context, query string, documents []string, topN int) ([]Ranked, error) {
	var out struct {
		Results []Ranked `json:"results"`
	}
	in := map[string]any{"model": r.alias, "query": query, "documents": documents, "top_n": topN}
	if err := r.c.post(ctx, "/rerank", in, &out); err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	for _, x := range out.Results {
		if x.Index < 0 || x.Index >= len(documents) || seen[x.Index] {
			return nil, fmt.Errorf("%w: /rerank returned index %d for %d documents", ErrUnavailable, x.Index, len(documents))
		}
		seen[x.Index] = true
	}
	sort.SliceStable(out.Results, func(i, j int) bool { return out.Results[i].Score > out.Results[j].Score })
	if len(out.Results) > topN {
		out.Results = out.Results[:topN]
	}
	return out.Results, nil
}

// Message is one message of a chat, in the OpenAI shape.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Completion is the first choice of a chat answer, with the answer's id, time and usage.
type Completion struct {
	ID           string
	Created      int64
	Content      string
	FinishReason string
	Usage        json.RawMessage
}

// Chat asks the chat alias for one answer to messages, not streamed.
func (c *Client) Chat(ctx context.Context, alias string, messages []Message) (Completion, error) {
	var out struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := c.post(ctx, "/chat/completions", map[string]any{"model": alias, "messages": messages}, &out); err != nil {
		return Completion{}, err
	}
	if len(out.Choices) == 0 {
		return Completion{}, fmt.Errorf("%w: /chat/completions returned no choice", ErrUnavailable)
	}
	ch := out.Choices[0]
	return Completion{ID: out.ID, Created: out.Created, Content: ch.Message.Content, FinishReason: ch.FinishReason, Usage: out.Usage}, nil
}

// Owners returns the owned_by of each alias the key may use: the alias's back-end key, or "" when
// the object carries none.
func (c *Client) Owners(ctx context.Context) (map[string]string, error) {
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/models", nil, &out); err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(out.Data))
	for _, m := range out.Data {
		owners[m.ID] = m.OwnedBy
	}
	return owners, nil
}
