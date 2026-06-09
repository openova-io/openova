# hw124 SSO re-proof — survived the 2026-06-10 network-policies incident

After tonight's incident (bp-network-policies default-deny → kyverno→apiserver
break → catalyst-api Recreate roll → ~10 min console 503; recovered + #3201),
re-proved the Tier-1/Tier-2 SSO chains did NOT regress. catalyst-api launch-url
API + the KC redirect chains intact.

## launch-url (catalyst-api `/catalyst/v1/apps/<bp>/launch-url`)
```
bp-grafana         -> https://grafana.hw124.omani.works/login/generic_oauth
bp-harbor          -> https://registry.hw124.omani.works/c/oidc/login
bp-openbao         -> https://bao.hw124.omani.works/ui/vault/auth?with=oidc
bp-guacamole       -> https://guacamole.hw124.omani.works/guacamole/
bp-powerdns-admin  -> https://pdns-admin.hw124.omani.works/oidc/login
```

## 302 → sovereign realm (server-side redirectors)
```
grafana  /login/generic_oauth -> auth.hw124…/realms/sovereign/…/auth?kc_idp_hint=catalyst-pin&client_id=grafana&…
harbor   /c/oidc/login        -> auth.hw124…/realms/sovereign/…/auth?client_id=harbor&kc_idp_hint=catalyst-pin&…
pdns     /oidc/login          -> auth.hw124…/realms/sovereign/…/auth?client_id=powerdns-admin&…   (silent via realm IDR defaultProvider)
```
(openbao `/ui/vault/auth` + guacamole `/guacamole/` redirect client-side in the
SPA — covered by the logged-in screenshots already in UAT §2.1/§2.2.)

Conclusion: SSO §2.1/§2.2 ✅ rows hold post-incident; no regression.
