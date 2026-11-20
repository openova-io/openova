# cortex screens

The screens of `bp-cortex` for the people of one Organization: sources, upload, documents, live
job state and assistants. TypeScript compiled by `tsc` to ES modules the browser loads as they
are; no runtime dependency and no bundler.

## What each screen calls

| Screen | Reads | Writes |
|---|---|---|
| Sources | the Application's `spec.parameters.sources`, `GET /v1/sources` | the Application update route |
| Upload | the upload sources of the parameters | `POST /v1/sources/{id}/documents` (`file`, optional `reader` parts) |
| Documents | `POST /v1/search`, `GET /v1/documents/{id}`, `GET /v1/documents/{id}/content` | `DELETE /v1/documents/{id}` |
| Jobs | `GET /v1/events` (a frame is a hint: the screens refetch) | none |
| Assistants | `spec.parameters.assistants`, `GET /v1/models` | the Application update route; `POST /v1/chat/completions` |

A source or an assistant is a definition, so it is never sent to cortex. Every save is
`PUT {platformURL}/api/v1/sovereigns/{sovereign}/applications/{application}?namespace={namespace}`
with the whole `spec.parameters`, the version the screen read (`ETag`) in `If-Match`, and
`bindings` exactly as read. On 409 `conflict` or 412 the screen reloads the Application, lists where the
refused save and the stored parameters differ, and offers the draft again. An Application route
that returns no `ETag` gets no save at all.

Every call carries the signed-in user's access token. Sign-on is the authorisation-code flow with
PKCE against the Organization's realm, as the public registration `clientId`; the token is kept in
the tab's session storage.

## Settings

The release that serves `web/` writes `web/config.json`:

```json
{"issuerURL": "https://<realm issuer>", "clientId": "cortex", "cortexURL": "",
 "platformURL": "https://<platform API>", "sovereign": "<id>", "application": "<name>", "namespace": "<namespace>"}
```

`cortexURL` empty means the screens and cortex share an origin.

## Build and test

```sh
npm ci
npm run build   # compiles src/ into web/ beside the tracked index.html and styles.css
npm test        # compiles src/ with the tests into .test-out/ and runs node --test
```

`embed.go` embeds `web/` (package `ui`, variable `Web`) for a binary that serves the screens.
