package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"

	"github.com/openova-io/openova/core/services/auth/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// Handler holds dependencies for auth HTTP handlers.
type Handler struct {
	Store              *store.Store
	Valkey             valkey.Client
	Producer           *events.Producer
	JWTSecret          []byte
	JWTRefreshSecret   []byte
	GoogleClientID     string
	GoogleClientSecret string
	BaseURL            string
	SMTPHost           string
	SMTPPort           string
	FromEmail          string
	SMTPUser           string
	SMTPPass           string
}

// --- Request / Response types ---

type magicLinkRequest struct {
	Email string `json:"email"`
}

type verifyRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type googleCallbackRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type tokenResponse struct {
	AccessToken  string     `json:"token"`
	RefreshToken string     `json:"refresh_token"`
	User         *store.User `json:"user"`
}

type googleTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

type googleUserInfo struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// --- Endpoints ---

// GoogleLogin constructs the Google OAuth authorization URL and returns it as JSON.
func (h *Handler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	redirectURI := r.URL.Query().Get("redirect_uri")
	if redirectURI == "" {
		redirectURI = h.BaseURL + "/auth/google/callback"
	}

	// Generate CSRF state token and store in Valkey (10 min TTL).
	state := uuid.New().String()
	ctx := r.Context()
	key := "oauth-state:" + state
	cmd := h.Valkey.B().Set().Key(key).Value("1").Ex(10 * time.Minute).Build()
	if err := h.Valkey.Do(ctx, cmd).Error(); err != nil {
		slog.Error("failed to store oauth state", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	params := url.Values{
		"client_id":     {h.GoogleClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
	}

	authURL := "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode()
	respond.OK(w, map[string]string{"url": authURL})
}

// Magic link brute-force hardening (#81):
// - Code TTL reduced from 15m → 5m (smaller attack window).
// - Max 5 wrong verify attempts per code; the 6th wrong attempt invalidates
//   the code (deletes `magic:<email>` and `magic:<email>:attempts`).
// - Per-IP leaky bucket: at most `magicRateMax` verify attempts per
//   `magicRateWindow` per source IP.
const (
	magicCodeTTL     = 5 * time.Minute
	magicMaxAttempts = 5
	magicRateMax     = 10 // verify requests per IP per window
	magicRateWindow  = 1 * time.Minute
)

// SendMagicLink generates a 6-digit code, stores it in Valkey, and emails it.
func (h *Handler) SendMagicLink(w http.ResponseWriter, r *http.Request) {
	var req magicLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		respond.Error(w, http.StatusBadRequest, "email is required")
		return
	}

	code, err := generateCode()
	if err != nil {
		slog.Error("failed to generate code", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Store code in Valkey with a short TTL. A fresh code resets any prior
	// attempt counter so a legitimate user who re-requested isn't locked out
	// by stale failures against the old code.
	ctx := r.Context()
	key := "magic:" + req.Email
	attemptsKey := key + ":attempts"
	cmd := h.Valkey.B().Set().Key(key).Value(code).Ex(magicCodeTTL).Build()
	if err := h.Valkey.Do(ctx, cmd).Error(); err != nil {
		slog.Error("failed to store magic code", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Clear any prior attempts — the old code is gone.
	h.Valkey.Do(ctx, h.Valkey.B().Del().Key(attemptsKey).Build())

	// Send email with the code.
	if err := h.sendCodeEmail(req.Email, code); err != nil {
		slog.Error("failed to send magic link email", "error", err, "email", req.Email)
		respond.Error(w, http.StatusInternalServerError, "failed to send email")
		return
	}

	respond.OK(w, map[string]string{"message": "check your email"})
}

// VerifyMagicLink validates the code and issues tokens.
func (h *Handler) VerifyMagicLink(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Code == "" {
		respond.Error(w, http.StatusBadRequest, "email and code are required")
		return
	}

	ctx := r.Context()
	ip := clientIP(r)

	// Per-IP leaky bucket — blocks a single host from racing through the
	// 10^6 code space regardless of which email it targets. The gateway's
	// per-IP rate limiter is bypassed when XFF is spoofable, so we enforce
	// here as well; the direct TCP peer is the fallback.
	if err := h.checkMagicRateLimit(ctx, ip); err != nil {
		respond.Error(w, http.StatusTooManyRequests, "too many attempts, please try again later")
		return
	}

	key := "magic:" + req.Email
	attemptsKey := key + ":attempts"

	// Retrieve stored code from Valkey.
	getCmd := h.Valkey.B().Get().Key(key).Build()
	storedCode, err := h.Valkey.Do(ctx, getCmd).ToString()
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	if storedCode != req.Code {
		// Count the wrong attempt. On the Nth wrong attempt, invalidate the
		// code entirely — further guesses fall through to "invalid or
		// expired" regardless of what they send.
		attempts, _ := h.Valkey.Do(ctx,
			h.Valkey.B().Incr().Key(attemptsKey).Build()).ToInt64()
		if attempts == 1 {
			// First failure — align TTL with the code so we don't leak
			// counter state after the code is gone.
			h.Valkey.Do(ctx,
				h.Valkey.B().Expire().Key(attemptsKey).Seconds(int64(magicCodeTTL.Seconds())).Build())
		}
		if attempts >= magicMaxAttempts {
			h.Valkey.Do(ctx, h.Valkey.B().Del().Key(key).Build())
			h.Valkey.Do(ctx, h.Valkey.B().Del().Key(attemptsKey).Build())
			respond.Error(w, http.StatusUnauthorized, "code invalidated after too many attempts")
			return
		}
		respond.Error(w, http.StatusUnauthorized, "invalid code")
		return
	}

	// Delete code + attempt counter after successful verification.
	h.Valkey.Do(ctx, h.Valkey.B().Del().Key(key).Build())
	h.Valkey.Do(ctx, h.Valkey.B().Del().Key(attemptsKey).Build())

	// Find or create user.
	user, err := h.findOrCreateUser(ctx, req.Email, "", "")
	if err != nil {
		slog.Error("failed to find or create user", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Issue tokens.
	resp, err := h.issueTokens(ctx, user)
	if err != nil {
		slog.Error("failed to issue tokens", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Publish login event.
	h.publishLoginEvent(ctx, user)

	respond.OK(w, resp)
}

// RefreshToken rotates the refresh token and issues new access + refresh tokens.
func (h *Handler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RefreshToken == "" {
		respond.Error(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	ctx := r.Context()
	tokenHash := hashToken(req.RefreshToken)

	userID, err := h.Store.ValidateRefreshToken(ctx, tokenHash)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	// Delete old token (rotation).
	h.Store.DeleteRefreshToken(ctx, tokenHash)

	user, err := h.Store.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		respond.Error(w, http.StatusUnauthorized, "user not found")
		return
	}

	resp, err := h.issueTokens(ctx, user)
	if err != nil {
		slog.Error("failed to issue tokens", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	respond.OK(w, resp)
}

// GoogleCallback exchanges an authorization code for tokens and user info.
func (h *Handler) GoogleCallback(w http.ResponseWriter, r *http.Request) {
	var req googleCallbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Code == "" || req.RedirectURI == "" {
		respond.Error(w, http.StatusBadRequest, "code and redirect_uri are required")
		return
	}

	ctx := r.Context()

	// Exchange code for access token.
	tokenResp, err := h.exchangeGoogleCode(req.Code, req.RedirectURI)
	if err != nil {
		slog.Error("google token exchange failed", "error", err)
		respond.Error(w, http.StatusBadRequest, "failed to exchange code")
		return
	}

	// Fetch user info from Google.
	userInfo, err := h.fetchGoogleUserInfo(tokenResp.AccessToken)
	if err != nil {
		slog.Error("google userinfo fetch failed", "error", err)
		respond.Error(w, http.StatusBadRequest, "failed to fetch user info")
		return
	}

	// Look up by provider first, then by email for merging.
	user, err := h.Store.FindUserByProvider(ctx, "google", userInfo.ID)
	if err != nil {
		slog.Error("failed to find user by provider", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		// Try matching by email for account merging.
		user, err = h.findOrCreateUser(ctx, userInfo.Email, userInfo.Name, userInfo.Picture)
		if err != nil {
			slog.Error("failed to find or create user", "error", err)
			respond.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	// Link Google provider to user.
	if err := h.Store.UpsertAuthProvider(ctx, user.ID, "google", userInfo.ID); err != nil {
		slog.Error("failed to upsert auth provider", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Issue tokens.
	resp, err := h.issueTokens(ctx, user)
	if err != nil {
		slog.Error("failed to issue tokens", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.publishLoginEvent(ctx, user)

	respond.OK(w, resp)
}

// Login authenticates a user with email + password (for admin login).
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		respond.Error(w, http.StatusBadRequest, "email and password are required")
		return
	}

	user, err := h.Store.VerifyPassword(r.Context(), req.Email, req.Password)
	if err != nil {
		slog.Error("login verify failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		respond.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	resp, err := h.issueTokens(r.Context(), user)
	if err != nil {
		slog.Error("failed to issue tokens", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.publishLoginEvent(r.Context(), user)

	// Return in the format the admin app expects. Admin now also stores the
	// refresh_token so silent refresh works there (#84); unchanged for any
	// callers that ignore the extra field.
	respond.OK(w, map[string]any{
		"token":         resp.AccessToken,
		"refresh_token": resp.RefreshToken,
		"user":          resp.User,
	})
}

// GetMe returns the authenticated user's profile.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	user, err := h.Store.GetUserByID(r.Context(), userID)
	if err != nil || user == nil {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}

	respond.OK(w, map[string]any{"user": user})
}

// AdminGetUser returns a user by ID. Superadmin role required — used by
// notification service (and potentially other internal services) to
// enrich event payloads with owner email / name without introducing a
// direct DB coupling. Never expose on the public gateway.
func (h *Handler) AdminGetUser(w http.ResponseWriter, r *http.Request) {
	if middleware.RoleFromContext(r.Context()) != "superadmin" {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		respond.Error(w, http.StatusBadRequest, "id is required")
		return
	}
	user, err := h.Store.GetUserByID(r.Context(), id)
	if err != nil {
		slog.Error("admin get user failed", "id", id, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to get user")
		return
	}
	if user == nil {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}
	respond.OK(w, map[string]any{"user": user})
}

// Logout deletes the provided refresh token.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req logoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RefreshToken == "" {
		respond.Error(w, http.StatusBadRequest, "refresh_token is required")
		return
	}

	tokenHash := hashToken(req.RefreshToken)
	h.Store.DeleteRefreshToken(r.Context(), tokenHash)

	respond.OK(w, map[string]string{"message": "logged out"})
}

// LogoutAll revokes every refresh token for the authenticated user so a
// stolen token on another device can't keep the session alive after the
// primary device signs out (#88). Requires a valid access token — the
// JWT middleware extracts the user ID from `sub`.
func (h *Handler) LogoutAll(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := h.Store.DeleteUserRefreshTokens(r.Context(), userID); err != nil {
		slog.Error("failed to revoke user refresh tokens", "error", err, "user_id", userID)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	respond.OK(w, map[string]string{"message": "all sessions signed out"})
}

// --- Helpers ---

// checkMagicRateLimit enforces a per-IP leaky bucket on /auth/verify.
// Returns nil if the call is allowed, non-nil (and the caller should 429) if
// the IP has exceeded `magicRateMax` in the current `magicRateWindow`.
// We use Valkey's INCR + EXPIRE pattern: first hit sets the window, each hit
// increments, caller decides after N. If Valkey is unreachable we fail OPEN
// (log + allow) — the alternative is to block legitimate traffic on a cache
// blip, which is worse than the marginal brute-force risk.
func (h *Handler) checkMagicRateLimit(ctx context.Context, ip string) error {
	if ip == "" {
		return nil
	}
	key := "ratelimit:magic-verify:" + ip
	n, err := h.Valkey.Do(ctx, h.Valkey.B().Incr().Key(key).Build()).ToInt64()
	if err != nil {
		slog.Warn("magic rate-limit incr failed, failing open", "error", err)
		return nil
	}
	if n == 1 {
		h.Valkey.Do(ctx,
			h.Valkey.B().Expire().Key(key).Seconds(int64(magicRateWindow.Seconds())).Build())
	}
	if n > int64(magicRateMax) {
		return fmt.Errorf("rate limited: %d > %d", n, magicRateMax)
	}
	return nil
}

// clientIP returns the best-effort client IP from the request.
// Prefers X-Forwarded-For (gateway sets this, first hop wins) and falls back
// to the direct TCP peer. The gateway strips spoofed XFF before forwarding,
// so trusting it here is safe in our topology.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry is the original client; remaining entries are the
		// chain of proxies.
		if comma := strings.IndexByte(xff, ','); comma > 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host := r.RemoteAddr
	if colon := strings.LastIndexByte(host, ':'); colon > 0 {
		host = host[:colon]
	}
	return host
}

// findOrCreateUser retrieves a user by email, creating one if it doesn't exist.
func (h *Handler) findOrCreateUser(ctx context.Context, email, name, avatarURL string) (*store.User, error) {
	user, err := h.Store.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if user != nil {
		return user, nil
	}
	return h.Store.CreateUser(ctx, email, name, avatarURL, "member")
}

// issueTokens creates a JWT access token and a refresh token, persisting the refresh token in the DB.
func (h *Handler) issueTokens(ctx context.Context, user *store.User) (*tokenResponse, error) {
	now := time.Now()

	// Access token: 15 minutes.
	accessClaims := jwt.MapClaims{
		"sub":   user.ID,
		"email": user.Email,
		"role":  user.Role,
		"iat":   now.Unix(),
		"exp":   now.Add(15 * time.Minute).Unix(),
	}
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessStr, err := accessToken.SignedString(h.JWTSecret)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}

	// Refresh token: random UUID, stored hashed in DB, 30 days.
	refreshRaw := uuid.New().String()
	refreshHash := hashToken(refreshRaw)
	expiresAt := now.Add(30 * 24 * time.Hour)

	if err := h.Store.StoreRefreshToken(ctx, user.ID, refreshHash, expiresAt); err != nil {
		return nil, fmt.Errorf("store refresh token: %w", err)
	}

	return &tokenResponse{
		AccessToken:  accessStr,
		RefreshToken: refreshRaw,
		User:         user,
	}, nil
}

// hashToken returns the hex-encoded SHA-256 hash of a token string.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// generateCode returns a cryptographically random 6-digit string.
func generateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// sendCodeEmail delivers the magic link code via SMTP.
// Uses a custom SMTP client to set a proper EHLO hostname (scratch containers lack /etc/hostname)
// and authenticates with PLAIN when SMTP_USER/SMTP_PASS are configured.
func (h *Handler) sendCodeEmail(to, code string) error {
	addr := h.SMTPHost + ":" + h.SMTPPort
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: Your login code\r\n\r\nYour login code: %s\r\n",
		h.FromEmail, to, code,
	)

	conn, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer conn.Close()

	if err := conn.Hello("openova.io"); err != nil {
		return fmt.Errorf("smtp ehlo: %w", err)
	}
	if h.SMTPUser != "" && h.SMTPPass != "" {
		// Upgrade to TLS before sending credentials (STARTTLS on port 587).
		tlsConfig := &tls.Config{ServerName: h.SMTPHost, InsecureSkipVerify: true}
		if err := conn.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
		auth := smtp.PlainAuth("", h.SMTPUser, h.SMTPPass, h.SMTPHost)
		if err := conn.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := conn.Mail(h.FromEmail); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := conn.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := conn.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return conn.Quit()
}

// exchangeGoogleCode exchanges an authorization code for Google tokens.
func (h *Handler) exchangeGoogleCode(code, redirectURI string) (*googleTokenResponse, error) {
	data := url.Values{
		"code":          {code},
		"client_id":     {h.GoogleClientID},
		"client_secret": {h.GoogleClientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}

	resp, err := http.PostForm("https://oauth2.googleapis.com/token", data)
	if err != nil {
		return nil, fmt.Errorf("google token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("google token error (status %d): %s", resp.StatusCode, body)
	}

	var tokenResp googleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode google token: %w", err)
	}
	return &tokenResp, nil
}

// fetchGoogleUserInfo retrieves the authenticated user's profile from Google.
func (h *Handler) fetchGoogleUserInfo(accessToken string) (*googleUserInfo, error) {
	req, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google userinfo request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("google userinfo error (status %d): %s", resp.StatusCode, body)
	}

	var info googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode google userinfo: %w", err)
	}
	return &info, nil
}

// publishLoginEvent sends a user.login event to RedPanda asynchronously.
func (h *Handler) publishLoginEvent(_ context.Context, user *store.User) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		evt, err := events.NewEvent("user.login", "auth-service", "", map[string]string{
			"user_id": user.ID,
			"email":   user.Email,
		})
		if err != nil {
			slog.Error("failed to create login event", "error", err)
			return
		}
		if err := h.Producer.Publish(ctx, "auth.events", evt); err != nil {
			slog.Warn("failed to publish login event (non-blocking)", "error", err)
		}
	}()
}
