package io.openova.keycloak.fapi;

import jakarta.ws.rs.core.Response;
import org.keycloak.OAuthErrorException;
import org.keycloak.events.EventBuilder;
import org.keycloak.models.KeycloakSession;
import org.keycloak.protocol.oidc.par.endpoints.ParEndpoint;
import org.keycloak.services.CorsErrorResponseException;

/**
 * Keycloak's pushed-authorisation-request endpoint with one correction: a failed client
 * authentication is answered with HTTP 401 and {@code invalid_client}, as RFC 6749 section 5.2
 * requires. The built-in endpoint answers 401 with {@code invalid_request}. Everything else is the
 * built-in endpoint's behaviour.
 */
public class FapiParEndpoint extends ParEndpoint {

    public FapiParEndpoint(KeycloakSession session, EventBuilder event) {
        super(session, event);
    }

    @Override
    protected void authorizeClient() {
        try {
            super.authorizeClient();
        } catch (CorsErrorResponseException e) {
            if (e.getResponse().getStatus() == Response.Status.UNAUTHORIZED.getStatusCode()) {
                throw throwErrorResponseException(OAuthErrorException.INVALID_CLIENT, e.getErrorDescription(),
                        Response.Status.UNAUTHORIZED);
            }
            throw e;
        }
    }
}
