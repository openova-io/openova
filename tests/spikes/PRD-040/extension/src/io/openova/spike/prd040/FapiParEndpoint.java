package io.openova.spike.prd040;

import jakarta.ws.rs.core.Response;

import org.jboss.logging.Logger;
import org.keycloak.OAuthErrorException;
import org.keycloak.events.EventBuilder;
import org.keycloak.models.KeycloakSession;
import org.keycloak.protocol.oidc.par.endpoints.ParEndpoint;
import org.keycloak.services.CorsErrorResponseException;

/**
 * Lab-test stub (spike PRD-040, never shipped). Keycloak's PAR endpoint turns
 * every client-authentication failure into HTTP 401 with error
 * "invalid_request". RFC 6749 section 5.2, which PAR section 2.3 follows, pairs
 * a client-authentication failure with "invalid_client" (401). This subclass
 * only rewrites that one error; the request handling is Keycloak's.
 */
public class FapiParEndpoint extends ParEndpoint {

    private static final Logger LOG = Logger.getLogger(FapiParEndpoint.class);

    public FapiParEndpoint(KeycloakSession session, EventBuilder event) {
        super(session, event);
    }

    @Override
    protected void authorizeClient() {
        try {
            super.authorizeClient();
        } catch (CorsErrorResponseException e) {
            LOG.infof("PRD040 par-client-auth-failed rewritten to invalid_client (was: %s)", e.getErrorDescription());
            throw new CorsErrorResponseException(cors, OAuthErrorException.INVALID_CLIENT,
                    "Client authentication failed.", Response.Status.UNAUTHORIZED);
        }
    }
}
