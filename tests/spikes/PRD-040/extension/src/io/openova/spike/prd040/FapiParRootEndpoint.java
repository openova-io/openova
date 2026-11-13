package io.openova.spike.prd040;

import jakarta.ws.rs.Path;

import org.keycloak.events.EventBuilder;
import org.keycloak.models.KeycloakSession;
import org.keycloak.protocol.oidc.ext.OIDCExtProvider;
import org.keycloak.protocol.oidc.par.endpoints.ParEndpoint;
import org.keycloak.protocol.oidc.par.endpoints.ParRootEndpoint;

/**
 * Lab-test stub (spike PRD-040, never shipped): replaces Keycloak's built-in
 * pushed-authorisation endpoint provider (same id "par", higher order) so that
 * the endpoint answers a failed client authentication with invalid_client.
 * Everything else is Keycloak's own code.
 */
public class FapiParRootEndpoint extends ParRootEndpoint {

    private final KeycloakSession session;
    private EventBuilder event;

    public FapiParRootEndpoint() {
        this(null);
    }

    public FapiParRootEndpoint(KeycloakSession session) {
        super(session);
        this.session = session;
    }

    @Override
    @Path("/request")
    public ParEndpoint request() {
        return new FapiParEndpoint(session, event);
    }

    @Override
    public OIDCExtProvider create(KeycloakSession session) {
        return new FapiParRootEndpoint(session);
    }

    @Override
    public void setEvent(EventBuilder event) {
        super.setEvent(event);
        this.event = event;
    }

    @Override
    public int order() {
        return 100;
    }
}
