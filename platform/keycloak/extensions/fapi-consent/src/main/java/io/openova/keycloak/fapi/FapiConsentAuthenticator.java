package io.openova.keycloak.fapi;

import com.fasterxml.jackson.core.type.TypeReference;
import jakarta.ws.rs.core.MultivaluedMap;
import jakarta.ws.rs.core.Response;
import org.jboss.logging.Logger;
import org.keycloak.authentication.AuthenticationFlowContext;
import org.keycloak.authentication.Authenticator;
import org.keycloak.common.util.Time;
import org.keycloak.models.ClientModel;
import org.keycloak.models.KeycloakSession;
import org.keycloak.models.RealmModel;
import org.keycloak.models.UserModel;
import org.keycloak.protocol.LoginProtocol;
import org.keycloak.protocol.oidc.OIDCLoginProtocol;
import org.keycloak.services.managers.AuthenticationManager;
import org.keycloak.sessions.AuthenticationSessionModel;
import org.keycloak.util.JsonSerialization;

import java.io.IOException;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * The consent step of the browser flow.
 *
 * <p>When the authorisation request carries a consent identifier, the step reads the consent and
 * resolves the signed-in customer through the internal authorisation API, shows the consent page,
 * and posts the customer's decision. After an authorised decision it stores the identifier as the
 * client note {@value #CONSENT_NOTE}, which {@link FapiConsentClaimMapper} writes into the tokens.
 * Every other end of the step sends the error {@code access_denied} to the client, with the code
 * of the reason as its description. A request without a consent identifier passes the step
 * unchanged and its tokens carry no consent. The step holds no business rule: the API decides.
 */
public final class FapiConsentAuthenticator implements Authenticator {

    /** The client note that holds the identifier of the consent authorised in this login. */
    public static final String CONSENT_NOTE = "fapi-consent.consent-id";

    /** The name of the consent page template. */
    public static final String TEMPLATE = "fapi-consent.ftl";

    /** The description sent to the client when the customer rejects the consent. */
    public static final String REJECTED = "rejected";

    private static final Logger LOG = Logger.getLogger(FapiConsentAuthenticator.class);
    private static final String NOTE_CONSENT = "fapi-consent.pending";
    private static final String NOTE_CUSTOMER = "fapi-consent.customer";
    private static final String NOTE_VIEW = "fapi-consent.view";

    private final String authzUrl;
    private final String identityScheme;

    public FapiConsentAuthenticator(String authzUrl, String identityScheme) {
        this.authzUrl = authzUrl;
        this.identityScheme = identityScheme;
    }

    @Override
    public void authenticate(AuthenticationFlowContext context) {
        AuthenticationSessionModel auth = context.getAuthenticationSession();
        // A client session outlives one login: clear what an earlier login of this client left.
        auth.setClientNote(CONSENT_NOTE, "");
        String consentId = ConsentRequest.consentId(auth.getClientNote(OIDCLoginProtocol.CLAIMS_PARAM));
        if (consentId == null) {
            context.success();
            return;
        }
        if (authzUrl == null || identityScheme == null) {
            LOG.error("fapi-consent: the options authz-url and identity-scheme must both be set");
            deny(context, AuthzClient.UNAVAILABLE);
            return;
        }
        AuthzClient client = new AuthzClient(context.getSession(), authzUrl);
        try {
            AuthzClient.Authorisation authorisation = client.authorisation(consentId, auth.getClient().getClientId());
            AuthzClient.Customer customer = client.customer(consentId, identityScheme, context.getUser().getUsername());
            Map<String, Object> view = new LinkedHashMap<>();
            view.put("family", authorisation.family());
            view.put("expiresAt", authorisation.expiresAt());
            view.put("summary", authorisation.summary());
            List<Map<String, String>> accounts = new ArrayList<>();
            for (AuthzClient.Account a : customer.accounts()) {
                Map<String, String> m = new LinkedHashMap<>();
                m.put("id", a.id());
                m.put("displayName", a.displayName());
                m.put("maskedNumber", a.maskedNumber());
                m.put("currency", a.currency());
                accounts.add(m);
            }
            view.put("accounts", accounts);
            auth.setAuthNote(NOTE_CONSENT, consentId);
            auth.setAuthNote(NOTE_CUSTOMER, customer.customerId());
            auth.setAuthNote(NOTE_VIEW, JsonSerialization.writeValueAsString(view));
            context.challenge(page(context, view, null));
        } catch (AuthzClient.Failure f) {
            deny(context, f.code());
        } catch (IOException e) {
            deny(context, AuthzClient.UNAVAILABLE);
        }
    }

    @Override
    public void action(AuthenticationFlowContext context) {
        AuthenticationSessionModel auth = context.getAuthenticationSession();
        String consentId = auth.getAuthNote(NOTE_CONSENT);
        String customerId = auth.getAuthNote(NOTE_CUSTOMER);
        Map<String, Object> view;
        try {
            view = JsonSerialization.readValue(auth.getAuthNote(NOTE_VIEW), new TypeReference<Map<String, Object>>() { });
        } catch (IOException | RuntimeException e) {
            view = null;
        }
        if (consentId == null || customerId == null || view == null) {
            deny(context, AuthzClient.UNAVAILABLE);
            return;
        }
        MultivaluedMap<String, String> form = context.getHttpRequest().getDecodedFormParameters();
        String acr = Decision.acr("true".equals(auth.getAuthNote(AuthenticationManager.SSO_AUTH)));
        String authTime = Decision.rfc3339(authTime(auth));
        AuthzClient client = new AuthzClient(context.getSession(), authzUrl);
        if (!Decision.AUTHORISE.equals(form.getFirst("decision"))) {
            try {
                client.decide(consentId, customerId, Decision.REJECT, List.of(), acr, authTime);
            } catch (AuthzClient.Failure f) {
                LOG.warnf("fapi-consent: the rejection of a consent was not recorded: %s", f.code());
            }
            deny(context, REJECTED);
            return;
        }
        List<String> offered = new ArrayList<>();
        Object accounts = view.get("accounts");
        if (accounts instanceof List<?> list) {
            for (Object o : list) {
                if (o instanceof Map<?, ?> m && m.get("id") instanceof String id) {
                    offered.add(id);
                }
            }
        }
        List<String> chosen;
        try {
            chosen = Decision.chosen(offered, form.get("account"));
        } catch (IllegalArgumentException e) {
            context.challenge(page(context, view, "fapiConsentChooseAccount"));
            return;
        }
        try {
            String state = client.decide(consentId, customerId, Decision.AUTHORISE, chosen, acr, authTime);
            if (!"authorised".equals(state)) {
                deny(context, AuthzClient.UNAVAILABLE);
                return;
            }
        } catch (AuthzClient.Failure f) {
            deny(context, f.code());
            return;
        }
        auth.setClientNote(CONSENT_NOTE, consentId);
        auth.removeAuthNote(NOTE_CONSENT);
        auth.removeAuthNote(NOTE_CUSTOMER);
        auth.removeAuthNote(NOTE_VIEW);
        context.success();
    }

    private static long authTime(AuthenticationSessionModel auth) {
        String note = auth.getAuthNote(AuthenticationManager.AUTH_TIME);
        if (note != null) {
            try {
                return Long.parseLong(note);
            } catch (NumberFormatException e) {
                // fall through to the time of the decision
            }
        }
        return Time.currentTime();
    }

    private static Response page(AuthenticationFlowContext context, Map<String, Object> view, String error) {
        ClientModel client = context.getAuthenticationSession().getClient();
        String name = client.getName() == null || client.getName().isBlank() ? client.getClientId() : client.getName();
        var form = context.form()
                .setAttribute("fapiClient", name)
                .setAttribute("fapiFamily", view.get("family"))
                .setAttribute("fapiExpiresAt", view.get("expiresAt"))
                .setAttribute("fapiSummary", view.get("summary"))
                .setAttribute("fapiAccounts", view.get("accounts"));
        if (error != null) {
            form.setError(error);
        }
        return form.createForm(TEMPLATE);
    }

    private static void deny(AuthenticationFlowContext context, String code) {
        KeycloakSession session = context.getSession();
        RealmModel realm = context.getRealm();
        AuthenticationSessionModel auth = context.getAuthenticationSession();
        context.getEvent().detail("fapi_consent", code);
        LoginProtocol protocol = session.getProvider(LoginProtocol.class, auth.getProtocol());
        protocol.setRealm(realm)
                .setHttpHeaders(session.getContext().getRequestHeaders())
                .setUriInfo(session.getContext().getUri())
                .setEventBuilder(context.getEvent());
        Response response = protocol.sendError(auth, LoginProtocol.Error.CONSENT_DENIED, code);
        context.forceChallenge(response);
    }

    @Override
    public boolean requiresUser() {
        return true;
    }

    @Override
    public boolean configuredFor(KeycloakSession session, RealmModel realm, UserModel user) {
        return true;
    }

    @Override
    public void setRequiredActions(KeycloakSession session, RealmModel realm, UserModel user) {
        // The step needs no required action.
    }

    @Override
    public void close() {
        // Nothing to release.
    }
}
