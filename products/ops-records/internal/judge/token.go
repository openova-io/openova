package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DefaultServiceAccountFile is where Kubernetes projects the pod's ServiceAccount token.
const DefaultServiceAccountFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// TokenPath is the route of catalyst-api that issues agent tokens.
const TokenPath = "/api/v1/internal/agent-tokens"

// maxTokenBody is the largest answer of the token route that is read.
const maxTokenBody = 64 << 10

// TokenRequest is the body of a request for an agent token.
type TokenRequest struct {
	Organization string `json:"organization"`
	Agent        string `json:"agent"`
	Tier         string `json:"tier"`
	TTLSeconds   int    `json:"ttlSeconds"`
}

// TokenSource gives agent tokens.
type TokenSource interface {
	Token(ctx context.Context, r TokenRequest) (string, error)
}

// Tokens asks catalyst-api for agent tokens. It authenticates with the pod's projected
// ServiceAccount token, read again for every request because the kubelet rotates it.
// A token is never logged and never part of an error.
type Tokens struct {
	BaseURL            string       // catalyst-api, for example http://catalyst-api.catalyst-system.svc:8080
	ServiceAccountFile string       // empty means DefaultServiceAccountFile
	Client             *http.Client // nil means http.DefaultClient
}

// Token asks for one agent token.
func (t *Tokens) Token(ctx context.Context, r TokenRequest) (string, error) {
	file := t.ServiceAccountFile
	if file == "" {
		file = DefaultServiceAccountFile
	}
	raw, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return "", fmt.Errorf("judge: agent token: the ServiceAccount token cannot be read: %w", err)
	}
	sa := strings.TrimSpace(string(raw))
	if sa == "" {
		return "", errors.New("judge: agent token: the ServiceAccount token file is empty")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+TokenPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("judge: agent token: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sa)
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("judge: agent token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return "", fmt.Errorf("judge: agent token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(data, &e)
		return "", fmt.Errorf("judge: agent token refused: HTTP %d %s: %s", resp.StatusCode, e.Error, e.Detail)
	}
	var ok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &ok); err != nil || ok.Token == "" {
		return "", errors.New("judge: agent token: the answer holds no token")
	}
	return ok.Token, nil
}
