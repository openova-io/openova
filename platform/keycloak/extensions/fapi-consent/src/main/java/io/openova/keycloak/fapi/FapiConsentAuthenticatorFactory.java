package io.openova.keycloak.fapi;

import org.keycloak.Config;
import org.keycloak.authentication.Authenticator;
import org.keycloak.authentication.AuthenticatorFactory;
import org.keycloak.models.AuthenticationExecutionModel;
import org.keycloak.models.KeycloakSession;
import org.keycloak.models.KeycloakSessionFactory;
import org.keycloak.provider.ProviderConfigProperty;

import java.util.List;

/**
 * Registers the consent step as the authenticator {@value #ID}.
 *
 * <p>It reads two options of its SPI scope: {@code authz-url}, the base address of the internal
 * authorisation API, and {@code identity-scheme}, the scheme under which the API resolves the
 * signed-in user's name to a customer. Without both, Keycloak still starts and every other realm
 * works; a login that carries a consent then ends with {@code authorisation-unavailable}.
 */
public final class FapiConsentAuthenticatorFactory implements AuthenticatorFactory {

    /** The provider identifier of the consent step. */
    public static final String ID = "fapi-consent";

    private String authzUrl;
    private String identityScheme;

    @Override
    public void init(Config.Scope config) {
        authzUrl = blankToNull(config.get("authz-url"));
        identityScheme = blankToNull(config.get("identity-scheme"));
    }

    private static String blankToNull(String s) {
        return s == null || s.isBlank() ? null : s.trim();
    }

    @Override
    public Authenticator create(KeycloakSession session) {
        return new FapiConsentAuthenticator(authzUrl, identityScheme);
    }

    @Override
    public String getId() {
        return ID;
    }

    @Override
    public String getDisplayType() {
        return "Open-banking consent";
    }

    @Override
    public String getReferenceCategory() {
        return "fapi-consent";
    }

    @Override
    public boolean isConfigurable() {
        return false;
    }

    @Override
    public AuthenticationExecutionModel.Requirement[] getRequirementChoices() {
        return new AuthenticationExecutionModel.Requirement[] {
            AuthenticationExecutionModel.Requirement.REQUIRED,
            AuthenticationExecutionModel.Requirement.DISABLED,
        };
    }

    @Override
    public boolean isUserSetupAllowed() {
        return false;
    }

    @Override
    public String getHelpText() {
        return "Shows the open-banking consent named in the authorisation request and records the customer's decision.";
    }

    @Override
    public List<ProviderConfigProperty> getConfigProperties() {
        return List.of();
    }

    @Override
    public void postInit(KeycloakSessionFactory factory) {
        // Nothing to do after start.
    }

    @Override
    public void close() {
        // Nothing to release.
    }
}
