package suite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/storefront"
)

// The onboarding journey of check BSS-G01. A new customer of the storefront
// of https://marketplace.<fqdn> signs in, redeems a voucher, orders the
// smallest package with one application, paid from credit, and reaches a
// serving application. Only step 3, the voucher, is a sovereign-admin action;
// step 7 proves that steps 4 to 6 made no call with the console session.
//
// The journey runs once per run. onboardedOrg gives every check of the billing
// area that needs a customer Organization the Organization it created, or the
// error it ended with. Its clean-up is registered with AtEnd (cleanUp): the
// owner deletes the Organization through the storefront, which ends its
// subscription; the sovereign-admin deletes it through the platform, as the
// console does (DELETE /api/v1/organizations/{id}), and revokes the voucher;
// then the suite waits until no region holds the Organization object or its
// namespace, and fails naming each one that is left.
//
// The sign-in code is mailed to the address the customer typed. The suite
// reads it from the gate lane's mailbox over IMAP (storefront.Mailbox) with
// the four values of the environment below; the address holds {tag}, which
// the journey replaces with a value of its own, so each run signs in a new
// customer. Without them it reads the Sovereign's own (sovereignMailbox), and
// without either step 1 fails before any request: there is no sign-in
// bypass. The sign-in service lets 5 code requests a minute through per
// source address; the journey makes one.

// The gate lane's mailbox, read from the environment of the run.
const (
	envMailboxIMAP    = "BSS_GATE_MAILBOX_IMAP" // host:port, implicit TLS
	envMailboxUser    = "BSS_GATE_MAILBOX_USER"
	envMailboxPhrase  = "BSS_GATE_MAILBOX_PASSWORD"
	envMailboxAddress = "BSS_GATE_MAILBOX_ADDRESS" // one address with {tag}, for example gate+{tag}@example.org
)

// The Secret of the Sovereign's own gate mailbox, written by the setup Job of
// its mail server (bp-stalwart-sovereign, kit slot 95), and its keys.
const sovMailboxNamespace, sovMailboxSecret = "platform", "stalwart-sovereign-conformance"

var sovMailboxKeys = []string{"address", "user", "password", "imap-host", "imap-service", "imap-port"}

// storefrontDefaultTLD is the parent domain the storefront preselects
// (DEFAULT_TLD, core/marketplace/src/lib/cart.ts). The tenant service puts an
// Organization whose choice it does not serve on the Sovereign's own pool.
const storefrontDefaultTLD = "omani.rest"

// onboardWaits are the time limits of the journey.
type onboardWaits struct {
	mail, ready, price, gone time.Duration // the longest waits
	every                    time.Duration // between two looks
}

var onboardDefaultWaits = onboardWaits{mail: 5 * time.Minute, ready: 30 * time.Minute,
	price: 5 * time.Minute, gone: 10 * time.Minute, every: 15 * time.Second}

// codeReader reads the sign-in code mailed to an address. storefront.Mailbox
// is one; it answers storefront.ErrNoMail while none has arrived.
type codeReader interface {
	Code(ctx context.Context, address string) (string, error)
}

// onboarding is what the journey reads besides the run: the tests fake it.
type onboarding struct {
	getenv    func(string) string
	mailbox   func(addr, serverName, user, password string) codeReader // serverName "": the host of addr
	kubectl   kubectlFunc
	forward   forwardFunc
	transport http.RoundTripper // nil: a new connection per request
	waits     onboardWaits
	tag       func() string
}

func defaultOnboarding() onboarding {
	return onboarding{
		getenv: getenvTrimmed,
		mailbox: func(addr, serverName, user, password string) codeReader {
			return storefront.Mailbox{Addr: addr, ServerName: serverName, User: user, Password: password}
		},
		kubectl: kube.Region.Run,
		forward: portForward,
		waits:   onboardDefaultWaits,
		tag: func() string {
			s := strconv.FormatInt(time.Now().UnixNano(), 36)
			return s[len(s)-8:]
		},
	}
}

// OnboardResult is what the journey created. Token is the customer's
// storefront token: a secret that never goes into evidence.
type OnboardResult struct {
	Email, Token                       string
	OrgID, OrgSlug, ParentDomain       string
	Region, AppHost                    string
	PlanID, PackageSKU, AppID, AppSlug string
	OrderID                            string
	AmountBaisa                        int64
	PriceSource                        string
}

type onboardMemo struct {
	once sync.Once
	res  OnboardResult
	err  error
}

var (
	onboardMu   sync.Mutex
	onboardRuns = map[*CheckContext]*onboardMemo{}
)

// onboardedOrg runs the journey for the first caller of a run, which records
// its steps with rec; every later caller gets the stored result or the stored
// error and records nothing.
func onboardedOrg(ctx context.Context, cc *CheckContext, rec StepRecorder) (OnboardResult, error) {
	res, _, err := defaultOnboarding().onboarded(ctx, cc, rec)
	return res, err
}

// onboarded is onboardedOrg; ran says whether this call ran the journey.
func (o onboarding) onboarded(ctx context.Context, cc *CheckContext, rec StepRecorder) (OnboardResult, bool, error) {
	onboardMu.Lock()
	m := onboardRuns[cc]
	if m == nil {
		m = &onboardMemo{}
		onboardRuns[cc] = m
	}
	onboardMu.Unlock()
	ran := false
	m.once.Do(func() {
		ran = true
		m.res, m.err = o.journey(ctx, cc, rec)
	})
	return m.res, ran, m.err
}

func getenvTrimmed(k string) string {
	return strings.TrimSpace(os.Getenv(k))
}

// httpClient is the client of the storefront, the application host and the
// price document: a new connection per request unless a test routes them.
func (o onboarding) httpClient() *http.Client {
	t := o.transport
	if t == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.DisableKeepAlives = true
		t = tr
	}
	return &http.Client{Transport: t, Timeout: priceListTimeout}
}

// countingTransport counts the requests sent with the console session.
type countingTransport struct {
	base http.RoundTripper
	mu   sync.Mutex
	n    int
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	base := c.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func (c *countingTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// onboardState is what the clean-up needs; it is filled as the journey goes.
type onboardState struct {
	sf      *storefront.Client
	admin   *api.Client
	voucher string
	orgID   string
	slug    string
}

func (o onboarding) journey(ctx context.Context, cc *CheckContext, rec StepRecorder) (OnboardResult, error) {
	var res OnboardResult
	failed := func(n int) error { return fmt.Errorf("the onboarding journey failed at step %s.%d", onboardID, n) }
	fqdn := sovereignFQDN(cc.Prior)
	var mail codeReader
	var tag string

	if !checkStep(rec, onboardID, 1, "the run holds a mailbox for the new customer: the gate lane's or the Sovereign's own", func(ev Evidence) error {
		switch {
		case fqdn == "":
			return errors.New("the run did not learn the name of the Sovereign: step D0.1 recorded no sovereignFQDN")
		case cc.API == nil:
			return errors.New("the run has no console session: the voucher of step 3 is issued by a sovereign-admin")
		}
		vals, missing := map[string]string{}, []string{}
		for _, k := range []string{envMailboxIMAP, envMailboxUser, envMailboxPhrase, envMailboxAddress} {
			if vals[k] = strings.TrimSpace(o.getenv(k)); vals[k] == "" {
				missing = append(missing, k)
			}
		}
		var template string
		switch {
		case len(missing) == 0:
			template = vals[envMailboxAddress]
			if strings.Count(template, "{tag}") != 1 || strings.Count(template, "@") != 1 {
				return fmt.Errorf("%s must be one address that holds {tag} once, for example gate+{tag}@example.org", envMailboxAddress)
			}
			mail = o.mailbox(vals[envMailboxIMAP], "", vals[envMailboxUser], vals[envMailboxPhrase])
			ev["mailbox"], ev["imap"] = "gate lane", vals[envMailboxIMAP]
		case len(missing) < 4:
			return fmt.Errorf("%s not set: set all four BSS_GATE_MAILBOX_* for the gate lane's mailbox, or none for the Sovereign's own", strings.Join(missing, ", "))
		default:
			m, t, err := o.sovereignMailbox(ctx, cc, ev)
			if err != nil {
				return err
			}
			mail, template = m, t
		}
		tag = strings.ToLower(o.tag())
		res.Email, res.OrgSlug = strings.Replace(template, "{tag}", tag, 1), "g01-"+tag
		ev["address"], ev["org_slug"] = res.Email, res.OrgSlug
		return nil
	}) {
		return res, failed(1)
	}

	client := o.httpClient()
	sf := storefront.New(fqdn, client)
	var plan storefront.Plan
	var app storefront.App
	if !checkStep(rec, onboardID, 2, "the storefront lists a priced plan and an application of class application", func(ev Evidence) error {
		plans, err := sf.Plans(ctx)
		if err != nil {
			return err
		}
		for _, p := range plans {
			if p.ProductSlug == "" && p.ID != "" && p.PriceBaisa > 0 &&
				(plan.ID == "" || p.PriceBaisa < plan.PriceBaisa || (p.PriceBaisa == plan.PriceBaisa && p.Slug < plan.Slug)) {
				plan = p
			}
		}
		if plan.ID == "" {
			return fmt.Errorf("GET %s/catalog/plans?product=generic lists no plan without a product and with a price", sf.Base)
		}
		apps, err := sf.Apps(ctx)
		if err != nil {
			return err
		}
		var picks []storefront.App
		for _, a := range apps {
			if !a.System && a.Deployable && a.Kind != "service" && a.ID != "" && a.Slug != "" {
				picks = append(picks, a)
			}
		}
		if len(picks) == 0 {
			return fmt.Errorf("GET %s/catalog/apps?published=true lists no deployable application of class application among %d", sf.Base, len(apps))
		}
		// The one with the fewest backing services is the quickest to serve.
		sort.Slice(picks, func(i, j int) bool {
			if len(picks[i].Dependencies) != len(picks[j].Dependencies) {
				return len(picks[i].Dependencies) < len(picks[j].Dependencies)
			}
			return picks[i].Slug < picks[j].Slug
		})
		app = picks[0]
		res.PlanID, res.PackageSKU, res.AppID, res.AppSlug = plan.ID, "plan."+strings.ToLower(plan.Slug), app.ID, app.Slug
		ev["plan_id"], ev["package_sku"], ev["plan_price_baisa"] = plan.ID, res.PackageSKU, plan.PriceBaisa
		ev["app_id"], ev["app_slug"], ev["apps_listed"], ev["apps_eligible"] = app.ID, app.Slug, len(apps), len(picks)
		return nil
	}) {
		return res, failed(2)
	}

	// The console calls of the journey go through a copy of the run's client
	// that counts them: step 7 reads the count.
	counter, admin := &countingTransport{}, *cc.API
	admin.HTTP = &http.Client{Transport: counter, Timeout: 60 * time.Second}
	if cc.API.HTTP != nil {
		counter.base, admin.HTTP.Timeout = cc.API.HTTP.Transport, cc.API.HTTP.Timeout
	}
	st := &onboardState{sf: sf, admin: &admin, slug: res.OrgSlug}
	credit := int((2*plan.PriceBaisa + 999) / 1000)
	if !checkStep(rec, onboardID, 3, "a sovereign-admin issues a voucher worth twice the price of the smallest package (POST /api/v1/org/billing/vouchers/issue)", func(ev Evidence) error {
		body := map[string]any{"credit_omr": credit, "description": "acceptance check " + onboardID + " " + tag,
			"active": true, "max_redemptions": 1, "plan_tier": strings.ToLower(plan.Slug)}
		ev["credit_omr"] = credit
		r, err := admin.Do(ctx, http.MethodPost, "/api/v1/org/billing/vouchers/issue", body)
		if err != nil {
			return err
		}
		ev["http"] = r.Status
		var v struct {
			Code      string `json:"code"`
			CreditOMR int    `json:"credit_omr"`
		}
		if r.Status != http.StatusOK && r.Status != http.StatusCreated {
			return fmt.Errorf("HTTP %d: %s", r.Status, api.Snippet(r.Body))
		}
		if err := r.JSON(&v); err != nil {
			return err
		}
		if v.Code == "" {
			return errors.New("the answer names no voucher code")
		}
		st.voucher = v.Code
		AtEnd(onboardID, func(ctx context.Context, rec StepRecorder) { o.cleanUp(ctx, cc, rec, st) })
		ev["voucher_last4"] = v.Code[max(0, len(v.Code)-4):]
		return nil
	}) {
		return res, failed(3)
	}

	consoleBefore := counter.count()
	customerFrom := len(sf.Calls)
	if !checkStep(rec, onboardID, 4, "a new customer signs in with the code mailed to the gate lane's mailbox (POST /api/auth/magic-link, POST /api/auth/verify)", func(ev Evidence) error {
		if err := sf.MagicLink(ctx, res.Email); err != nil {
			return err
		}
		start := time.Now()
		code := ""
		_, err := poll(ctx, o.waits.mail, o.waits.every, func() (bool, string, error) {
			c, err := mail.Code(ctx, res.Email)
			switch {
			case errors.Is(err, storefront.ErrNoMail):
				return false, "no sign-in mail yet", nil
			case err != nil:
				return false, err.Error(), nil
			}
			code = c
			return true, "", nil
		})
		ev["mail_ms"] = time.Since(start).Milliseconds()
		if err != nil {
			return fmt.Errorf("the sign-in code to %s: %w", res.Email, err)
		}
		if _, err := sf.Verify(ctx, res.Email, code); err != nil {
			return err
		}
		me, err := sf.Me(ctx)
		if err != nil {
			return err
		}
		ev["role"] = me.Role
		switch {
		case !strings.EqualFold(me.Email, res.Email):
			return fmt.Errorf("GET /api/auth/me answers %q, not the address that signed in", me.Email)
		case me.Role == "superadmin" || me.Role == "sovereign-admin":
			return fmt.Errorf("the new customer has the role %q", me.Role)
		}
		res.Token = sf.Token
		return nil
	}) {
		return res, failed(4)
	}

	if !checkStep(rec, onboardID, 5, "the customer redeems the voucher and orders the smallest package with the application, paid from credit (POST /api/tenant/orgs, /api/billing/checkout, /api/provisioning/start)", func(ev Evidence) error {
		pv, err := sf.RedeemPreview(ctx, st.voucher)
		if err != nil {
			return err
		}
		ev["preview_credit_omr"] = pv.CreditOMR
		if pv.CreditOMR != credit {
			return fmt.Errorf("the voucher preview shows a credit of %d OMR, issued %d", pv.CreditOMR, credit)
		}
		org, err := sf.CreateOrg(ctx, storefront.OrgRequest{Slug: res.OrgSlug, Name: "Acceptance " + tag, PlanID: plan.ID,
			Apps: []string{app.ID}, Addons: []string{}, PackageSKU: res.PackageSKU, ParentDomain: storefrontDefaultTLD,
			AppConfigs: map[string]map[string]any{}, DeferLaunch: true})
		if err != nil {
			return err
		}
		if org.ID == "" {
			return errors.New("POST /api/tenant/orgs answered no id")
		}
		st.orgID = org.ID
		if org.Slug != "" {
			res.OrgSlug, st.slug = org.Slug, org.Slug
		}
		res.OrgID, res.ParentDomain = org.ID, org.ParentDomain
		ev["org_id"], ev["org_slug"], ev["parent_domain"] = org.ID, res.OrgSlug, org.ParentDomain
		co, err := sf.Checkout(ctx, storefront.CheckoutRequest{PlanID: plan.ID, Apps: []string{app.ID}, Addons: []string{},
			PackageSKU: res.PackageSKU, Topology: "single-region", OverageMode: "capped", TenantID: org.ID, PromoCode: st.voucher})
		if err != nil {
			return err
		}
		res.OrderID, res.AmountBaisa, res.PriceSource = co.OrderID, co.AmountBaisa, co.PriceSource
		ev["order_id"], ev["order_amount_baisa"], ev["price_source"], ev["paid_by_credit"] = co.OrderID, co.AmountBaisa, co.PriceSource, co.PaidByCredit
		switch {
		case co.SessionURL != "":
			return errors.New("the checkout answered a card payment session: the voucher did not cover the order")
		case !co.PaidByCredit || co.OrderID == "":
			return fmt.Errorf("the checkout is not paid from credit (paid_by_credit=%t, order_id=%q)", co.PaidByCredit, co.OrderID)
		}
		return sf.StartProvisioning(ctx, storefront.ProvisionRequest{TenantID: org.ID, OrderID: co.OrderID, PlanID: plan.ID,
			Apps: []string{app.ID}, Subdomain: res.OrgSlug})
	}) {
		return res, failed(5)
	}

	if !checkStep(rec, onboardID, 6, "the Organization is Ready, its plan-quota exists and the application's host answers HTTP 200, within "+o.waits.ready.String(), func(ev Evidence) error {
		start := time.Now()
		last, err := poll(ctx, o.waits.ready, o.waits.every, func() (bool, string, error) {
			return o.serving(ctx, cc, client, &res)
		})
		ev["ready_ms"], ev["last"] = time.Since(start).Milliseconds(), last
		ev["region"], ev["app_host"] = res.Region, res.AppHost
		return err
	}) {
		return res, failed(6)
	}

	if !checkStep(rec, onboardID, 7, "steps 4 to 6 made no call with the console session of the sovereign-admin", func(ev Evidence) error {
		calls := []string{}
		for _, c := range sf.Calls[customerFrom:] {
			calls = append(calls, fmt.Sprintf("%s %s %d %s", c.Method, c.Path, c.Status, c.Credential))
		}
		console := counter.count() - consoleBefore
		ev["storefront_calls"], ev["console_calls"] = calls, console
		switch {
		case console != 0:
			return fmt.Errorf("%d call(s) with the console session during steps 4 to 6", console)
		case res.Token == "":
			return errors.New("the customer holds no storefront token")
		case res.Token == cc.API.Session || res.Token == cc.API.SessionValue():
			return errors.New("the customer's storefront token is the console session of the sovereign-admin")
		}
		return nil
	}) {
		return res, failed(7)
	}
	return res, nil
}

// sovereignMailbox reads the Secret of the gate mailbox from the first region
// that holds it. It returns a reader that reaches the IMAP port through that
// region's API server (a port forward) with the certificate checked against the
// server's name, and the template <user>+{tag}@<fqdn>. No password goes into ev.
func (o onboarding) sovereignMailbox(ctx context.Context, cc *CheckContext, ev Evidence) (codeReader, string, error) {
	where := sovMailboxNamespace + "/" + sovMailboxSecret
	ev["mailbox"], ev["secret"] = "sovereign", where
	looked := []string{}
	for _, region := range cc.Regions {
		out, err := o.kubectl(region, ctx, nil, "get", "secret", sovMailboxSecret, "-n", sovMailboxNamespace, "--ignore-not-found", "-o", "json")
		if err != nil {
			return nil, "", fmt.Errorf("read the Secret %s: %w", where, err)
		}
		if strings.TrimSpace(string(out)) == "" {
			looked = append(looked, region.ID)
			continue
		}
		var s kube.Object
		if err := json.Unmarshal(out, &s); err != nil {
			return nil, "", fmt.Errorf("[%s] the Secret %s is not a Secret object", region.ID, where)
		}
		val, missing := map[string]string{}, []string{}
		for _, k := range sovMailboxKeys {
			b, err := base64.StdEncoding.DecodeString(s.Str("data", k))
			if val[k] = strings.TrimSpace(string(b)); err != nil || val[k] == "" {
				missing = append(missing, k)
			}
		}
		port, perr := strconv.Atoi(val["imap-port"])
		local, domain, ok := strings.Cut(val["address"], "@")
		switch {
		case len(missing) > 0:
			return nil, "", fmt.Errorf("[%s] the Secret %s holds no %s", region.ID, where, strings.Join(missing, ", "))
		case perr != nil || port < 1 || port > 65535:
			return nil, "", fmt.Errorf("[%s] the key imap-port of the Secret %s is not a port", region.ID, where)
		case !ok || local == "" || domain == "" || strings.ContainsAny(local+domain, "@+{}"):
			return nil, "", fmt.Errorf("[%s] the key address of the Secret %s is not one plain address", region.ID, where)
		}
		ev["mailbox_region"] = region.ID
		ev["imap"] = fmt.Sprintf("%s/service/%s:%d through the API server of region %s, certificate of %s",
			sovMailboxNamespace, val["imap-service"], port, region.ID, val["imap-host"])
		return forwardedMailbox{o: o, region: region, service: val["imap-service"], port: port,
			host: val["imap-host"], user: val["user"], password: val["password"]}, local + "+{tag}@" + domain, nil
	}
	return nil, "", fmt.Errorf("no region (%s) holds the Secret %s, which the Sovereign's mail server writes once the gate's mailbox exists, and BSS_GATE_MAILBOX_* are not set: the run has no mailbox to read the sign-in code from, and the journey has no sign-in bypass",
		strings.Join(looked, ", "), where)
}

// forwardedMailbox is the Sovereign's own mailbox: each read starts a port
// forward to the server's IMAP port, reads, and ends the forward.
type forwardedMailbox struct {
	o                    onboarding
	region               kube.Region
	service              string
	port                 int
	host, user, password string
}

func (m forwardedMailbox) Code(ctx context.Context, address string) (string, error) {
	addr, stop, err := m.o.forward(ctx, m.region, sovMailboxNamespace, m.service, m.port)
	if err != nil {
		return "", err
	}
	defer stop()
	return m.o.mailbox(addr, m.host, m.user, m.password).Code(ctx, address)
}

// serving looks once, in every region, for the Organization of res, its
// plan-quota and the HTTPRoute of the application, and asks the host of that
// route. It sets res.Region and res.AppHost when all are there.
func (o onboarding) serving(ctx context.Context, cc *CheckContext, client *http.Client, res *OnboardResult) (bool, string, error) {
	seen := []string{}
	for _, region := range cc.Regions {
		org, err := o.organization(ctx, region, res.OrgSlug)
		switch {
		case err != nil:
			seen = append(seen, region.ID+": "+err.Error())
			continue
		case org == nil:
			seen = append(seen, region.ID+": no Organization "+res.OrgSlug)
			continue
		}
		if status, reason, _, _ := org.Condition("Ready"); status != "True" {
			seen = append(seen, fmt.Sprintf("%s: Organization Ready=%q %s", region.ID, status, reason))
			continue
		}
		out, err := o.kubectl(region, ctx, nil, "get", "resourcequota", "plan-quota", "-n", res.OrgSlug, "--ignore-not-found", "-o", "name")
		if err != nil || strings.TrimSpace(string(out)) == "" {
			seen = append(seen, region.ID+": no ResourceQuota plan-quota in "+res.OrgSlug)
			continue
		}
		host, err := o.appHost(ctx, region, res.AppSlug+"."+res.OrgSlug+".")
		if err != nil || host == "" {
			seen = append(seen, region.ID+": no HTTPRoute with a host "+res.AppSlug+"."+res.OrgSlug+".*")
			continue
		}
		status, err := hostStatus(ctx, client, "https://"+host+"/")
		if err != nil || status != http.StatusOK {
			seen = append(seen, fmt.Sprintf("%s: https://%s/ answered %d %v", region.ID, host, status, err))
			continue
		}
		res.Region, res.AppHost = region.ID, host
		return true, region.ID + ": ready and serving", nil
	}
	return false, strings.Join(seen, "; "), nil
}

// organization returns the Organization whose slug is slug, or nil.
func (o onboarding) organization(ctx context.Context, region kube.Region, slug string) (kube.Object, error) {
	out, err := o.kubectl(region, ctx, nil, "get", "organizations.orgs.openova.io", "-o", "json")
	if err != nil {
		return nil, err
	}
	var l kube.List
	if err := json.Unmarshal(out, &l); err != nil {
		return nil, fmt.Errorf("decode the Organizations: %w", err)
	}
	for _, it := range l.Items {
		if it.Str("spec", "slug") == slug || it.Name() == slug {
			return it, nil
		}
	}
	return nil, nil
}

// appHost returns the first host of an HTTPRoute that starts with prefix.
func (o onboarding) appHost(ctx context.Context, region kube.Region, prefix string) (string, error) {
	out, err := o.kubectl(region, ctx, nil, "get", "httproutes.gateway.networking.k8s.io", "-A", "-o", "json")
	if err != nil {
		return "", err
	}
	var l kube.List
	if err := json.Unmarshal(out, &l); err != nil {
		return "", err
	}
	hosts := []string{}
	for _, it := range l.Items {
		hs, _ := it.Get("spec", "hostnames").([]any)
		for _, h := range hs {
			if s, _ := h.(string); strings.HasPrefix(s, prefix) {
				hosts = append(hosts, s)
			}
		}
	}
	sort.Strings(hosts)
	if len(hosts) == 0 {
		return "", nil
	}
	return hosts[0], nil
}

// hostStatus is the status of GET u, after redirects.
func hostStatus(ctx context.Context, client *http.Client, u string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = res.Body.Close()
	return res.StatusCode, nil
}

// cleanUp is the end of the journey. The owner deletes the Organization
// through the storefront (its subscription ends), and the sovereign-admin
// deletes it through the platform (DELETE /api/v1/organizations/{id}, the id
// the platform lists it under: the tenant id of the storefront) and revokes
// the voucher. A delete that answers 404 and a voucher that was redeemed or is
// gone (404) are fine: what decides is what is left. The suite then waits, up
// to waits.gone, until no region holds the Organization object or its
// namespace; a region that does not serve the kind Organization holds no such
// object. When something is left the step fails naming each one: kind, name
// and region, and what each delete answered.
func (o onboarding) cleanUp(ctx context.Context, cc *CheckContext, rec StepRecorder, st *onboardState) {
	checkStep(rec, onboardID, 9, "the owner deletes the Organization of the journey (DELETE /api/tenant/orgs/{id}), the platform deletes it (DELETE /api/v1/organizations/{id}), the voucher is revoked, and no region holds the Organization or its namespace after "+o.waits.gone.String(), func(ev Evidence) error {
		var errs, answers []string
		if st.orgID != "" {
			ev["org_id"], ev["org_slug"] = st.orgID, st.slug
			var nf *storefront.Error
			switch err := st.sf.DeleteOrg(ctx, st.orgID); {
			case err == nil:
				answers = append(answers, "the storefront's delete answered 2xx")
			case errors.As(err, &nf) && nf.Status == http.StatusNotFound:
				answers = append(answers, "the storefront's delete answered 404")
			default:
				errs = append(errs, "the owner's delete: "+err.Error())
			}
			path := "/api/v1/organizations/" + url.PathEscape(st.orgID)
			switch r, err := st.admin.Do(ctx, http.MethodDelete, path, nil); {
			case err != nil:
				errs = append(errs, "the platform's delete: "+err.Error())
			case r.Status == http.StatusNotFound || (r.Status >= 200 && r.Status <= 299):
				ev["platform_delete_http"] = r.Status
				answers = append(answers, fmt.Sprintf("DELETE %s answered %d", path, r.Status))
			default:
				ev["platform_delete_http"] = r.Status
				errs = append(errs, fmt.Sprintf("the platform's delete: DELETE %s: HTTP %d %s", path, r.Status, api.Snippet(r.Body)))
			}
		}
		if st.voucher != "" {
			r, err := st.admin.Do(ctx, http.MethodDelete, "/api/v1/org/billing/vouchers/revoke/"+url.PathEscape(st.voucher), nil)
			switch {
			case err != nil:
				errs = append(errs, "revoke the voucher: "+err.Error())
			case r.Status != http.StatusNotFound && (r.Status < 200 || r.Status > 299):
				errs = append(errs, fmt.Sprintf("revoke the voucher: HTTP %d %s", r.Status, api.Snippet(r.Body)))
			default:
				ev["revoke_http"] = r.Status
			}
		}
		if st.orgID != "" {
			left, err := o.leftovers(ctx, cc, st.slug)
			ev["left"] = left
			if err != nil {
				errs = append(errs, fmt.Sprintf("the Organization %s is not gone after %s: left %s (%s)", st.slug, o.waits.gone, strings.Join(left, ", "), strings.Join(answers, "; ")))
			}
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "; "))
		}
		return nil
	})
}

// leftovers waits up to waits.gone until no region holds the Organization
// object or the namespace of slug. It returns what the last look found, each
// as "<kind> <name> in <region>", or what could not be read, and an error when
// that is not nothing.
func (o onboarding) leftovers(ctx context.Context, cc *CheckContext, slug string) ([]string, error) {
	var left []string
	_, err := poll(ctx, o.waits.gone, o.waits.every, func() (bool, string, error) {
		left = []string{}
		for _, region := range cc.Regions {
			org, err := o.organization(ctx, region, slug)
			switch {
			case err != nil && strings.Contains(err.Error(), notServed):
				// The region serves no kind Organization: it holds none.
			case err != nil:
				left = append(left, "Organization "+slug+" in "+region.ID+" (not read: "+err.Error()+")")
			case org != nil:
				left = append(left, "Organization "+slug+" in "+region.ID)
			}
			out, err := o.kubectl(region, ctx, nil, "get", "namespace", slug, "--ignore-not-found", "-o", "name")
			switch {
			case err != nil:
				left = append(left, "Namespace "+slug+" in "+region.ID+" (not read: "+err.Error()+")")
			case strings.TrimSpace(string(out)) != "":
				left = append(left, "Namespace "+slug+" in "+region.ID)
			}
		}
		return len(left) == 0, strings.Join(left, ", "), nil
	})
	return left, err
}
