# SOV-070 Spike: a scoped cloud identity per Sovereign on the reference cloud (result)

Outcome: supplied

Date: 2026-11-13. Type: lab test on the reference cloud (the operator-hosted Huawei Cloud Stack that `infra/providers/huawei` is written for). Nothing was created in the cloud.

## Question

Can the parent mint a cloud identity per Sovereign, limited to one project, and can that identity rotate its own key? Six rows:

- E1: the provisioning account creates a user or agency limited to one project through the identity interface.
- E2: an access key is created for that identity.
- E3: `tofu plan` of the reference module with that key runs with no permission error.
- E4: with that key, a second key is created for the same identity and the first is deleted (self-rotation).
- E5: the deleted key is rejected on the next call.
- E6: with that key, an attempt to create another user is refused.

## What was run

- **Where.** From a host outside the cloud, as the parent runs today. The provisioning account's own key was used only to call the identity service. It was never treated as the scoped key.
- **Signing.** AK/SK signing (`SDK-HMAC-SHA256`), the same as the platform's Go signer. The signer was checked first on two published reads: list networks and list servers both answered 200.
- **Host names.** The reference module names `iam.<region>.<cloud domain>` as its identity endpoint. Every name under `<region>.<cloud domain>` resolves to one API gateway, including a made-up name. The gateway chooses the service by host name. Names without the region (`iam.<cloud domain>` and similar) do not resolve.
- **Identity reads.** Signed GET on 9 Keystone and Huawei IAM routes, on 3 host names (`iam`, `iam-apigateway-proxy`, `iam-cache-proxy`): 27 calls. The routes were `/v3/auth/projects`, `/v3/auth/domains`, `/v3/users`, `/v3.0/OS-USER/users`, `/v3/projects`, `/v3.0/OS-CREDENTIAL/credentials`, `/v3/roles`, `/v3.0/OS-AGENCY/agencies` and `/v3/auth/catalog`.
- **Management-portal style routes.** Signed GET on 7 routes (`/rest/vdc/v3.0/users`, `/rest/vdc/v3.1/users`, `/rest/vdc/v3.0/vdcs`, `/rest/vdc/v3.0/projects`, `/rest/vdc/v3.0/users/current`, `/v3-ext/auth/projects`, `/v3.0/OS-USER/users/x/credentials`), on 3 host names (`sc`, `iam`, `console` under the region): 21 calls.
- **Identity writes.** These calls could not create anything. Each POST or PUT carried an empty body `{}`, which a published route would reject as invalid. Each DELETE named a key id that does not exist. 8 routes on 2 host names: 16 calls. The routes were POST `/v3.0/OS-USER/users`, POST `/v3/users`, POST `/v3.0/OS-CREDENTIAL/credentials`, DELETE and PUT `/v3.0/OS-CREDENTIAL/credentials/<made-up id>`, POST `/v3.0/OS-AGENCY/agencies`, POST `/v3.0/OS-CREDENTIAL/securitytokens` and POST `/v3/auth/tokens`.
- **The same read without the project header:** 1 call.
- **Controls, unsigned:**
  - a published network route;
  - an unknown path on the network host;
  - an unknown host;
  - `/v3/users` and `/v3/auth/tokens` on the identity host.
- **Portal and sign-in hosts.** 4 identity routes each on the web console host and the sign-in host. Both sit on addresses other than the API gateway.
- **Through OpenTofu.** `tofu plan` ran with OpenTofu 1.12.0 and huaweicloud provider 1.91.0, the version in the module's lock file. The provider used the module's endpoint overrides. The config read three data sources: `huaweicloud_vpcs` as the control, then `huaweicloud_identity_projects` and `huaweicloud_identity_users`.
- **Key rejection codes, for the probe verb.**
  - A made-up access key with a random secret, on a published read.
  - The real key with a made-up project id.
  - A wrong secret with the real key was deliberately not sent, so the live account could not be put at risk.

## Measured values

| Row | Result | Measured |
|---|---|---|
| E1 | no | Every identity route answers `404 APIGW.0101` "The API does not exist or has not been published in the environment". That held for all 27 reads, all 21 portal-style reads and all 16 write attempts, signed or unsigned, with or without the project header. `tofu plan` failed on both identity data sources with the same code: `GET …/v3/projects` and `GET …/v3/users?enabled=true`. The control data source in the same plan read in 3 s. No route exists to create a user or an agency. |
| E2 | no | POST `/v3.0/OS-CREDENTIAL/credentials` answers `404 APIGW.0101`. No identity existed to hold a key in any case (E1). |
| E3 | not run | There is no scoped key. The plan was not run against any Sovereign's state. |
| E4 | no | Not run with a scoped key. The routes it needs answer `404 APIGW.0101` for every caller, including the provisioning account: key create (POST), key delete (DELETE) and key update (PUT). So no key on this cloud can rotate itself through the API. |
| E5 | not run | No key was deleted. A related value: an access key the cloud does not know gets `401 APIGW.0301` "Incorrect IAM authentication information: ak … not exist". |
| E6 | not run | There is no scoped key. Creating a user is refused for every caller at the gateway (`404 APIGW.0101`), not for lack of rights. Whether a key the owner supplies can create users in the console was not measured. |

Controls:
- Unsigned call to a published network route: `401 APIGW.0301`. So a published route checks the signature.
- Unsigned call to an unknown path, or to an unknown host: `404 APIGW.0101`.
- Unsigned `/v3/users` and `/v3/auth/tokens` on the identity host: `404 APIGW.0101`. The gateway refuses the identity routes before it reads the signature, so `APIGW.0101` here means "not published". It does not mean "no rights".

Portal and sign-in hosts:
- The web console host answers 200 with the single-page application's HTML for any path. It is not an API.
- The sign-in host answers a bare 404 with an empty body.

Other measured values:
- The real key with a made-up project id gets `401 APIGW.0301` "get token error, status:400". The gateway turns AK/SK into a project token itself, and a wrong project fails there.
- A valid key and project on a published read (list networks, `limit=1`) answers 200. That call is the cheapest "accepted" probe.

## Decision

E1 and E2 are `no`. The decision rule makes the mode on this cloud `supplied`:
- The cloud profile's `spec.identity.mode` for the reference cloud is `supplied`.
- The owner provides the credential, and a new key at hand-over.
- `ownership-pivot` waits with reason `CredentialRequired` until the new key is in place.
- Founder question 4 is raised.

E4 is also `no`. This does not change the outcome, but it does change rotation (see below). E6 was not measured, so it is recorded as an open risk.

## What follows for the plan

1. **CLD-102.** The Huawei adapter does not implement `mint`, `rotate` or `revoke`. On this cloud they must exit with the "not supported" code. It implements `probe` only:
   - Accepted: 200 on a signed list-networks read with `limit=1`.
   - Rejected: `401 APIGW.0301`. Unknown key: "ak … not exist". Wrong project: "get token error".
   - Not published: `404 APIGW.0101`. This must be treated as a capability answer, never as accepted or rejected.
2. **The provisioning package that reads `cloudCredentialMode`.** It keeps `supplied` as the only mode on the reference cloud. `minted` must be refused for this provider rather than tried.
3. **SOV-410.** It sets `spec.identity.mode: supplied` in the Huawei profile. `credential-install` takes the `CredentialRequired` wait path.
4. **SOV-730 (credential rotation).** On this cloud rotation cannot be automated in either direction, by parent or by child. The owner creates the new key in the console and supplies it. The platform can only probe the new key, swap it in and confirm the old one is rejected. The owner deletes the old key in the console.
5. **Founder question 4.** The cloud operator publishes no identity API on the tenant gateway. A minted identity is only possible if they publish one: user create, access-key create and delete, and a project-scoped policy. Otherwise they must issue a project-limited user and key per Sovereign by hand.

## What is left

- **E6 risk.** The rights of the key the owner supplies are not known. No API can check them, because every identity route is unpublished. Whether it can create users or reach other projects is the owner's choice, and is not measured.
- **An identity endpoint inside the cloud.** An identity endpoint reachable only from inside the cloud's networks was not looked for. That needs a VM with a way to send results out. The parent runs outside the cloud, so this would not change E1 or E2 for minting by the parent.
- **E3 with a non-account key.** The zero-change plan with a key other than the account key needs a project-limited key that the operator or the owner creates by hand. SOV-080 E5 depends on such a key.

## Evidence

Outcome: **supplied**. Run from outside the cloud on 2026-11-13, 06:44 to 06:52 UTC, with no CI run and no cloud resource created. Every identity call above returned the same body:

```
404 {"error_msg":"The API does not exist or has not been published in the environment","error_code":"APIGW.0101"}
```

The two errors from `tofu plan` (huaweicloud provider 1.91.0):

```
Error: error retrieving IAM project list: Resource not found: [GET https://iam.<region>.<cloud domain>/v3/projects], request_id: 7ae120de990d2b341be7eda18a600a90, error message: {"error_msg":"The API does not exist or has not been published in the environment","error_code":"APIGW.0101",...}
Error: error retrieving IAM user list: Resource not found: [GET https://iam.<region>.<cloud domain>/v3/users?enabled=true], request_id: 0bf4fea2f0b8a419febd8a5b59dd5cab, error message: {"error_msg":"The API does not exist or has not been published in the environment","error_code":"APIGW.0101",...}
```

The control in the same plan: `data.huaweicloud_vpcs.control: Read complete after 3s`.

The rejection bodies for the probe verb:

```
made-up key:            401 {"error_msg":"Incorrect IAM authentication information: ak <key> not exist","error_code":"APIGW.0301"}
real key, made-up project: 401 {"error_msg":"Incorrect IAM authentication information: get token error,status:400","error_code":"APIGW.0301"}
unsigned, published route: 401 APIGW.0301
```

## Evidence

Outcome: **supplied**. The measurements were made on the reference cloud on 13 November 2026.

- 2026-11-13 06:44-06:52 UTC: signed REST probes (SDK-HMAC-SHA256, AK/SK) with the provisioning account's key from outside the cloud, against the identity host names of the reference module and the region's API gateway; 65 signed identity-route calls, 5 unsigned controls, 8 calls to the portal and sign-in hosts
- 2026-11-13 06:50 UTC: tofu plan (OpenTofu 1.12.0, huaweicloud provider 1.91.0 as in the module's lock file) of a read-only probe config: data sources huaweicloud_vpcs (control), huaweicloud_identity_projects, huaweicloud_identity_users; exit 1 after 6 s

Resources the test created, and their end state:

- nothing was created

## What later tasks must take into account

SOV-070 outcome is "supplied". It applies on the reference cloud, the Huawei Cloud Stack the provider module targets. The cloud's API gateway publishes no identity API at all. Every user, agency, access-key and token route answers 404 APIGW.0101 ("not published"). That held across 65 signed calls on 3 identity host names, including writes sent with invalid bodies, and for unsigned calls too. tofu plan with the module's own provider (huaweicloud 1.91.0) fails on the identity data sources with the same code, while a network read in the same plan succeeds. So E1 and E2 are no, and E4 is no for every caller. E3, E5 and E6 could not run because no scoped key can exist. Nothing was created in the cloud. The working folder was deleted.

What later tasks must take into account:
(1) The Huawei profile is spec.identity.mode: supplied, and founder question 4 is raised. A minted identity needs the cloud operator either to publish user create, access-key create and delete, and a project-scoped policy, or to issue a project-limited user and key per Sovereign by hand.
(2) CLD-102: the Huawei adapter implements probe only. mint, rotate and revoke return "not supported".
(3) The probe verb must read three answers:
   - accepted: 200 on a signed list-networks read with limit=1;
   - rejected: 401 APIGW.0301 ("ak … not exist" for an unknown key, "get token error,status:400" for a wrong project);
   - capability answer: 404 APIGW.0101 means "not published". It is never "rejected", and never "no rights".
(4) SOV-410 takes the CredentialRequired wait path on this cloud. The provisioning step must refuse minted for this provider rather than try it.
(5) SOV-730: rotation cannot be automated here, by parent or child. The owner creates the new key in the console. The platform probes it and swaps it in. The owner deletes the old key.
(6) Open risk (E6): the rights of a key the owner supplies cannot be checked through any API.
(7) Not measured:
   - an identity endpoint reachable only from inside the cloud's networks (needs a VM with an outbound path);
   - SOV-080 E5's plan with a non-account key (needs a project-limited key made by hand).
(8) Probing facts:
   - Every name under the region's domain resolves to one gateway, which routes by host name, so DNS success proves nothing.
   - The AK/SK canonical URI must end with "/".
   - A wrong secret was deliberately not tried with the live account key, to avoid any lockout risk to the running environment.
