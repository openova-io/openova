# Source of profile rw-4.0.0

The six OpenAPI documents in this folder are the publisher's files of the Read/Write API 4.0.0 of the UK open-banking standard. They are copied byte for byte and are never edited. A newer release of the standard is a new profile folder.

- Publisher: Open Banking Limited, repository https://github.com/OpenBankingUK/read-write-api-specs
- Tag: `v4.0-Update-5`, the newest tag whose documents carry `info.version: 4.0.0`
- Commit: `5af5621e1b68dda5a0d821f2ffee9d282cfe550a`
- Folder in that repository: `dist/openapi/`
- Licence: the publisher's open licence, https://www.openbanking.org.uk/open-licence, named in the `info.license` field of every document

| File | Operations | SHA-256 |
|---|---|---|
| `account-info-openapi.yaml` | 29 | `7d9297935e11971173664ba793c14b6fd64e162a97d2de46967cd7f9cc0ca792` |
| `payment-initiation-openapi.yaml` | 41 | `1cb84c8b6972cb56f414448af94f5c7595d6ebe13a5c41deeba5904e04efb41e` |
| `vrp-openapi.yaml` | 9 | `f547f3cbc465970c65a744dce9c28fa5f0201fb5f07b8f35af79942e3c64fb20` |
| `confirmation-funds-openapi.yaml` | 4 | `2ab1717555e8e960980be39f07821e1c0d280662a0fbb84dd562168cb5604358` |
| `events-openapi.yaml` | 5 | `ba248d53243ddad30ee856e175f8df38669eac2b1015c7c10d88db56afbcb642` |
| `event-notifications-openapi.yaml` | 1 | `c60932f376472697c22af602d4deba5842921732aa547ef71d6c41a67389908a` |

`vrp-openapi.yaml` has Windows line endings in the publisher's repository. The file `.gitattributes` of this folder keeps every document free of line-ending conversion, so the checksums hold in every checkout.

## The answer for an operation that is not offered

`profile.yaml` sets `notImplementedStatus: 404`. The 25 international and file-payment operations lie under its `notOffered` prefixes and are answered with that status. The standard's Read/Write Data API Profile, version 4.0, section "HTTP Status Codes", part "400 (Bad Request) v/s 404 (Not Found)", says:

> If an ASPSP has not implemented an API endpoint, it **must** respond with a 404 (Not Found) for requests to that URL.

Address: https://openbankinguk.github.io/read-write-api-site3/v4.0/profiles/read-write-data-api-profile.html#_400-bad-request-v-s-404-not-found

The same part lists, among its examples, "A TPP attempts to retrieve a resource that is in the specification, but not implemented by the ASPSP" with the response 404 (Not Found).
