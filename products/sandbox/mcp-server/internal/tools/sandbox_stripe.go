// sandbox_stripe.go — real handlers for sandbox.stripe.* (per-Sandbox
// Stripe account binding + product / price / checkout-session ops).
//
// Scope (architecture.md §3): agents shipping monetised apps from inside
// a Sandbox need a way to bind the operator's Stripe account ONCE and
// then drive product/price/checkout flows without ever round-tripping a
// raw secret through prompts. The MCP server's role is to:
//
//   1. Accept the Stripe API key via `sandbox.stripe.bindAccount`, store
//      it in the per-Sandbox K8s Secret (`sandbox-<owner-uid>-secrets`,
//      key `stripe_api_key`) — same crypto-at-rest path the rest of the
//      sandbox.secrets.* tools use. Return a MASKED confirmation
//      (`sk_…last4`) so the agent + operator can verify what got bound
//      without the value re-appearing in transcripts.
//
//   2. Read the stored key on every subsequent call (`listProducts`,
//      `listPrices`, `createCheckoutSession`) — the agent NEVER passes
//      the key on the call wire after first bind. The key flows
//      apiserver → MCP pod env (via Secret mount lookup) → Stripe.
//
//   3. Drive the Stripe REST API directly over HTTPS (no SDK dep). The
//      surface we need is tiny (`GET /v1/products`, `GET /v1/prices`,
//      `POST /v1/checkout/sessions`) and adding `stripe-go` would pull
//      in ~80 transitive packages for four endpoints. Inline HTTP keeps
//      the mcp-server binary lean AND matches the codebase's existing
//      "talk to api.stripe.com over HTTPS with form-urlencoded bodies"
//      pattern (core/services/billing only imports stripe-go for
//      webhook-signature verification + checkout-session creation in
//      the billing service path, which is a different process).
//
// Auth: same shape as sandbox.secrets.* / sandbox.storage.* —
// claims.OrgID must match env.OrgID, RequiredCapability =
// "sandbox.stripe".
//
// Hard read-only-cluster rule (DoD): these handlers ONLY mutate the
// per-Sandbox Secret (when bindAccount writes the key) and ONLY read
// from it elsewhere. No k8s.write to anything outside the per-Sandbox
// Secret. The Stripe API itself is the destination of every product /
// price / checkout call.
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// stripeSecretKeyName is the data-key under which the Stripe API key
// lives inside the per-Sandbox Secret store (`sandbox-<owner-uid>-secrets`).
// Pinned so listProducts / listPrices / createCheckoutSession can fetch
// it deterministically without an arg.
const stripeSecretKeyName = "stripe_api_key"

// stripeAPIBase is the canonical Stripe REST API root. Overridable by
// tests via stripeHTTPClient + stripeAPIBaseOverride below.
const stripeAPIBase = "https://api.stripe.com"

// stripeRequestTimeout caps every Stripe HTTPS call. 15s matches the
// default the canonical stripe-go client uses.
const stripeRequestTimeout = 15 * time.Second

// stripeHTTPClient is the http.Client every Stripe-bound call routes
// through. Hoisted so unit tests can swap a stub against httptest.Server
// without dialling out to api.stripe.com.
var stripeHTTPClient = &http.Client{Timeout: stripeRequestTimeout}

// stripeAPIBaseOverride lets tests redirect requests to a httptest.Server
// URL. Empty in production; the call helpers fall back to stripeAPIBase.
var stripeAPIBaseOverride = ""

// stripeKeyPrefixes — Stripe API keys carry one of these documented
// prefixes (`sk_live_`, `sk_test_`, `rk_live_`, `rk_test_`). Accept all
// four so the agent can bind either a secret key or a restricted key.
// Refuse anything else — stops obvious typos (`pk_live_` publishable
// keys) from being stored as authoritative.
var stripeKeyPrefixes = []string{"sk_live_", "sk_test_", "rk_live_", "rk_test_"}

// --- helpers -----------------------------------------------------------

// stripeAPIBaseURL returns the effective base URL — test override if
// set, the canonical api.stripe.com otherwise.
func stripeAPIBaseURL() string {
	if stripeAPIBaseOverride != "" {
		return stripeAPIBaseOverride
	}
	return stripeAPIBase
}

// validateStripeKey checks the key matches one of the documented
// prefixes AND has a non-trivial length. Stops obvious typos /
// publishable-key mix-ups from being stored.
func validateStripeKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("`api_key` is required")
	}
	if len(key) < 16 {
		return fmt.Errorf("`api_key` looks too short (got %d chars, expected >=16)", len(key))
	}
	for _, p := range stripeKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return nil
		}
	}
	return fmt.Errorf("`api_key` must start with one of %v (got %q)", stripeKeyPrefixes, key[:min(len(key), 8)])
}

// maskStripeKey returns `<prefix>…<last4>` so the agent + operator can
// verify which key got bound without the value re-appearing in clear.
// Example: `sk_test_…xY12`.
func maskStripeKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 12 {
		return "***"
	}
	// Find the prefix `sk_test_` / `rk_live_` etc — keep through the
	// second underscore + last 4 chars.
	prefix := ""
	for _, p := range stripeKeyPrefixes {
		if strings.HasPrefix(key, p) {
			prefix = p
			break
		}
	}
	if prefix == "" {
		return "***" + key[len(key)-4:]
	}
	return prefix + "…" + key[len(key)-4:]
}

// readStripeKey fetches the bound Stripe API key from the per-Sandbox
// Secret store. Returns ("", structured-not-found-error) when the Secret
// or the `stripe_api_key` data-key isn't set so callers can hand the
// agent a clean "run bindAccount first" message.
func readStripeKey(ctx context.Context) (string, error) {
	env, ns, name, err := requireSandboxSecretsScope(ctx)
	if err != nil {
		return "", err
	}
	_ = env
	client, err := dynamicClient(ctx)
	if err != nil {
		return "", err
	}
	obj, err := client.Resource(secretsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("sandbox.stripe: no Stripe account bound yet — call sandbox.stripe.bindAccount first (Secret %s/%s does not exist)", ns, name)
		}
		return "", fmt.Errorf("sandbox.stripe: GET Secret %s/%s: %w", ns, name, err)
	}
	if err := assertManagedBy(obj, name); err != nil {
		return "", err
	}
	data, _, _ := unstructured.NestedMap(obj.Object, "data")
	enc, ok := data[stripeSecretKeyName].(string)
	if !ok || enc == "" {
		return "", fmt.Errorf("sandbox.stripe: no Stripe account bound yet — call sandbox.stripe.bindAccount first (Secret %s/%s has no `%s` key)", ns, name, stripeSecretKeyName)
	}
	decoded, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("sandbox.stripe: decode `%s`: %w", stripeSecretKeyName, err)
	}
	return strings.TrimSpace(string(decoded)), nil
}

// stripeDo issues an authenticated Stripe REST call. Returns the parsed
// JSON body, the HTTP status, and any transport / decode error. On
// non-2xx, attempts to parse the Stripe error envelope into a clean
// message rather than dumping raw HTML.
func stripeDo(ctx context.Context, key, method, path string, form url.Values) (map[string]any, int, error) {
	full := strings.TrimRight(stripeAPIBaseURL(), "/") + path
	var body io.Reader
	if method != http.MethodGet && form != nil {
		body = strings.NewReader(form.Encode())
	} else if method == http.MethodGet && len(form) > 0 {
		if strings.Contains(full, "?") {
			full += "&" + form.Encode()
		} else {
			full += "?" + form.Encode()
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, 0, fmt.Errorf("stripe %s %s: build request: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if method != http.MethodGet && form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	// Pin a Stripe-Version header so a future API rev (line items
	// reshape, etc.) does NOT silently break us. Bumped explicitly when
	// we re-test against a newer Stripe API rev.
	req.Header.Set("Stripe-Version", "2024-06-20")
	resp, err := stripeHTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("stripe %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		// Stripe always returns JSON for both success + error envelopes;
		// a decode failure here is itself a hard error (HTML page from a
		// proxy in the middle, etc.).
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("stripe %s %s: decode body (status=%d, %d bytes): %w", method, path, resp.StatusCode, len(raw), err)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Surface Stripe's structured error envelope: {error: {type,
		// code, message, ...}}.
		if errObj, ok := out["error"].(map[string]any); ok {
			msg, _ := errObj["message"].(string)
			code, _ := errObj["code"].(string)
			typ, _ := errObj["type"].(string)
			return out, resp.StatusCode, fmt.Errorf("stripe %s %s: %d %s (type=%s code=%s): %s",
				method, path, resp.StatusCode, http.StatusText(resp.StatusCode), typ, code, msg)
		}
		return out, resp.StatusCode, fmt.Errorf("stripe %s %s: %d %s", method, path, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return out, resp.StatusCode, nil
}

// --- sandbox.stripe.bindAccount ----------------------------------------

type sandboxStripeBindAccountArgs struct {
	APIKey string `json:"api_key"`
}

func sandboxStripeBindAccount(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxStripeBindAccountArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.stripe.bindAccount: invalid arguments: %w", err)
	}
	if err := validateStripeKey(args.APIKey); err != nil {
		return nil, fmt.Errorf("sandbox.stripe.bindAccount: %w", err)
	}
	key := strings.TrimSpace(args.APIKey)
	env, ns, name, err := requireSandboxSecretsScope(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(key))

	existing, getErr := client.Resource(secretsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("sandbox.stripe.bindAccount: GET Secret %s/%s: %w", ns, name, getErr)
	}
	if apierrors.IsNotFound(getErr) {
		obj := buildSecretObject(env, ns, name)
		data, _ := obj.Object["data"].(map[string]any)
		data[stripeSecretKeyName] = encoded
		obj.Object["data"] = data
		created, err := client.Resource(secretsGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("sandbox.stripe.bindAccount: CREATE Secret %s/%s: %w", ns, name, err)
		}
		return map[string]any{
			"status":          "Bound",
			"masked":          maskStripeKey(key),
			"namespace":       ns,
			"secret":          name,
			"dataKey":         stripeSecretKeyName,
			"resourceVersion": created.GetResourceVersion(),
			"note":            "Stripe API key stored in the Sandbox Secret store. Subsequent sandbox.stripe.* calls read it implicitly — do NOT pass it on the wire again.",
		}, nil
	}
	if err := assertManagedBy(existing, name); err != nil {
		return nil, err
	}
	data, _, _ := unstructured.NestedMap(existing.Object, "data")
	if data == nil {
		data = map[string]any{}
	}
	previouslyBound := false
	if _, ok := data[stripeSecretKeyName]; ok {
		previouslyBound = true
	}
	data[stripeSecretKeyName] = encoded
	if err := unstructured.SetNestedMap(existing.Object, data, "data"); err != nil {
		return nil, fmt.Errorf("sandbox.stripe.bindAccount: set nested data: %w", err)
	}
	updated, err := client.Resource(secretsGVR).Namespace(ns).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox.stripe.bindAccount: UPDATE Secret %s/%s: %w", ns, name, err)
	}
	status := "Bound"
	if previouslyBound {
		status = "Rebound"
	}
	return map[string]any{
		"status":          status,
		"masked":          maskStripeKey(key),
		"namespace":       ns,
		"secret":          name,
		"dataKey":         stripeSecretKeyName,
		"resourceVersion": updated.GetResourceVersion(),
	}, nil
}

func schemaSandboxStripeBindAccount() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"api_key"},
		"properties": map[string]any{
			"api_key": map[string]any{
				"type":        "string",
				"description": "Stripe secret or restricted key (`sk_live_…` / `sk_test_…` / `rk_live_…` / `rk_test_…`). Stored in the per-Sandbox Secret; subsequent sandbox.stripe.* tools read it implicitly.",
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.stripe.listProducts ---------------------------------------

type sandboxStripeListProductsArgs struct {
	Limit  int    `json:"limit,omitempty"`
	Active *bool  `json:"active,omitempty"`
	StartingAfter string `json:"starting_after,omitempty"`
}

func sandboxStripeListProducts(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxStripeListProductsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("sandbox.stripe.listProducts: invalid arguments: %w", err)
		}
	}
	key, err := readStripeKey(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("limit", stripeLimitOr(args.Limit, 20))
	if args.Active != nil {
		if *args.Active {
			q.Set("active", "true")
		} else {
			q.Set("active", "false")
		}
	}
	if args.StartingAfter != "" {
		q.Set("starting_after", args.StartingAfter)
	}
	body, status, err := stripeDo(ctx, key, http.MethodGet, "/v1/products", q)
	if err != nil {
		return nil, fmt.Errorf("sandbox.stripe.listProducts: %w", err)
	}
	products, _ := body["data"].([]any)
	hasMore, _ := body["has_more"].(bool)
	return map[string]any{
		"status":   "OK",
		"products": products,
		"count":    len(products),
		"hasMore":  hasMore,
		"httpStatus": status,
	}, nil
}

func schemaSandboxStripeListProducts() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Page size (Stripe caps at 100; default 20)."},
			"active":        map[string]any{"type": "boolean", "description": "Filter by active flag (omit → both)."},
			"starting_after": map[string]any{"type": "string", "description": "Stripe cursor — pass the last product `id` from a previous page to fetch the next page."},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.stripe.listPrices -----------------------------------------

type sandboxStripeListPricesArgs struct {
	ProductID     string `json:"product_id,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Active        *bool  `json:"active,omitempty"`
	StartingAfter string `json:"starting_after,omitempty"`
}

func sandboxStripeListPrices(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxStripeListPricesArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("sandbox.stripe.listPrices: invalid arguments: %w", err)
		}
	}
	key, err := readStripeKey(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("limit", stripeLimitOr(args.Limit, 20))
	if args.ProductID != "" {
		q.Set("product", args.ProductID)
	}
	if args.Active != nil {
		if *args.Active {
			q.Set("active", "true")
		} else {
			q.Set("active", "false")
		}
	}
	if args.StartingAfter != "" {
		q.Set("starting_after", args.StartingAfter)
	}
	body, status, err := stripeDo(ctx, key, http.MethodGet, "/v1/prices", q)
	if err != nil {
		return nil, fmt.Errorf("sandbox.stripe.listPrices: %w", err)
	}
	prices, _ := body["data"].([]any)
	hasMore, _ := body["has_more"].(bool)
	return map[string]any{
		"status":     "OK",
		"prices":     prices,
		"count":      len(prices),
		"hasMore":    hasMore,
		"product":    args.ProductID,
		"httpStatus": status,
	}, nil
}

func schemaSandboxStripeListPrices() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"product_id":     map[string]any{"type": "string", "description": "Optional Stripe Product ID (`prod_…`) to filter prices by."},
			"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Page size (Stripe caps at 100; default 20)."},
			"active":         map[string]any{"type": "boolean", "description": "Filter by active flag (omit → both)."},
			"starting_after": map[string]any{"type": "string", "description": "Stripe cursor — pass the last price `id` from a previous page to fetch the next page."},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.stripe.createCheckoutSession ------------------------------

type sandboxStripeCreateCheckoutSessionArgs struct {
	PriceID    string `json:"price_id"`
	SuccessURL string `json:"success_url"`
	CancelURL  string `json:"cancel_url"`
	Quantity   int    `json:"quantity,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

func sandboxStripeCreateCheckoutSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxStripeCreateCheckoutSessionArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.stripe.createCheckoutSession: invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.PriceID) == "" {
		return nil, errors.New("sandbox.stripe.createCheckoutSession: `price_id` is required")
	}
	if strings.TrimSpace(args.SuccessURL) == "" {
		return nil, errors.New("sandbox.stripe.createCheckoutSession: `success_url` is required")
	}
	if strings.TrimSpace(args.CancelURL) == "" {
		return nil, errors.New("sandbox.stripe.createCheckoutSession: `cancel_url` is required")
	}
	for _, u := range []string{args.SuccessURL, args.CancelURL} {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("sandbox.stripe.createCheckoutSession: invalid URL %q (must be absolute http(s)://...)", u)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("sandbox.stripe.createCheckoutSession: URL %q must be http or https (got %q)", u, parsed.Scheme)
		}
	}
	qty := args.Quantity
	if qty <= 0 {
		qty = 1
	}
	mode := strings.TrimSpace(args.Mode)
	if mode == "" {
		mode = "payment"
	}
	switch mode {
	case "payment", "subscription", "setup":
	default:
		return nil, fmt.Errorf("sandbox.stripe.createCheckoutSession: `mode` must be one of payment|subscription|setup (got %q)", mode)
	}
	key, err := readStripeKey(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("mode", mode)
	form.Set("success_url", args.SuccessURL)
	form.Set("cancel_url", args.CancelURL)
	form.Set("line_items[0][price]", args.PriceID)
	form.Set("line_items[0][quantity]", fmt.Sprintf("%d", qty))
	body, status, err := stripeDo(ctx, key, http.MethodPost, "/v1/checkout/sessions", form)
	if err != nil {
		return nil, fmt.Errorf("sandbox.stripe.createCheckoutSession: %w", err)
	}
	sessionID, _ := body["id"].(string)
	sessionURL, _ := body["url"].(string)
	return map[string]any{
		"status":     "Created",
		"id":         sessionID,
		"url":        sessionURL,
		"mode":       mode,
		"priceId":    args.PriceID,
		"quantity":   qty,
		"httpStatus": status,
	}, nil
}

func schemaSandboxStripeCreateCheckoutSession() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"price_id", "success_url", "cancel_url"},
		"properties": map[string]any{
			"price_id":    map[string]any{"type": "string", "description": "Stripe Price ID (`price_…`) to bill in the Checkout session."},
			"success_url": map[string]any{"type": "string", "description": "Absolute http(s) URL Stripe redirects to on a successful checkout."},
			"cancel_url":  map[string]any{"type": "string", "description": "Absolute http(s) URL Stripe redirects to on cancel."},
			"quantity":    map[string]any{"type": "integer", "minimum": 1, "description": "Line-item quantity (default 1)."},
			"mode":        map[string]any{"type": "string", "enum": []string{"payment", "subscription", "setup"}, "description": "Stripe Checkout mode (default `payment`)."},
		},
		"additionalProperties": false,
	}
}

// --- shared helpers ----------------------------------------------------

// stripeLimitOr returns the Stripe `limit` query value, clamping to
// (1..100] and falling back to def when 0 / negative.
func stripeLimitOr(limit, def int) string {
	if limit <= 0 {
		limit = def
	}
	if limit > 100 {
		limit = 100
	}
	return fmt.Sprintf("%d", limit)
}
