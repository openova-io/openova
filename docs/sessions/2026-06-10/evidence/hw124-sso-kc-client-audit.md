# hw124 SSO live audit — KC clients + OIDC chains (post-incident, 2026-06-10)

Live audit of the named Tier-1/2 SSO apps after the network-policies incident +
catalyst-api roll. KC client state via the admin API (token from
catalyst-system/catalyst-kc-sa-credentials, realm sovereign); redirect chains via
curl against the live FQDNs.

## KC clients (admin/realms/sovereign/clients)
```
guacamole         enabled=true   redirectUris=["https://guacamole.hw124.omani.works/*"]
powerdns-admin    enabled=true   redirectUris=["https://pdns-admin.hw124.omani.works/*",
                                               "https://pdns-admin.hw124.omani.works/oidc/authorized"]
catalyst-api-server enabled=true redirectUris=[]   (SA/bearer client — console PIN login, no browser redirect)
```

## Live OIDC chains
```
guacamole  /guacamole/                       -> 200
guacamole  OpenID-ext login                  -> 302 auth.hw124…/realms/sovereign/.../auth
                                                   ?kc_idp_hint=catalyst-pin&client_id=guacamole
                                                   &response_type=id_token&redirect_uri=…/guacamole/
powerdns   /oidc/login                        -> 302 auth.hw124…/realms/sovereign/.../auth
                                                   ?client_id=powerdns-admin&redirect_uri=…/oidc/authorized
```

Conclusion: guacamole + powerdns-admin silent-SSO are fully wired and serving
(KC client enabled + correct redirectUri + live chain to the sovereign realm),
consistent with their independent logged-in walks (UAT §2.2). catalyst-platform's
"SSO" is the console PIN login. All Tier-1/2 SSO verified done post-incident.
