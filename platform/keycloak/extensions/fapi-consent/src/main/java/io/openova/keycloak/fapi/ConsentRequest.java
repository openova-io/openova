package io.openova.keycloak.fapi;

import com.fasterxml.jackson.databind.JsonNode;
import org.keycloak.util.JsonSerialization;

import java.io.IOException;
import java.nio.charset.StandardCharsets;

/**
 * Reads the consent identifier from the claims request of an authorisation request.
 *
 * <p>The carrier is {@code intent-claim}: the third party asks for the essential ID-token claim
 * {@value #CLAIM} with the consent identifier as its value, inside the signed request object it
 * pushes to Keycloak. Keycloak keeps that claims request as the client note {@code claims} of the
 * authentication session, and later of the client session.
 */
public final class ConsentRequest {

    /** The claim that carries the consent identifier, in the request and in the tokens. */
    public static final String CLAIM = "openbanking_intent_id";

    /** The longest consent identifier accepted. */
    public static final int MAX_LENGTH = 128;

    private ConsentRequest() {
    }

    /**
     * Returns the consent identifier of a claims request, or null when the request carries none.
     *
     * <p>The identifier is the string {@code value} of the member {@value #CLAIM} of the member
     * {@code id_token}, and that member must say {@code "essential": true}. A request that is not a
     * JSON object, a claim that is not essential, and a value that is empty, longer than
     * {@value #MAX_LENGTH} characters or holds a character outside printable ASCII carry no consent.
     */
    public static String consentId(String claimsJson) {
        if (claimsJson == null || claimsJson.isBlank()) {
            return null;
        }
        JsonNode root;
        try {
            root = JsonSerialization.mapper.readTree(claimsJson.getBytes(StandardCharsets.UTF_8));
        } catch (IOException e) {
            return null;
        }
        if (root == null || !root.isObject()) {
            return null;
        }
        JsonNode claim = root.path("id_token").path(CLAIM);
        if (!claim.isObject() || !claim.path("essential").isBoolean() || !claim.path("essential").booleanValue()) {
            return null;
        }
        JsonNode value = claim.path("value");
        if (!value.isTextual()) {
            return null;
        }
        String id = value.textValue();
        if (id.isEmpty() || id.length() > MAX_LENGTH) {
            return null;
        }
        for (int i = 0; i < id.length(); i++) {
            char c = id.charAt(i);
            if (c < 0x21 || c > 0x7e) {
                return null;
            }
        }
        return id;
    }

    /** Percent-encodes a value for one path segment: every byte outside the unreserved set. */
    public static String pathSegment(String value) {
        StringBuilder out = new StringBuilder();
        for (byte b : value.getBytes(StandardCharsets.UTF_8)) {
            int c = b & 0xff;
            boolean unreserved = (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
                    || c == '-' || c == '.' || c == '_' || c == '~';
            if (unreserved) {
                out.append((char) c);
            } else {
                out.append('%').append(Character.toUpperCase(Character.forDigit(c >> 4, 16)))
                        .append(Character.toUpperCase(Character.forDigit(c & 0xf, 16)));
            }
        }
        return out.toString();
    }
}
