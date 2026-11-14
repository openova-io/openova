package api

// The three account-access consent operations of the account-information document.
//
// POST creates a consent in the state awaiting for the calling client. GET returns it with
// the profile's wire code of its state. DELETE revokes it by the third party; only an
// authorised consent can be revoked, as the consent package's table says. A consent that does
// not exist, or that belongs to another client, is answered with 400, as the standard's
// profile asks for a resource identifier that does not exist.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/consent"
	"github.com/openova-io/openova/products/fingate/internal/gen/accountinfo"
	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/token"
)

// accountInfo implements the strict server of the account-information document. An operation
// it does not implement is listed in unimplemented.txt and never reaches it.
type accountInfo struct {
	accountinfo.StrictServerInterface
	s *Server
}

func interaction(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func badConsent(iid, format string, a ...any) accountinfo.N400ErrorJSONResponse {
	return accountinfo.N400ErrorJSONResponse{Body: errorBody(fmt.Sprintf(format, a...)),
		Headers: accountinfo.N400ErrorResponseHeaders{XFapiInteractionId: iid}}
}

// CreateAccountAccessConsents implements POST /account-access-consents.
func (a *accountInfo) CreateAccountAccessConsents(ctx context.Context, req accountinfo.CreateAccountAccessConsentsRequestObject) (accountinfo.CreateAccountAccessConsentsResponseObject, error) {
	iid := interaction(req.Params.XFapiInteractionId)
	body := req.JSONBody
	if body == nil {
		body = req.ApplicationJSONCharsetUTF8Body
	}
	if body == nil {
		return accountinfo.CreateAccountAccessConsents415Response{Headers: accountinfo.N415ErrorResponseHeaders{XFapiInteractionId: iid}}, nil
	}
	now := a.s.cfg.Now()
	if msg := checkConsentRequest(body, now); msg != "" {
		return accountinfo.CreateAccountAccessConsents400JSONResponse{N400ErrorJSONResponse: badConsent(iid, "%s", msg)}, nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	c := store.Consent{ID: "aac-" + a.s.cfg.NewID(), Family: consent.AccountAccess, ClientID: claimsOf(ctx).ClientID, Request: raw}
	if body.Data.ExpirationDateTime != nil {
		c.ExpiresAt = *body.Data.ExpirationDateTime
	}
	c, err = a.s.cfg.Store.Create(ctx, c)
	if err != nil {
		return nil, err
	}
	out, err := a.view(c)
	if err != nil {
		return nil, err
	}
	return accountinfo.CreateAccountAccessConsents201JSONResponse{N201AccountAccessConsentsCreatedJSONResponse: accountinfo.N201AccountAccessConsentsCreatedJSONResponse{
		Body: out, Headers: accountinfo.N201AccountAccessConsentsCreatedResponseHeaders{XFapiInteractionId: iid}}}, nil
}

// GetAccountAccessConsentsConsentId implements GET /account-access-consents/{ConsentId}.
func (a *accountInfo) GetAccountAccessConsentsConsentId(ctx context.Context, req accountinfo.GetAccountAccessConsentsConsentIdRequestObject) (accountinfo.GetAccountAccessConsentsConsentIdResponseObject, error) {
	iid := interaction(req.Params.XFapiInteractionId)
	c, ok, err := a.own(ctx, req.ConsentId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return accountinfo.GetAccountAccessConsentsConsentId400JSONResponse{N400ErrorJSONResponse: badConsent(iid, "consent %q does not exist", req.ConsentId)}, nil
	}
	out, err := a.view(c)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountAccessConsentsConsentId200JSONResponse{N200AccountAccessConsentsConsentIdReadJSONResponse: accountinfo.N200AccountAccessConsentsConsentIdReadJSONResponse{
		Body: out, Headers: accountinfo.N200AccountAccessConsentsConsentIdReadResponseHeaders{XFapiInteractionId: iid}}}, nil
}

// DeleteAccountAccessConsentsConsentId implements DELETE /account-access-consents/{ConsentId}.
func (a *accountInfo) DeleteAccountAccessConsentsConsentId(ctx context.Context, req accountinfo.DeleteAccountAccessConsentsConsentIdRequestObject) (accountinfo.DeleteAccountAccessConsentsConsentIdResponseObject, error) {
	iid := interaction(req.Params.XFapiInteractionId)
	c, ok, err := a.own(ctx, req.ConsentId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return accountinfo.DeleteAccountAccessConsentsConsentId400JSONResponse{N400ErrorJSONResponse: badConsent(iid, "consent %q does not exist", req.ConsentId)}, nil
	}
	if _, err := a.s.cfg.Store.Apply(ctx, c.ID, consent.Revoke, consent.ThirdParty, "deleted by the third party"); err != nil {
		if errors.Is(err, consent.ErrTransition) {
			return accountinfo.DeleteAccountAccessConsentsConsentId400JSONResponse{N400ErrorJSONResponse: badConsent(iid,
				"consent %q is %s; only an authorised consent can be deleted", c.ID, c.State)}, nil
		}
		return nil, err
	}
	return accountinfo.DeleteAccountAccessConsentsConsentId204Response{Headers: accountinfo.N204AccountAccessConsentsConsentIdDeletedResponseHeaders{XFapiInteractionId: iid}}, nil
}

// own returns the account-access consent id when it belongs to the calling client.
func (a *accountInfo) own(ctx context.Context, id string) (store.Consent, bool, error) {
	c, ok, err := a.s.cfg.Store.Get(ctx, id)
	if err != nil || !ok {
		return store.Consent{}, false, err
	}
	if c.Family != consent.AccountAccess || c.ClientID != claimsOf(ctx).ClientID {
		return store.Consent{}, false, nil
	}
	return c, true, nil
}

// checkConsentRequest returns why a consent request is refused, or "".
func checkConsentRequest(b *accountinfo.OBReadConsent1, now time.Time) string {
	if b.Risk == nil {
		return "Risk is missing"
	}
	if len(b.Data.Permissions) == 0 {
		return "Data.Permissions is empty"
	}
	for i, p := range b.Data.Permissions {
		if !p.Valid() {
			return fmt.Sprintf("Data.Permissions[%d] %q is not a permission", i, p)
		}
		if slices.Contains(b.Data.Permissions[:i], p) {
			return fmt.Sprintf("Data.Permissions[%d] %q is repeated", i, p)
		}
	}
	if e := b.Data.ExpirationDateTime; e != nil && !e.After(now) {
		return "Data.ExpirationDateTime is not in the future"
	}
	if f, t := b.Data.TransactionFromDateTime, b.Data.TransactionToDateTime; f != nil && t != nil && f.After(*t) {
		return "Data.TransactionFromDateTime is after Data.TransactionToDateTime"
	}
	return ""
}

// view is the consent as the documents answer it.
func (a *accountInfo) view(c store.Consent) (accountinfo.OBReadConsentResponse1, error) {
	var req accountinfo.OBReadConsent1
	if err := json.Unmarshal(c.Request, &req); err != nil {
		return accountinfo.OBReadConsentResponse1{}, fmt.Errorf("api: stored consent %s: %w", c.ID, err)
	}
	code, err := consent.Wire(*a.s.cfg.Profile, c.Family, c.State)
	if err != nil {
		return accountinfo.OBReadConsentResponse1{}, err
	}
	var out accountinfo.OBReadConsentResponse1
	out.Data.ConsentId = c.ID
	out.Data.CreationDateTime = c.CreatedAt
	out.Data.Status = accountinfo.OBReadConsentResponse1DataStatus(code)
	out.Data.StatusUpdateDateTime = c.StateAt
	out.Data.ExpirationDateTime = req.Data.ExpirationDateTime
	out.Data.TransactionFromDateTime = req.Data.TransactionFromDateTime
	out.Data.TransactionToDateTime = req.Data.TransactionToDateTime
	for _, p := range req.Data.Permissions {
		out.Data.Permissions = append(out.Data.Permissions, accountinfo.OBReadConsentResponse1DataPermissions(p))
	}
	out.Risk = req.Risk
	host := ""
	if d := a.s.cfg.Definitions.Current(); d != nil {
		host = d.Host
	}
	out.Links = &accountinfo.Links{Self: "https://" + host + a.s.bases["account-info"] + "/account-access-consents/" + c.ID}
	return out, nil
}

// Consents answers the consent lookup of rule T9 from a store.
type Consents struct{ Store store.Store }

// Consent implements token.Consents.
func (c Consents) Consent(ctx context.Context, id string) (token.Consent, bool, error) {
	row, ok, err := c.Store.Get(ctx, id)
	if err != nil || !ok {
		return token.Consent{}, false, err
	}
	out := token.Consent{ClientID: row.ClientID, Family: string(row.Family), State: string(row.State),
		ExpiresAt: row.ExpiresAt, Accounts: slices.Clone(row.Accounts)}
	if row.Family == consent.AccountAccess {
		var req accountinfo.OBReadConsent1
		if err := json.Unmarshal(row.Request, &req); err != nil {
			return token.Consent{}, false, fmt.Errorf("api: stored consent %s: %w", row.ID, err)
		}
		for _, p := range req.Data.Permissions {
			out.Permissions = append(out.Permissions, string(p))
		}
	}
	return out, true, nil
}
