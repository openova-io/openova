package io.openova.keycloak.fapi;

import org.keycloak.models.ClientSessionContext;
import org.keycloak.models.KeycloakSession;
import org.keycloak.models.ProtocolMapperModel;
import org.keycloak.models.UserSessionModel;
import org.keycloak.protocol.oidc.mappers.AbstractOIDCProtocolMapper;
import org.keycloak.protocol.oidc.mappers.OIDCAccessTokenMapper;
import org.keycloak.protocol.oidc.mappers.OIDCAttributeMapperHelper;
import org.keycloak.protocol.oidc.mappers.OIDCIDTokenMapper;
import org.keycloak.provider.ProviderConfigProperty;
import org.keycloak.representations.IDToken;

import java.util.ArrayList;
import java.util.List;

/**
 * Writes the consent identifier that the consent step authorised into the access token and the ID
 * token, as the claim {@value ConsentRequest#CLAIM}. It reads only the client note
 * {@value FapiConsentAuthenticator#CONSENT_NOTE}; a value the client asked for in its claims
 * request never reaches a token through this mapper.
 */
public final class FapiConsentClaimMapper extends AbstractOIDCProtocolMapper
        implements OIDCAccessTokenMapper, OIDCIDTokenMapper {

    /** The provider identifier of the mapper. */
    public static final String ID = "fapi-consent-claim";

    private static final List<ProviderConfigProperty> CONFIG = new ArrayList<>();

    static {
        OIDCAttributeMapperHelper.addIncludeInTokensConfig(CONFIG, FapiConsentClaimMapper.class);
    }

    @Override
    public String getId() {
        return ID;
    }

    @Override
    public String getDisplayType() {
        return "Open-banking consent claim";
    }

    @Override
    public String getDisplayCategory() {
        return TOKEN_MAPPER_CATEGORY;
    }

    @Override
    public String getHelpText() {
        return "Adds the consent the customer authorised in the consent step as the claim " + ConsentRequest.CLAIM + ".";
    }

    @Override
    public List<ProviderConfigProperty> getConfigProperties() {
        return CONFIG;
    }

    @Override
    protected void setClaim(IDToken token, ProtocolMapperModel mappingModel, UserSessionModel userSession,
                            KeycloakSession keycloakSession, ClientSessionContext clientSessionCtx) {
        String consentId = clientSessionCtx.getClientSession().getNote(FapiConsentAuthenticator.CONSENT_NOTE);
        if (consentId != null && !consentId.isEmpty()) {
            token.getOtherClaims().put(ConsentRequest.CLAIM, consentId);
        }
    }
}
