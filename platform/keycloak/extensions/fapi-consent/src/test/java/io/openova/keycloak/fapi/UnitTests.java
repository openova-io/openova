package io.openova.keycloak.fapi;

import java.util.Arrays;
import java.util.List;
import java.util.Objects;

/**
 * The unit tests of the extension. They run without a test framework, because the build runs
 * inside the Keycloak image, which holds no test framework: {@code main} runs every case, prints
 * one line per case and exits with 1 when one fails.
 */
public final class UnitTests {

    private static int failures;
    private static int cases;

    private UnitTests() {
    }

    public static void main(String[] args) {
        consentId();
        pathSegment();
        chosen();
        acrAndTime();
        System.err.printf("unit tests: %d cases, %d failed%n", cases, failures);
        System.exit(failures == 0 ? 0 : 1);
    }

    private static void consentId() {
        String ok = "{\"id_token\":{\"openbanking_intent_id\":{\"value\":\"urn-c-1\",\"essential\":true}}}";
        eq("consent id from an essential ID-token claim", "urn-c-1", ConsentRequest.consentId(ok));
        eq("no claims request", null, ConsentRequest.consentId(null));
        eq("empty claims request", null, ConsentRequest.consentId(""));
        eq("claims request that is not JSON", null, ConsentRequest.consentId("{"));
        eq("claims request that is an array", null, ConsentRequest.consentId("[]"));
        eq("claim not essential", null, ConsentRequest.consentId(
                "{\"id_token\":{\"openbanking_intent_id\":{\"value\":\"c\",\"essential\":false}}}"));
        eq("essential as a string", null, ConsentRequest.consentId(
                "{\"id_token\":{\"openbanking_intent_id\":{\"value\":\"c\",\"essential\":\"true\"}}}"));
        eq("claim only in userinfo", null, ConsentRequest.consentId(
                "{\"userinfo\":{\"openbanking_intent_id\":{\"value\":\"c\",\"essential\":true}}}"));
        eq("value is a number", null, ConsentRequest.consentId(
                "{\"id_token\":{\"openbanking_intent_id\":{\"value\":7,\"essential\":true}}}"));
        eq("value is empty", null, ConsentRequest.consentId(
                "{\"id_token\":{\"openbanking_intent_id\":{\"value\":\"\",\"essential\":true}}}"));
        eq("value holds a space", null, ConsentRequest.consentId(
                "{\"id_token\":{\"openbanking_intent_id\":{\"value\":\"a b\",\"essential\":true}}}"));
        String longest = "x".repeat(ConsentRequest.MAX_LENGTH);
        eq("value of the longest length", longest, ConsentRequest.consentId(
                "{\"id_token\":{\"openbanking_intent_id\":{\"value\":\"" + longest + "\",\"essential\":true}}}"));
        eq("value one character too long", null, ConsentRequest.consentId(
                "{\"id_token\":{\"openbanking_intent_id\":{\"value\":\"" + longest + "x\",\"essential\":true}}}"));
        eq("values list instead of value", null, ConsentRequest.consentId(
                "{\"id_token\":{\"openbanking_intent_id\":{\"values\":[\"c\"],\"essential\":true}}}"));
    }

    private static void pathSegment() {
        eq("unreserved characters stay", "aZ09-._~", ConsentRequest.pathSegment("aZ09-._~"));
        eq("slash, colon and percent are encoded", "a%2Fb%3Ac%25", ConsentRequest.pathSegment("a/b:c%"));
        eq("query characters are encoded", "a%3Fb%23c%26", ConsentRequest.pathSegment("a?b#c&"));
    }

    private static void chosen() {
        List<String> offered = List.of("acc-1", "acc-2", "acc-3");
        eq("chosen accounts in offered order", List.of("acc-1", "acc-3"),
                Decision.chosen(offered, Arrays.asList("acc-3", "acc-1")));
        eq("a repeated account counts once", List.of("acc-2"),
                Decision.chosen(offered, Arrays.asList("acc-2", "acc-2")));
        refused("no account chosen", () -> Decision.chosen(offered, List.of()));
        refused("no form field", () -> Decision.chosen(offered, null));
        refused("an account that was not offered", () -> Decision.chosen(offered, List.of("acc-1", "acc-9")));
    }

    private static void acrAndTime() {
        eq("acr after a sign-in", "1", Decision.acr(false));
        eq("acr after a reused session", "0", Decision.acr(true));
        eq("RFC 3339 time", "2026-11-14T10:20:30Z", Decision.rfc3339(1794651630L));
        eq("RFC 3339 epoch", "1970-01-01T00:00:00Z", Decision.rfc3339(0L));
    }

    private static void eq(String name, Object want, Object got) {
        cases++;
        if (Objects.equals(want, got)) {
            System.err.println("ok   " + name);
        } else {
            failures++;
            System.err.println("FAIL " + name + ": want " + want + ", got " + got);
        }
    }

    private static void refused(String name, Runnable r) {
        cases++;
        try {
            r.run();
            failures++;
            System.err.println("FAIL " + name + ": no exception");
        } catch (IllegalArgumentException e) {
            System.err.println("ok   " + name);
        }
    }
}
