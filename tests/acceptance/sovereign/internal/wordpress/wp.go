// Package wordpress drives a WordPress site over HTTP the way a visitor and an
// author do: the installer form, the REST API (application-password Basic
// auth) for writes, and anonymous GETs for reads.
//
// It uses the `?rest_route=` form so it works with or without pretty
// permalinks.
package wordpress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Site is one WordPress at https://<host>.
type Site struct {
	Base string // https://host
	HTTP *http.Client

	// Author credentials (application password). Secret — never printed.
	User, AppPassword string
}

// New builds a Site client. insecure skips TLS verification (staging LE).
func New(host string, insecure bool) *Site {
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for staging certs
	}
	// Never reuse a connection across a DNS move: each request re-resolves.
	tr.DisableKeepAlives = true
	return &Site{
		Base: "https://" + strings.TrimSuffix(strings.TrimPrefix(host, "https://"), "/"),
		HTTP: &http.Client{Timeout: 60 * time.Second, Jar: jar, Transport: tr},
	}
}

// Page is a fetched page.
type Page struct {
	Status   int
	FinalURL string
	Body     []byte
}

// Get fetches path following redirects.
func (s *Site) Get(ctx context.Context, path string) (*Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.abs(path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return &Page{Status: res.StatusCode, FinalURL: res.Request.URL.String(), Body: b}, nil
}

func (s *Site) abs(p string) string {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	return s.Base + "/" + strings.TrimPrefix(p, "/")
}

// NeedsInstall reports whether the site is showing the installer.
func NeedsInstall(p *Page) bool {
	return strings.Contains(p.FinalURL, "/wp-admin/install.php") ||
		bytes.Contains(p.Body, []byte(`id="setup"`)) && bytes.Contains(p.Body, []byte("install.php"))
}

// IsConfiguredHome reports whether a page is a rendered, configured WordPress
// front page: 200, not the installer, and carrying a WordPress fingerprint.
func IsConfiguredHome(p *Page) (bool, string) {
	if p.Status != http.StatusOK {
		return false, fmt.Sprintf("home HTTP %d at %s", p.Status, p.FinalURL)
	}
	if NeedsInstall(p) {
		return false, "home redirects to install.php — WordPress is not configured"
	}
	if !bytes.Contains(p.Body, []byte("wp-content")) && !bytes.Contains(p.Body, []byte("wp-json")) {
		return false, "200 but no WordPress fingerprint (wp-content/wp-json) in the body"
	}
	return true, ""
}

// Install completes the installer form (step=2) with the given admin.
func (s *Site) Install(ctx context.Context, title, user, pass, email string) error {
	if _, err := s.Get(ctx, "/wp-admin/install.php"); err != nil {
		return err
	}
	// step=1 language selection is skipped by posting language directly.
	form := url.Values{
		"weblog_title":    {title},
		"user_name":       {user},
		"admin_password":  {pass},
		"admin_password2": {pass},
		"pw_weak":         {"1"},
		"admin_email":     {email},
		"blog_public":     {"0"},
		"language":        {""},
		"Submit":          {"Install WordPress"},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.abs("/wp-admin/install.php?step=2"), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK || !(bytes.Contains(b, []byte("wp-login.php")) || bytes.Contains(b, []byte("Success"))) {
		return fmt.Errorf("installer step=2: HTTP %d, no success marker", res.StatusCode)
	}
	return nil
}

// Post is the subset of a REST post / media object the suite reads.
type Post struct {
	ID        int    `json:"id"`
	Status    string `json:"status"`
	Link      string `json:"link"`
	SourceURL string `json:"source_url"`
	Content   struct {
		Rendered string `json:"rendered"`
	} `json:"content"`
}

func (s *Site) rest(ctx context.Context, method, route string, body io.Reader, hdr map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.Base+"/?rest_route="+url.QueryEscape(route), body)
	if err != nil {
		return nil, nil, err
	}
	req.SetBasicAuth(s.User, s.AppPassword)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	return res, b, err
}

// Whoami checks the author credentials (GET /wp/v2/users/me).
func (s *Site) Whoami(ctx context.Context) error {
	res, b, err := s.rest(ctx, http.MethodGet, "/wp/v2/users/me", nil, nil)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("users/me HTTP %d: %s", res.StatusCode, snippet(b))
	}
	return nil
}

// CreatePost publishes a post and returns it with the wall time of the write.
func (s *Site) CreatePost(ctx context.Context, title, content string) (*Post, time.Duration, error) {
	b, _ := json.Marshal(map[string]string{"title": title, "content": content, "status": "publish"})
	t0 := time.Now()
	res, body, err := s.rest(ctx, http.MethodPost, "/wp/v2/posts", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	d := time.Since(t0)
	if err != nil {
		return nil, d, err
	}
	if res.StatusCode != http.StatusCreated {
		return nil, d, fmt.Errorf("create post HTTP %d: %s", res.StatusCode, snippet(body))
	}
	var p Post
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, d, err
	}
	return &p, d, nil
}

// UploadMedia uploads a file and returns the media object + write wall time.
func (s *Site) UploadMedia(ctx context.Context, filename, mime string, data []byte) (*Post, time.Duration, error) {
	t0 := time.Now()
	res, body, err := s.rest(ctx, http.MethodPost, "/wp/v2/media", bytes.NewReader(data), map[string]string{
		"Content-Type":        mime,
		"Content-Disposition": fmt.Sprintf(`attachment; filename="%s"`, filename),
	})
	d := time.Since(t0)
	if err != nil {
		return nil, d, err
	}
	if res.StatusCode != http.StatusCreated {
		return nil, d, fmt.Errorf("upload media HTTP %d: %s", res.StatusCode, snippet(body))
	}
	var p Post
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, d, err
	}
	if p.SourceURL == "" {
		return nil, d, fmt.Errorf("media %d has no source_url", p.ID)
	}
	return &p, d, nil
}

// ReadPostAnon fetches a published post anonymously by id and reports whether
// the rendered page carries the marker.
func (s *Site) ReadPostAnon(ctx context.Context, id int, marker string) (bool, *Page, error) {
	anon := &Site{Base: s.Base, HTTP: &http.Client{Timeout: s.HTTP.Timeout, Transport: s.HTTP.Transport}}
	p, err := anon.Get(ctx, fmt.Sprintf("/?p=%d", id))
	if err != nil {
		return false, nil, err
	}
	return p.Status == http.StatusOK && bytes.Contains(p.Body, []byte(marker)), p, nil
}

// FetchSHA256 GETs a URL anonymously and returns sha256(body) + status.
func (s *Site) FetchSHA256(ctx context.Context, u string) (string, int, error) {
	anon := &Site{Base: s.Base, HTTP: &http.Client{Timeout: s.HTTP.Timeout, Transport: s.HTTP.Transport}}
	p, err := anon.Get(ctx, u)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(p.Body)
	return hex.EncodeToString(sum[:]), p.Status, nil
}

// RandomPNG renders a small PNG whose pixels are seeded by marker, so every
// run uploads unique bytes (a cached object from a previous run can never
// satisfy the byte-equality check).
func RandomPNG(marker string) ([]byte, string) {
	h := sha256.Sum256([]byte(marker))
	var seed int64
	for _, c := range h[:8] {
		seed = seed<<8 | int64(c)
	}
	r := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// EnsureAuthorScript is the PHP run (via stdin, never argv) inside the
// WordPress container to create the suite's author and mint an application
// password. Login over wp-login.php is not usable: the chart sets the OIDC
// plugin to login_type=auto, which redirects every password login to
// Keycloak. Output line: "APPPW:<password>".
func EnsureAuthorScript(host, login, pass, runID string) string {
	q := func(s string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
	}
	return `<?php
$_SERVER['HTTP_HOST'] = ` + q(host) + `; $_SERVER['HTTPS'] = 'on'; $_SERVER['REQUEST_URI'] = '/';
define('WP_USE_THEMES', false);
require '/var/www/html/wp-load.php';
require_once ABSPATH . 'wp-admin/includes/user.php';
$login = ` + q(login) + `;
$u = get_user_by('login', $login);
if (!$u) {
  $id = wp_insert_user(array('user_login' => $login, 'user_pass' => ` + q(pass) + `, 'user_email' => $login . '@acceptance.invalid', 'role' => 'administrator'));
  if (is_wp_error($id)) { fwrite(STDERR, 'wp_insert_user: ' . $id->get_error_message()); exit(2); }
  $u = get_user_by('id', $id);
}
if (!class_exists('WP_Application_Passwords')) { fwrite(STDERR, 'WP_Application_Passwords unavailable (WordPress < 5.6)'); exit(3); }
$r = WP_Application_Passwords::create_new_application_password($u->ID, array('name' => 'acceptance-' . ` + q(runID) + `));
if (is_wp_error($r)) { fwrite(STDERR, 'app password: ' . $r->get_error_message()); exit(4); }
echo "APPPW:" . $r[0] . "\n";
`
}

// ParseAppPassword extracts the password from the script's output.
func ParseAppPassword(out string) (string, error) {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "APPPW:") {
			if p := strings.TrimSpace(strings.TrimPrefix(l, "APPPW:")); p != "" {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("no application password in script output (%d bytes)", len(out))
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
