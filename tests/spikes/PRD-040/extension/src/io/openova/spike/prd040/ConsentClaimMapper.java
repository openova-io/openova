package io.openova.spike.prd040;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import org.jboss.logging.Logger;
import org.keycloak.models.AuthenticatedClientSessionModel;
import org.keycloak.models.ClientSessionContext;
import org.keycloak.models.KeycloakSession;
import org.keycloak.models.ProtocolMapperModel;
import org.keycloak.models.UserSessionModel;
import org.keycloak.protocol.oidc.OIDCLoginProtocol;
import org.keycloak.protocol.oidc.mappers.AbstractOIDCProtocolMapper;
import org.keycloak.protocol.oidc.mappers.OIDCAccessTokenMapper;
import org.keycloak.protocol.oidc.mappers.OIDCAttributeMapperHelper;
import org.keycloak.protocol.oidc.mappers.OIDCIDTokenMapper;
import org.keycloak.provider.ProviderConfigProperty;
import org.keycloak.representations.ClaimsRepresentation;
import org.keycloak.representations.IDToken;
import org.keycloak.util.JsonSerialization;

/**
 * Lab-test stub of a consent extension (spike PRD-040, never shipped).
 *
 * Carrier "intent-claim": the client creates a consent at the resource server,
 * then sends its identifier as an essential ID-token claim inside the signed
 * request object (by value or pushed). Keycloak keeps the claims request as a
 * client-session note. This mapper reads it, runs a stub consent step (one HTTP
 * call to the resource server, once per consent and client session) and writes
 * the identifier into the access token and the ID token. If the consent step
 * fails, the claim is not issued, so the resource server refuses the call.
 */
public class ConsentClaimMapper extends AbstractOIDCProtocolMapper
        implements OIDCAccessTokenMapper, OIDCIDTokenMapper {

    public static final String PROVIDER_ID = "prd040-consent-claim-mapper";
    public static final String CLAIM_NAME = "claim.name";
    public static final String CONSENT_STEP_URL = "consent.step.url";

    private static final Logger LOG = Logger.getLogger(ConsentClaimMapper.class);
    private static final List<ProviderConfigProperty> CONFIG = new ArrayList<>();
    private static final HttpClient HTTP = HttpClient.newBuilder()
            .connectTimeout(Duration.ofSeconds(3))
            .build();

    static {
        ProviderConfigProperty name = new ProviderConfigProperty();
        name.setName(CLAIM_NAME);
        name.setLabel("Claim name");
        name.setType(ProviderConfigProperty.STRING_TYPE);
        name.setDefaultValue("openbanking_intent_id");
        CONFIG.add(name);
        ProviderConfigProperty url = new ProviderConfigProperty();
        url.setName(CONSENT_STEP_URL);
        url.setLabel("Consent step URL");
        url.setType(ProviderConfigProperty.STRING_TYPE);
        CONFIG.add(url);
        OIDCAttributeMapperHelper.addIncludeInTokensConfig(CONFIG, ConsentClaimMapper.class);
    }

    @Override
    public String getDisplayCategory() {
        return TOKEN_MAPPER_CATEGORY;
    }

    @Override
    public String getDisplayType() {
        return "PRD-040 consent claim (lab stub)";
    }

    @Override
    public String getHelpText() {
        return "Copies the consent identifier from the claims request into the tokens after a stub consent step.";
    }

    @Override
    public List<ProviderConfigProperty> getConfigProperties() {
        return CONFIG;
    }

    @Override
    public String getId() {
        return PROVIDER_ID;
    }

    @Override
    protected void setClaim(IDToken token, ProtocolMapperModel model, UserSessionModel userSession,
                            KeycloakSession session, ClientSessionContext ctx) {
        AuthenticatedClientSessionModel clientSession = ctx.getClientSession();
        String claims = clientSession.getNote(OIDCLoginProtocol.CLAIMS_PARAM);
        String clientId = clientSession.getClient().getClientId();
        if (claims == null || claims.isEmpty()) {
            LOG.infof("PRD040 no-claims-note client=%s token=%s", clientId, token.getType());
            return;
        }
        String name = model.getConfig().getOrDefault(CLAIM_NAME, "openbanking_intent_id");
        ClaimsRepresentation rep;
        try {
            rep = JsonSerialization.readValue(claims, ClaimsRepresentation.class);
        } catch (IOException e) {
            LOG.warnf("PRD040 unreadable-claims client=%s", clientId);
            return;
        }
        ClaimsRepresentation.ClaimContext idCtx = ClaimsRepresentation.ClaimContext.ID_TOKEN;
        if (!rep.isPresent(name, idCtx) || rep.isPresentAsNullClaim(name, idCtx)) {
            LOG.infof("PRD040 no-consent-claim client=%s token=%s", clientId, token.getType());
            return;
        }
        ClaimsRepresentation.ClaimValue<String> value = rep.getClaimValue(name, idCtx, String.class);
        if (value == null || value.getValue() == null || value.getValue().isEmpty()) {
            return;
        }
        String consentId = value.getValue();
        String url = model.getConfig().get(CONSENT_STEP_URL);
        String marker = "prd040.consent." + consentId;
        if (url != null && !url.isEmpty() && clientSession.getNote(marker) == null) {
            int status = consentStep(url, consentId, clientId, userSession.getUser().getId());
            LOG.infof("PRD040 consent-step consent=%s client=%s status=%d", consentId, clientId, status);
            if (status < 200 || status > 299) {
                return;
            }
            clientSession.setNote(marker, "authorised");
        }
        token.getOtherClaims().put(name, consentId);
        LOG.infof("PRD040 claim-issued consent=%s client=%s token=%s", consentId, clientId, token.getType());
    }

    private static int consentStep(String url, String consentId, String clientId, String subject) {
        try {
            Map<String, String> body = new LinkedHashMap<>();
            body.put("consent_id", consentId);
            body.put("client_id", clientId);
            body.put("subject", subject);
            HttpRequest req = HttpRequest.newBuilder(URI.create(url))
                    .timeout(Duration.ofSeconds(5))
                    .header("Content-Type", "application/json")
                    .POST(HttpRequest.BodyPublishers.ofString(JsonSerialization.writeValueAsString(body)))
                    .build();
            return HTTP.send(req, HttpResponse.BodyHandlers.discarding()).statusCode();
        } catch (IOException | IllegalArgumentException e) {
            LOG.warnf("PRD040 consent-step-error %s", e.toString());
            return -1;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return -1;
        }
    }
}
