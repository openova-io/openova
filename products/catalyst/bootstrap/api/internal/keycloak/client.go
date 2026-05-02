// Package keycloak provides a minimal Keycloak Admin API client for the
// catalyst-api's /auth/handover endpoint.
//
// It implements two operations:
//
//  1. EnsureUser — find-or-create an operator user in the Sovereign realm,
//     set emailVerified=true and seed required actions, then add the user to
//     a named group.
//
//  2. ImpersonateToken — exchange the service-account token for a user
//     access + refresh token pair via RFC 8693 token-exchange
//     (grant_type=urn:ietf:params:oauth:grant-type:token-exchange).
//
// Auth: the client authenticates as a service account (client_credentials)
// before each Admin API call.  The service account credentials are set at
// construction time and injected by main.go from env vars:
//
//	CATALYST_KC_ADDR            — http(s)://keycloak.keycloak.svc:8080
//	CATALYST_KC_REALM           — sovereign (or whatever the realm name is)
//	CATALYST_KC_SA_CLIENT_ID    — e.g. "catalyst-api-server"
//	CATALYST_KC_SA_CLIENT_SECRET
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client wraps the Keycloak Admin REST API for the /auth/handover flow.
type Client struct {
	addr         string // e.g. "http://keycloak.keycloak.svc:8080"
	realm        string // e.g. "sovereign"
	saClientID   string
	saClientSecret string
	http         *http.Client
}

// New returns a Client configured with the given parameters.
func New(addr, realm, saClientID, saClientSecret string) *Client {
	return NewWithHTTP(addr, realm, saClientID, saClientSecret, &http.Client{Timeout: 30 * time.Second})
}

// NewWithHTTP returns a Client using the provided HTTP client (for tests).
func NewWithHTTP(addr, realm, saClientID, saClientSecret string, hc *http.Client) *Client {
	return &Client{
		addr:           strings.TrimRight(addr, "/"),
		realm:          realm,
		saClientID:     saClientID,
		saClientSecret: saClientSecret,
		http:           hc,
	}
}

// EnsureUser finds or creates a user with the given email in the Sovereign
// realm, then adds the user to the named Keycloak group.
//
// On create, emailVerified is set to true and requiredActions is seeded with
// ["UPDATE_PASSWORD", "CONFIGURE_PASSKEY"]. On find, neither is modified
// (idempotent).
//
// Returns the Keycloak user ID (UUID string) on success.
func (c *Client) EnsureUser(ctx context.Context, email, group string) (string, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return "", fmt.Errorf("keycloak.EnsureUser: service account token: %w", err)
	}

	// Try to find existing user by email.
	userID, err := c.findUserByEmail(ctx, saToken, email)
	if err != nil {
		return "", fmt.Errorf("keycloak.EnsureUser: find user: %w", err)
	}

	if userID == "" {
		// Create the user.
		userID, err = c.createUser(ctx, saToken, email)
		if err != nil {
			return "", fmt.Errorf("keycloak.EnsureUser: create user: %w", err)
		}
	}

	// Add to group.
	if group != "" {
		if err := c.addUserToGroup(ctx, saToken, userID, group); err != nil {
			return "", fmt.Errorf("keycloak.EnsureUser: add to group: %w", err)
		}
	}

	return userID, nil
}

// ImpersonateToken exchanges the service-account token for a user-scoped
// token via RFC 8693 token-exchange.
//
// Returns (accessToken, refreshToken, expiresIn seconds, error).
func (c *Client) ImpersonateToken(ctx context.Context, userID, audience string) (string, string, int, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return "", "", 0, fmt.Errorf("keycloak.ImpersonateToken: service account token: %w", err)
	}

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.addr, c.realm)

	data := url.Values{}
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
	data.Set("client_id", c.saClientID)
	data.Set("client_secret", c.saClientSecret)
	data.Set("subject_token", saToken)
	data.Set("subject_token_type", "urn:ietf:params:oauth:token-type:access_token")
	data.Set("requested_token_type", "urn:ietf:params:oauth:token-type:access_token")
	data.Set("requested_subject", userID)
	if audience != "" {
		data.Set("audience", audience)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("keycloak.ImpersonateToken: POST token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("keycloak.ImpersonateToken: token endpoint %d: %s", resp.StatusCode, body)
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", "", 0, fmt.Errorf("keycloak.ImpersonateToken: decode response: %w", err)
	}

	return tok.AccessToken, tok.RefreshToken, tok.ExpiresIn, nil
}

// ── internal helpers ─────────────────────────────────────────────────────────

func (c *Client) serviceAccountToken(ctx context.Context) (string, error) {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.addr, c.realm)

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", c.saClientID)
	data.Set("client_secret", c.saClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: POST token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak: service account token %d: %s", resp.StatusCode, body)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("keycloak: decode token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("keycloak: empty access_token in response")
	}
	return tok.AccessToken, nil
}

func (c *Client) findUserByEmail(ctx context.Context, saToken, email string) (string, error) {
	u := fmt.Sprintf("%s/admin/realms/%s/users?email=%s&exact=true",
		c.addr, c.realm, url.QueryEscape(email))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: GET users: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak: GET users %d: %s", resp.StatusCode, body)
	}

	var users []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &users); err != nil {
		return "", fmt.Errorf("keycloak: decode users: %w", err)
	}
	if len(users) == 0 {
		return "", nil
	}
	return users[0].ID, nil
}

func (c *Client) createUser(ctx context.Context, saToken, email string) (string, error) {
	payload := map[string]interface{}{
		"email":         email,
		"username":      email,
		"emailVerified": true,
		"enabled":       true,
		"requiredActions": []string{
			"UPDATE_PASSWORD",
			"CONFIGURE_PASSKEY",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	u := fmt.Sprintf("%s/admin/realms/%s/users", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: POST users: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("keycloak: create user %d: %s", resp.StatusCode, respBody)
	}

	// Location header contains the new user URL, last segment is the user ID.
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("keycloak: create user: no Location header")
	}
	parts := strings.Split(loc, "/")
	if len(parts) == 0 {
		return "", fmt.Errorf("keycloak: create user: malformed Location: %s", loc)
	}
	return parts[len(parts)-1], nil
}

func (c *Client) addUserToGroup(ctx context.Context, saToken, userID, groupName string) error {
	// Find group by name.
	groupID, err := c.findGroupByName(ctx, saToken, groupName)
	if err != nil {
		return fmt.Errorf("find group %q: %w", groupName, err)
	}
	if groupID == "" {
		// Group doesn't exist — create it.
		groupID, err = c.createGroup(ctx, saToken, groupName)
		if err != nil {
			return fmt.Errorf("create group %q: %w", groupName, err)
		}
	}

	u := fmt.Sprintf("%s/admin/realms/%s/users/%s/groups/%s", c.addr, c.realm, userID, groupID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT user/group: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("keycloak: PUT user/group %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) findGroupByName(ctx context.Context, saToken, name string) (string, error) {
	u := fmt.Sprintf("%s/admin/realms/%s/groups?search=%s&exact=true",
		c.addr, c.realm, url.QueryEscape(name))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: GET groups: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak: GET groups %d: %s", resp.StatusCode, body)
	}

	var groups []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &groups); err != nil {
		return "", fmt.Errorf("keycloak: decode groups: %w", err)
	}
	if len(groups) == 0 {
		return "", nil
	}
	return groups[0].ID, nil
}

func (c *Client) createGroup(ctx context.Context, saToken, name string) (string, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	u := fmt.Sprintf("%s/admin/realms/%s/groups", c.addr, c.realm)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: POST groups: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("keycloak: create group %d", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("keycloak: create group: no Location header")
	}
	parts := strings.Split(loc, "/")
	return parts[len(parts)-1], nil
}
