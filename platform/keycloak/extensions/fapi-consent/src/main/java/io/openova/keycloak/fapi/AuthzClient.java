package io.openova.keycloak.fapi;

import com.fasterxml.jackson.databind.JsonNode;
import org.apache.http.client.methods.CloseableHttpResponse;
import org.apache.http.client.methods.HttpGet;
import org.apache.http.client.methods.HttpPost;
import org.apache.http.client.methods.HttpRequestBase;
import org.apache.http.entity.ByteArrayEntity;
import org.apache.http.entity.ContentType;
import org.apache.http.util.EntityUtils;
import org.keycloak.common.util.Time;
import org.keycloak.connections.httpclient.HttpClientProvider;
import org.keycloak.crypto.SignatureProvider;
import org.keycloak.crypto.SignatureSignerContext;
import org.keycloak.jose.jws.JWSBuilder;
import org.keycloak.models.KeycloakSession;
import org.keycloak.models.RealmModel;
import org.keycloak.models.utils.KeycloakModelUtils;
import org.keycloak.representations.JsonWebToken;
import org.keycloak.services.Urls;
import org.keycloak.util.JsonSerialization;

import java.io.IOException;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * The client of the three calls of the open-banking product's internal authorisation API.
 *
 * <p>Each call carries a bearer token that the extension signs itself with the realm's active
 * PS256 key: issuer the realm, audience {@value #AUDIENCE}, subject {@value #SUBJECT}, valid for
 * {@value #TOKEN_SECONDS} seconds. An answer other than 200 becomes a {@link Failure} whose code is
 * the error the API documents for that status, or {@value #UNAVAILABLE}.
 */
public final class AuthzClient {

    /** The audience of the token the extension presents. */
    public static final String AUDIENCE = "fingate-authz";

    /** The subject and the authorised party of the token the extension presents. */
    public static final String SUBJECT = "fapi-consent";

    /** The lifetime of the token the extension presents, in seconds. */
    public static final int TOKEN_SECONDS = 60;

    /** The signing algorithm of the token the extension presents. */
    public static final String ALGORITHM = "PS256";

    /** The code of every failure the API does not document: no answer, a bad answer, no key. */
    public static final String UNAVAILABLE = "authorisation-unavailable";

    /** A call that did not succeed, with the code that names why. */
    public static final class Failure extends Exception {
        private static final long serialVersionUID = 1L;
        private final String code;

        public Failure(String code) {
            super(code);
            this.code = code;
        }

        public String code() {
            return code;
        }
    }

    /** The answer of the first call: the consent as the customer will see it. */
    public record Authorisation(String consentId, String family, String clientId, String state,
                                String expiresAt, Map<String, Object> summary) {
    }

    /** One account the customer may choose. */
    public record Account(String id, String displayName, String maskedNumber, String currency) {
    }

    /** The answer of the second call: the customer and the accounts the customer holds. */
    public record Customer(String customerId, List<Account> accounts) {
    }

    private final KeycloakSession session;
    private final String base;

    public AuthzClient(KeycloakSession session, String baseUrl) {
        this.session = session;
        this.base = baseUrl.endsWith("/") ? baseUrl.substring(0, baseUrl.length() - 1) : baseUrl;
    }

    /** {@code GET /internal/v1/authorisations/{consentId}?client_id=<id>}. */
    public Authorisation authorisation(String consentId, String clientId) throws Failure {
        String url = base + "/internal/v1/authorisations/" + ConsentRequest.pathSegment(consentId)
                + "?client_id=" + URLEncoder.encode(clientId, StandardCharsets.UTF_8);
        JsonNode n = call(new HttpGet(url), null,
                Map.of(404, "consent-not-found", 403, "client-mismatch", 409, "not-awaiting"));
        JsonNode summary = n.path("summary");
        if (!summary.isObject()) {
            throw new Failure(UNAVAILABLE);
        }
        @SuppressWarnings("unchecked")
        Map<String, Object> summaryMap = JsonSerialization.mapper.convertValue(summary, Map.class);
        return new Authorisation(text(n, "consentId"), text(n, "family"), text(n, "clientId"),
                text(n, "state"), n.path("expiresAt").asText(""), summaryMap);
    }

    /** {@code POST /internal/v1/authorisations/{consentId}/customer}. */
    public Customer customer(String consentId, String scheme, String subject) throws Failure {
        Map<String, Object> identity = new LinkedHashMap<>();
        identity.put("scheme", scheme);
        identity.put("subject", subject);
        identity.put("attributes", Map.of());
        JsonNode n = call(new HttpPost(path(consentId, "customer")), Map.of("identity", identity),
                Map.of(404, "customer-unknown", 502, "ledger-unavailable"));
        JsonNode accounts = n.path("accounts");
        if (!accounts.isArray()) {
            throw new Failure(UNAVAILABLE);
        }
        List<Account> list = new ArrayList<>();
        for (JsonNode a : accounts) {
            list.add(new Account(text(a, "id"), a.path("displayName").asText(""),
                    a.path("maskedNumber").asText(""), a.path("currency").asText("")));
        }
        return new Customer(text(n, "customerId"), List.copyOf(list));
    }

    /** {@code POST /internal/v1/authorisations/{consentId}/decision}; returns the new state. */
    public String decide(String consentId, String customerId, String decision, List<String> accounts,
                         String acr, String authTime) throws Failure {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("customerId", customerId);
        body.put("decision", decision);
        body.put("accounts", accounts);
        body.put("acr", acr);
        body.put("authTime", authTime);
        JsonNode n = call(new HttpPost(path(consentId, "decision")), body,
                Map.of(409, "not-awaiting", 422, "account-not-owned"));
        return text(n, "state");
    }

    private String path(String consentId, String leaf) {
        return base + "/internal/v1/authorisations/" + ConsentRequest.pathSegment(consentId) + "/" + leaf;
    }

    private JsonNode call(HttpRequestBase request, Object body, Map<Integer, String> documented) throws Failure {
        request.setHeader("Authorization", "Bearer " + bearer());
        request.setHeader("Accept", "application/json");
        try {
            if (body != null) {
                ((HttpPost) request).setEntity(new ByteArrayEntity(JsonSerialization.writeValueAsBytes(body),
                        ContentType.APPLICATION_JSON));
            }
            HttpClientProvider provider = session.getProvider(HttpClientProvider.class);
            try (CloseableHttpResponse response = provider.getHttpClient().execute(request)) {
                int status = response.getStatusLine().getStatusCode();
                byte[] bytes = response.getEntity() == null ? new byte[0] : EntityUtils.toByteArray(response.getEntity());
                if (status != 200) {
                    throw new Failure(documented.getOrDefault(status, UNAVAILABLE));
                }
                JsonNode n = JsonSerialization.mapper.readTree(bytes);
                if (n == null || !n.isObject()) {
                    throw new Failure(UNAVAILABLE);
                }
                return n;
            }
        } catch (IOException | RuntimeException e) {
            throw new Failure(UNAVAILABLE);
        }
    }

    private String bearer() throws Failure {
        try {
            RealmModel realm = session.getContext().getRealm();
            long now = Time.currentTime();
            JsonWebToken token = new JsonWebToken();
            token.id(KeycloakModelUtils.generateId());
            token.issuer(Urls.realmIssuer(session.getContext().getUri().getBaseUri(), realm.getName()));
            token.audience(AUDIENCE);
            token.subject(SUBJECT);
            token.issuedFor(SUBJECT);
            token.type("Bearer");
            token.iat(now);
            token.exp(now + TOKEN_SECONDS);
            SignatureSignerContext signer = session.getProvider(SignatureProvider.class, ALGORITHM).signer();
            return new JWSBuilder().type("JWT").jsonContent(token).sign(signer);
        } catch (RuntimeException e) {
            throw new Failure(UNAVAILABLE);
        }
    }

    private static String text(JsonNode n, String field) throws Failure {
        JsonNode v = n.path(field);
        if (!v.isTextual() || v.textValue().isEmpty()) {
            throw new Failure(UNAVAILABLE);
        }
        return v.textValue();
    }
}
