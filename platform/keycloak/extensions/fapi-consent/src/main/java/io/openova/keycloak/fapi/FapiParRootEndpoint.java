package io.openova.keycloak.fapi;

import jakarta.ws.rs.Path;
import org.keycloak.events.EventBuilder;
import org.keycloak.models.KeycloakSession;
import org.keycloak.protocol.oidc.ext.OIDCExtProvider;
import org.keycloak.protocol.oidc.par.endpoints.ParEndpoint;
import org.keycloak.protocol.oidc.par.endpoints.ParRootEndpoint;

/**
 * Replaces Keycloak's pushed-authorisation-request endpoint (provider {@code par}) by one that
 * differs in one answer only, see {@link FapiParEndpoint}. It has the built-in provider's
 * identifier and a higher order, so Keycloak serves this one.
 */
public class FapiParRootEndpoint extends ParRootEndpoint {

    /** The order that puts this provider before the built-in one, whose order is 0. */
    public static final int ORDER = 100;

    private final KeycloakSession session;
    private EventBuilder event;

    public FapiParRootEndpoint() {
        super();
        this.session = null;
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
        return ORDER;
    }
}
