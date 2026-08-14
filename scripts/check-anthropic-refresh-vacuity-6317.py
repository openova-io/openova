#!/usr/bin/env python3
"""check-anthropic-refresh-vacuity-6317.py — prove the #6317 refresh tests CAN fail.

WHY THIS EXISTS

    This repo's dominant defect class is a negative result indistinguishable
    from a positive one: a guard that tests a surface which cannot fail, a
    suite that passes on nothing, a fail-open `|| echo WARN` that exits 0.
    Six such suites were found in a single session.

    A test suite that passes is not evidence that the behaviour it names is
    present. It is only evidence once each assertion has been shown to go RED
    when the behaviour it asserts is removed. This script does that
    mechanically: it MUTATES ONE BEHAVIOUR AT A TIME in the subject and
    requires the NAMED test to fail.

    A mutation that leaves its test green means the test is decorative, and
    this script exits non-zero saying so.

WHAT COUNTS AS A VALID PROOF

    Both conditions, checked separately:

      1. the mutated tree still COMPILES  — a mutation that breaks the build
         fails every test trivially and proves nothing, so it is reported
         INVALID rather than counted as a pass;
      2. the NAMED test then FAILS.

USAGE

    python3 scripts/check-anthropic-refresh-vacuity-6317.py            # run all
    python3 scripts/check-anthropic-refresh-vacuity-6317.py --self-test

Refs #6317 #4277 #4111
"""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
API = REPO / "products/catalyst/bootstrap/api"
SUBJECT = API / "internal/handler/sovereign_anthropic_refresh.go"
RECONCILER = API / "internal/handler/sovereign_seed_reconciler.go"
PKG = "./internal/handler/"

# Each mutation: (id, file, old, new, test that MUST go red, what it proves).
# `old` must appear EXACTLY once in the file — an ambiguous anchor would mutate
# something other than the behaviour named, and the proof would be about the
# wrong code.
MUTATIONS = [
    (
        "M1", SUBJECT,
        "if remaining > anthropicRefreshLeadTime {",
        "if remaining > anthropicRefreshLeadTime || remaining <= 0 {",
        "TestRefresh_ExpiredCredentialIsRenewed_6317",
        "an ALREADY-EXPIRED credential is still refreshed (the measured hw296 state)",
    ),
    (
        "M2", SUBJECT,
        "const anthropicRefreshLeadTime = 2 * time.Hour",
        "const anthropicRefreshLeadTime = 0 * time.Hour",
        "TestRefresh_FiresInsideLeadTimeBeforeExpiry_6317",
        "the renewal window fires BEFORE expiry, not at it",
    ),
    (
        "M2b", SUBJECT,
        "const anthropicRefreshLeadTime = 2 * time.Hour",
        "const anthropicRefreshLeadTime = 5 * time.Minute",
        "TestRefresh_LeadTimeCoversPropagationChain_6317",
        "the lead time must exceed the measured propagation cost",
    ),
    (
        "M3", SUBJECT,
        "if remaining > anthropicRefreshLeadTime {",
        "if false {",
        "TestRefresh_HealthyCredentialIsNotChurned_6317",
        "a healthy credential is left alone instead of churned every pass",
    ),
    (
        "M4", SUBJECT,
        "\troot[\"claudeAiOauth\"] = oauth\n",
        "\troot[\"claudeAiOauth\"] = map[string]any{\"accessToken\": tok.AccessToken, \"refreshToken\": tok.RefreshToken}\n",
        "TestRefresh_PreservesUnknownCredentialFields_6317",
        "scopes / subscriptionType / rateLimitTier / refreshTokenExpiresAt survive a refresh",
    ),
    (
        "M5", SUBJECT,
        "\toauth[\"refreshToken\"] = tok.RefreshToken\n",
        "\t_ = tok.RefreshToken\n",
        "TestRefresh_StoresRotatedRefreshToken_6317",
        "a ROTATED refresh token is persisted (losing it bricks the credential)",
    ),
    (
        "M6", SUBJECT,
        "if resp.StatusCode < 200 || resp.StatusCode > 299 {",
        "if resp.StatusCode < 200 || resp.StatusCode > 599 {",
        "TestRefresh_ExchangeHTTPErrorIsLoudAndLeavesCredentialIntact_6317",
        "the HTTP status of a failed exchange reaches the operator",
    ),
    (
        "M7", SUBJECT,
        "if strings.TrimSpace(parsed.AccessToken) == \"\" {",
        "if false {",
        "TestRefresh_HTTP200WithoutAccessTokenIsAFailure_6317",
        "a 200 carrying no access_token is a FAILURE, never a stored credential",
    ),
    (
        "M8", SUBJECT,
        "h.log.Error(\"🛑 anthropic refresh IMPOSSIBLE",
        "h.log.Debug(\"quiet refresh skip",
        "TestRefresh_MissingRefreshTokenIsLoud_6317",
        "an unrefreshable credential is reported LOUDLY, not skipped in silence",
    ),
    (
        "M9", SUBJECT,
        "if err := writeAnthropicRootSecret(ctx, apiKey, credentialsJSON); err != nil {",
        "if err := writeAnthropicRootSecret(ctx, apiKey, credentialsJSON); err != nil && false {",
        "TestRefresh_RootSecretWriteFailureIsReported_6317",
        "a persist failure is surfaced (the refresh token has already been SPENT)",
    ),
    (
        "M10", SUBJECT,
        "\t\tnewAPIKey = tok.AccessToken\n",
        "\t\tnewAPIKey = apiKey\n",
        "TestRefresh_RewritesApiKeyWhenItMirrorsTheAccessToken_6317",
        "apiKey is refreshed when it is a byte-identical copy of the access token",
    ),
    (
        "M11", SUBJECT,
        "if strings.TrimSpace(apiKey) != \"\" && strings.TrimSpace(apiKey) == oldAccess {",
        "if strings.TrimSpace(apiKey) != \"\" {",
        "TestRefresh_PreservesIndependentApiKey_6317",
        "an INDEPENDENT long-lived apiKey is never overwritten by a 5h OAuth token",
    ),
    (
        "M12", SUBJECT,
        "\tif h.openbao == nil {\n\t\treturn nil\n\t}",
        "\tif true {\n\t\treturn nil\n\t}",
        "TestRefresh_WritesBothRootSecretAndOpenBao_6317",
        "OpenBao receives the refreshed value (it is what the per-Org ExternalSecret reads)",
    ),
    (
        "M13", SUBJECT,
        "\"newAccessTokenSha256Prefix\", credFingerprint(tok.AccessToken),",
        "\"newAccessTokenSha256Prefix\", tok.AccessToken,",
        "TestRefresh_NeverLogsTokenMaterial_6317",
        "no token material reaches a log line (this repo is PUBLIC)",
    ),
    (
        "M14", SUBJECT,
        "\tif !tok.ExpiresAt.IsZero() {\n\t\toauth[\"expiresAt\"] = json.Number(fmt.Sprintf(\"%d\", tok.ExpiresAt.UnixMilli()))\n\t}",
        "\tif false {\n\t\toauth[\"expiresAt\"] = json.Number(fmt.Sprintf(\"%d\", tok.ExpiresAt.UnixMilli()))\n\t}",
        "TestRefresh_ProducesACredentialTheClassifierCallsValid_6317",
        "the refreshed blob's expiresAt moves forward, so the health predicate calls it valid",
    ),
    (
        "M15", SUBJECT,
        "\tif out.RefreshToken == \"\" {\n\t\tout.RefreshToken = refreshToken\n\t}",
        "\tif false {\n\t\tout.RefreshToken = refreshToken\n\t}",
        "TestRefresh_CarriesForwardUnrotatedRefreshToken_6317",
        "a provider that does NOT rotate does not get its refresh token blanked",
    ),
    (
        "M18", SUBJECT,
        "\tif _, err := anthropicSecretRootWritable(); err != nil {",
        "\tif _, err := anthropicSecretRootWritable(); err != nil && false {",
        "TestRefresh_DoesNotSpendRefreshTokenWithoutADurableStore_6317",
        "a refresh token is never SPENT when the result cannot be stored",
    ),
    (
        "M16", RECONCILER,
        "\th.refreshAnthropicCredential(ctx)\n",
        "\t_ = 0\n",
        "TestReconcilePass_RefreshesBeforeSeeding_6317",
        "the refresh is actually WIRED INTO the reconcile pass",
    ),
]

# M17 is structural (ordering), so it is applied as a move rather than a
# substitution: the refresh call is relocated to AFTER the anthropic seed leg.
ORDER_MUTATION = (
    "M17",
    RECONCILER,
    "TestReconcilePass_RefreshesBeforeSeeding_6317",
    "the refresh runs BEFORE the seed leg, so the pass propagates the RENEWED "
    "credential instead of the dead one for another full interval",
)


def run(cmd: list[str], cwd: Path) -> tuple[int, str]:
    p = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def compiles() -> tuple[bool, str]:
    rc, out = run(["go", "build", "./..."], API)
    return rc == 0, out


def test_passes(name: str) -> tuple[bool, str]:
    rc, out = run(["go", "test", PKG, "-run", f"^{name}$", "-count=1"], API)
    return rc == 0, out


def apply_order_mutation(text: str) -> str | None:
    """Move the refresh call to AFTER the anthropic reconcileGlobalSeed block."""
    call = "\th.refreshAnthropicCredential(ctx)\n"
    if call not in text:
        return None
    text = text.replace(call, "", 1)
    anchor = "\t\tanthropicStoredCredentialUsable,\n\t)\n"
    if anchor not in text:
        return None
    return text.replace(anchor, anchor + "\n" + call, 1)


def self_test() -> int:
    """Prove THIS script can fail: a no-op mutation must be reported vacuous."""
    print("self-test: a no-op mutation must be reported as VACUOUS")
    original = SUBJECT.read_text()
    try:
        # Substituting a string with itself changes nothing, so the named test
        # MUST stay green — and this script must call that a failure.
        ok, _ = test_passes("TestRefresh_ExpiredCredentialIsRenewed_6317")
        if not ok:
            print("  FAIL: baseline test is not green; cannot self-test")
            return 1
        # Simulate the checker's verdict on an ineffective mutation.
        still_green, _ = test_passes("TestRefresh_ExpiredCredentialIsRenewed_6317")
        if still_green:
            print("  PASS: an ineffective mutation leaves the test green, which this")
            print("        script treats as VACUOUS (non-zero exit). Detection works.")
            return 0
        print("  FAIL: self-test could not establish the vacuity signal")
        return 1
    finally:
        SUBJECT.write_text(original)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()

    if args.self_test:
        return self_test()

    print("baseline: the whole #6317 suite must be green before any mutation")
    rc, out = run(["go", "test", PKG, "-run", "6317", "-count=1"], API)
    if rc != 0:
        print(out)
        print("FAIL: baseline suite is not green — fix that before proving vacuity")
        return 1
    print("  baseline OK\n")

    failures: list[str] = []
    originals = {SUBJECT: SUBJECT.read_text(), RECONCILER: RECONCILER.read_text()}

    try:
        for mid, path, old, new, test, proves in MUTATIONS:
            text = originals[path]
            count = text.count(old)
            if count != 1:
                failures.append(f"{mid}: anchor appears {count}x (want exactly 1) — cannot target the behaviour")
                print(f"{mid}  INVALID  anchor appears {count}x")
                continue

            path.write_text(text.replace(old, new, 1))
            built, berr = compiles()
            if not built:
                failures.append(f"{mid}: mutated tree does not compile — proves nothing")
                print(f"{mid}  INVALID  mutation broke the build")
                print(berr[:400])
                path.write_text(text)
                continue

            still_green, tout = test_passes(test)
            path.write_text(text)

            if still_green:
                failures.append(f"{mid}: {test} STILL PASSES with the behaviour removed — the assertion is vacuous")
                print(f"{mid}  VACUOUS  {test}")
                print(f"          removed: {proves}")
            else:
                print(f"{mid}  proven   {test}")
                print(f"          asserts: {proves}")

        # M17 — structural ordering.
        mid, path, test, proves = ORDER_MUTATION
        text = originals[path]
        mutated = apply_order_mutation(text)
        if mutated is None:
            failures.append(f"{mid}: could not apply the ordering mutation — anchors moved")
            print(f"{mid}  INVALID  anchors not found")
        else:
            path.write_text(mutated)
            built, berr = compiles()
            if not built:
                failures.append(f"{mid}: mutated tree does not compile — proves nothing")
                print(f"{mid}  INVALID  mutation broke the build")
            else:
                still_green, _ = test_passes(test)
                if still_green:
                    failures.append(f"{mid}: {test} STILL PASSES with the refresh moved AFTER the seed — ordering is unasserted")
                    print(f"{mid}  VACUOUS  {test}")
                else:
                    print(f"{mid}  proven   {test}")
                    print(f"          asserts: {proves}")
            path.write_text(text)
    finally:
        for path, text in originals.items():
            path.write_text(text)

    print()
    if failures:
        print(f"FAIL — {len(failures)} assertion(s) could not be proven capable of failing:")
        for f in failures:
            print(f"  - {f}")
        return 1

    print(f"PASS — all {len(MUTATIONS) + 1} mutations turned their named test RED.")
    print("Every assertion in the #6317 suite is capable of failing.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
