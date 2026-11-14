package io.openova.keycloak.fapi;

import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;

/** The parts of a customer's decision that the extension works out itself. */
public final class Decision {

    /** The form value of the button that authorises the consent. */
    public static final String AUTHORISE = "authorise";

    /** The form value of the button that rejects the consent. */
    public static final String REJECT = "reject";

    private Decision() {
    }

    /**
     * Returns the accounts the customer chose, in the order they were offered.
     *
     * @throws IllegalArgumentException when no account was chosen, or one was chosen that was not
     *     offered on the page
     */
    public static List<String> chosen(List<String> offered, List<String> posted) {
        Set<String> picked = new LinkedHashSet<>(posted == null ? List.of() : posted);
        if (picked.isEmpty()) {
            throw new IllegalArgumentException("no account chosen");
        }
        for (String id : picked) {
            if (!offered.contains(id)) {
                throw new IllegalArgumentException("an account was chosen that was not offered");
            }
        }
        List<String> out = new ArrayList<>();
        for (String id : offered) {
            if (picked.contains(id)) {
                out.add(id);
            }
        }
        return out;
    }

    /**
     * The authentication context class of the decision, as Keycloak writes it into the tokens of a
     * realm without a level-of-authentication map: {@code 1} when the customer signed in during this
     * request, {@code 0} when an existing session was reused.
     */
    public static String acr(boolean reusedSession) {
        return reusedSession ? "0" : "1";
    }

    /** An epoch second as an RFC 3339 time in UTC, without fractions. */
    public static String rfc3339(long epochSecond) {
        return DateTimeFormatter.ISO_INSTANT.withZone(ZoneOffset.UTC)
                .format(Instant.ofEpochSecond(epochSecond).truncatedTo(ChronoUnit.SECONDS));
    }
}
