# CC2 — Unified Gitea Client SUPERSET Design

Slice CC2 of EPIC-0 (#1095) consolidates the four divergent Gitea HTTP
clients shipped by the Group C controllers (slices C1-C4) into a single
shared `core/controllers/pkg/gitea` package.

CC1 (#1135) explicitly DEFERRED this consolidation because the four
`Client` surfaces collide on signature shape and return semantics.
CC2 designs the SUPERSET — the union of every operation any Group C
controller currently uses — and migrates each controller to it without
behavior change.

## 1. The four pre-existing surfaces (input)

| Method | C1 organization | C2 environment | C3 blueprint | C4 application |
|---|---|---|---|---|
| Org get | `GetOrg(slug) (Org, error)` | `GetOrg(org) (*Org, error)` | — | — |
| Org create | `CreateOrg(slug, fullName, desc, vis) (Org, error)` | — | — | — |
| Org find-or-create | `EnsureOrg(slug, fullName, desc, vis) (Org, error)` | — | — | — |
| Repo get | `GetRepo(owner, name) (Repo, error)` | — | — | — |
| Repo create | `CreateRepo(org, name, desc, private, autoInit, defaultBranch) (Repo, error)` | — | — | — |
| Repo find-or-create | `EnsureRepo(org, name, desc, private) (Repo, error)` | — | `EnsureRepo(org, repo) error` | `EnsureRepo(org, repo) error` |
| File get | `GetFile(owner, repo, path, branch) (FileContent, []byte, error)` | `GetFile(org, repo, branch, path) (*FileContent, error)` | `GetFile(org, repo, branch, path) (*FileResponse, error)` | `GetFile(org, repo, branch, path) (*FileResponse, error)` |
| File create-or-update | `PutFile(owner, repo, path, branch, data, msg) (FileContent, error)` | `UpsertFile(org, repo, branch, path, data, msg, name, email) (committed bool, error)` | `PutFile(org, repo, branch, path, data, msg) (sha string, error)` | `PutFile(org, repo, branch, path, data, msg) (sha string, committed bool, error)` |
| File delete | — | — | `DeleteFile(org, repo, branch, path, msg) (bool, error)` | `DeleteFile(org, repo, branch, path, msg) (bool, error)` |
| Branch find-or-create | — | — | — | `EnsureBranch(org, repo, branch) error` |

### Pain points

1. **`EnsureRepo` collides on signature.** C1 returns `Repo` + accepts
   description/private; C3+C4 return `error` and accept just `(org, repo)`.
2. **`PutFile` argument order varies** — C1 has `(path, branch, ...)`
   while C2/C3/C4 have `(branch, path, ...)`.
3. **`PutFile` returns differ** — C1 returns `FileContent`, C3 returns
   `string`, C4 returns `(string, bool, error)`, C2 calls it `UpsertFile`
   and returns `(bool, error)`.
4. **`GetFile` returns differ** — C1 returns `(FileContent, []byte, error)`
   (decoded blob); the others return base64-encoded.
5. **`BaseURL` semantics differ** — env client expects URL ending in
   `/api/v1`; the others hardcode `/api/v1` in endpoint paths.
6. **Error sentinels differ** — C2/C3/C4 use `HTTPError` + `IsNotFound`;
   C1 uses typed sentinel errors (`ErrOrgNotFound`, `ErrRepoNotFound`,
   `ErrFileNotFound`).

## 2. The SUPERSET surface (output)

The shared `core/controllers/pkg/gitea/Client` exposes the
**union** of every operation currently in use. Naming separates Org/Repo
CRUD from File CRUD so call sites read obviously.

```go
// Client wraps the Gitea Admin REST API.
type Client struct {
    BaseURL   string         // e.g. "https://gitea.hfmp.openova.io" — /api/v1 is appended internally
    Token     string         // admin personal-access token
    HTTP      *http.Client   // tests inject httptest server.Client()
    UserAgent string         // emitted on every request
}

func New(baseURL, token string) *Client            // 30s timeout default
func NewWithHTTP(baseURL, token string, hc *http.Client) *Client

// --- Org + Repo CRUD ------------------------------------------------

type Org struct {
    ID          int64  `json:"id,omitempty"`
    Username    string `json:"username,omitempty"`
    FullName    string `json:"full_name,omitempty"`
    Description string `json:"description,omitempty"`
    Visibility  string `json:"visibility,omitempty"`
}

type Repo struct {
    ID            int64  `json:"id,omitempty"`
    Name          string `json:"name,omitempty"`
    FullName      string `json:"full_name,omitempty"`
    Description   string `json:"description,omitempty"`
    Private       bool   `json:"private,omitempty"`
    DefaultBranch string `json:"default_branch,omitempty"`
}

func (c *Client) GetOrg(ctx context.Context, slug string) (Org, error)
func (c *Client) CreateOrg(ctx context.Context, slug, fullName, description, visibility string) (Org, error)
func (c *Client) EnsureOrg(ctx context.Context, slug, fullName, description, visibility string) (Org, error)

func (c *Client) GetRepo(ctx context.Context, owner, name string) (Repo, error)
func (c *Client) CreateRepo(ctx context.Context, org, name, description string, private, autoInit bool, defaultBranch string) (Repo, error)
// EnsureRepo: SUPERSET of C1 + C3+C4 — returns the Repo for callers
// that want it; auto_init=true and private flag are configurable to
// support both catalog (public + auto-init) and per-Org (private +
// auto-init) repo shapes. C3+C4 callers discard the Repo via `_`.
func (c *Client) EnsureRepo(ctx context.Context, org, name, description string, private bool) (Repo, error)

// EnsureBranch: branches off main if absent. Idempotent on both 409
// and 422.
func (c *Client) EnsureBranch(ctx context.Context, org, repo, branch string) error

// --- File CRUD -----------------------------------------------------

// File is the surface returned by GetFile / PutFile. SHA is the BLOB
// SHA needed by PUT-update; ContentBase64 is preserved to support
// callers that want the raw response body.
type File struct {
    Path          string `json:"path"`
    SHA           string `json:"sha"`
    ContentBase64 string `json:"content"` // base64 from Gitea
    Type          string `json:"type"`    // "file" | "dir" | "symlink" | "submodule"
}

// Decoded returns the file's raw bytes (decoding ContentBase64).
func (f *File) Decoded() ([]byte, error)

// GetFile fetches the file. Returns ErrFileNotFound on 404.
// Returns ErrRepoNotFound when the 404 response body indicates the
// repo itself is missing (C2 needs this distinction).
func (c *Client) GetFile(ctx context.Context, org, repo, branch, path string) (File, error)

// PutFile creates the file if absent, updates it if present, OR
// short-circuits with no API write if the existing content is
// byte-equal to `data` (the canonical idempotency anchor — C1's
// pattern; preserved in CC2). `committed` is true only when the
// controller actually wrote bytes.
//
// The author/email parameters are optional — pass empty strings to
// use Gitea's default committer (the token's owner). C2 passes them
// explicitly for committer-attribution; C1/C3/C4 don't care.
type PutFileOpts struct {
    AuthorName  string
    AuthorEmail string
}
func (c *Client) PutFile(ctx context.Context, org, repo, branch, path string, data []byte, message string, opts ...PutFileOpts) (file File, committed bool, err error)

// DeleteFile removes the file. Idempotent: a 404 from the probe is
// treated as "already absent" — returns (true, nil).
func (c *Client) DeleteFile(ctx context.Context, org, repo, branch, path, message string) (deleted bool, err error)

// --- Errors --------------------------------------------------------

var (
    ErrOrgNotFound  = errors.New("gitea: org not found")
    ErrRepoNotFound = errors.New("gitea: repo not found")
    ErrFileNotFound = errors.New("gitea: file not found")
)

// HTTPError is returned for non-2xx responses that don't map to a
// typed sentinel. Callers can inspect Status for retry decisions.
type HTTPError struct {
    Method, URL string
    Status      int
    Body        string
}
func (e *HTTPError) Error() string

// IsNotFound reports whether err is any of the 404-derived sentinels
// (ErrOrgNotFound, ErrRepoNotFound, ErrFileNotFound) OR a HTTPError
// with Status==404. Preserved for the C4 GiteaErrorClassifier surface.
func IsNotFound(err error) bool

// IsConflict reports whether err is a 409. Preserved for C4
// EnsureRepo's parallel-create race handling.
func IsConflict(err error) bool
```

### Method-by-method winner-pick rationale

| Method | Winner | Rationale |
|---|---|---|
| `GetOrg` | C1 (organization) | Has the cleanest typed-sentinel error path; C2's `*Org` was changed to value-type to match. |
| `CreateOrg` | C1 (organization) | Only C1 implements it. Defaults visibility to "private" preserved. |
| `EnsureOrg` | C1 (organization) | Only C1 implements it. 422/409 race re-find pattern preserved. |
| `GetRepo` | C1 (organization) | Only C1 implements it directly (C3+C4 use raw `do(GET /repos/{org}/{name})`). |
| `CreateRepo` | C1 (organization) | Only C1 has full surface (description/private/autoInit/defaultBranch). |
| `EnsureRepo` | **C1 surface, C4 race-handling** | C1's signature `(org, name, desc, private) (Repo, error)` is the SUPERSET (C3+C4 callers can pass desc + private and discard Repo via `_`). C4's `IsConflict` + `IsNotFound` race handling under create folded in (the parallel-create race on a hot-reconcile loop is real). |
| `EnsureBranch` | C4 (application) | Only C4 implements it. Probe-then-create with 409/422 idempotency preserved. |
| `GetFile` | C2 (environment) | C2 has the repo-vs-file 404 distinction (probe body for "repository") — needed by env-controller. C1's pre-decoded `[]byte` second return is replaced by `File.Decoded()` helper to keep the signature uniform. |
| `PutFile` | **C4 (application) signature, C1 byte-equal short-circuit** | C4's `(file, committed, err)` triple is the most informative; the byte-equal short-circuit (returns `committed=false` with no API write) is the canonical idempotency anchor present in all four. The optional `PutFileOpts` extends to support C2's committer attribution without polluting the common path. |
| `DeleteFile` | C3/C4 (identical) | Idempotent-on-404 pattern, `(deleted, err)` return. |
| `IsNotFound` / `IsConflict` | C4 (application) | The `HTTPError`-based helpers translate to the unified type. Extended to also recognize `ErrOrgNotFound`/`ErrRepoNotFound`/`ErrFileNotFound` so call sites don't care which form the client returned. |

### URL handling

The `BaseURL` parameter is the Gitea root **without** `/api/v1`. The
client prepends `/api/v1` to every endpoint internally. environment-
controller's `cmd/main.go` is updated to drop the trailing `/api/v1`
from `GITEA_API_URL`.

## 3. Per-controller migration

### organization (C1)
- Imports updated: `internal/gitea` → `internal/gitea` (shared)
- `EnsureOrg`, `EnsureRepo` — same surface, no call-site change.
- `PutFile(ctx, org, repo, path, branch, data, msg)` → `PutFile(ctx, org, repo, branch, path, data, msg)` — argument order swap (C1 was the outlier).
- C1 ignored the return of `PutFile` (used `_, err :=`); now it captures `(_, _, err :=)` for the (file, committed, err) triple — committed bool is discarded.
- `gitea.New(url, token)` constructor preserved.

### blueprint (C3)
- Imports updated.
- `EnsureRepo(ctx, org, repo)` → `_, err := EnsureRepo(ctx, org, repo, "Catalyst Blueprint mirror — auto-managed by blueprint-controller. Do not edit manually.", false)` — wrap with the existing description literal that was inlined in the old client; private=false (catalog Org per NAMING §11.2).
- `PutFile(ctx, org, repo, branch, path, ...)` → `_, _, err := PutFile(ctx, ...)` — discard file + committed.
- `DeleteFile` — same.
- `gitea.NewClient(url, token)` → `gitea.New(url, token)`.

### environment (C2)
- Imports updated.
- `GetOrg(ctx, org) (*Org, error)` → `Org, error` (value type). Caller used `org.Username` — uses `org.Username` on value.
- `UpsertFile(ctx, org, repo, branch, path, data, msg, authorName, authorEmail) (committed bool, error)` → `_, committed, err := PutFile(ctx, org, repo, branch, path, data, msg, gitea.PutFileOpts{AuthorName: authorName, AuthorEmail: authorEmail})` — preserves committer attribution via opts.
- The `GiteaClient` interface in the controller updates to match the new surface.
- `gitea.NewClient(url, token)` → `gitea.New(url, token)`. cmd/main.go drops trailing `/api/v1` from GITEA_API_URL.

### application (C4)
- Imports updated.
- `EnsureRepo(ctx, org, repo)` → `_, err := EnsureRepo(ctx, org, repo, "Application manifests — auto-managed by application-controller. Do not edit manually.", true)` — preserves the existing description + private=true.
- `PutFile`, `DeleteFile`, `EnsureBranch` — same shape.
- `gitea.NewClient(url, token)` → `gitea.New(url, token)`.
- The `Gitea` interface in the controller updates to match the new method shape.

## 4. Tests preserved

httptest-based fakes follow the union of patterns from C1 (`gs.handle`),
C3 (`fakeGitea.handler`), and C4 (`fakeGiteaServer.handler`). The
new shared package's `client_test.go` covers:

- `TestEnsureOrg_FindHits` — find-or-create when org pre-exists (1 GET, 0 POST)
- `TestEnsureOrg_CreatesWhenMissing` — 404 → POST
- `TestEnsureOrg_409Race` — 422 on POST → re-find returns existing
- `TestEnsureRepo_FindHits` — find-or-create when repo pre-exists
- `TestEnsureRepo_CreatesWithPrivate` — POST with private=true
- `TestEnsureRepo_OrgMissing` — 404 on POST → ErrOrgNotFound
- `TestEnsureRepo_409Race` — 409 on POST → success (parallel create won)
- `TestEnsureBranch_Main` — main branch is no-op
- `TestEnsureBranch_CreatesDevelop` — branches from main
- `TestEnsureBranch_Idempotent` — 422 already-exists
- `TestGetFile_OK` — 200 returns File with base64 + Decoded() helper
- `TestGetFile_FileNotFound` — 404 → ErrFileNotFound
- `TestGetFile_RepoNotFound` — 404 with "repository" body → ErrRepoNotFound
- `TestPutFile_CreateNew` — POST with no SHA
- `TestPutFile_UpdateExisting` — PUT with existing SHA
- `TestPutFile_ByteEqualNoOp` — identical content → 0 writes
- `TestPutFile_WithAuthor` — opts pass committer through
- `TestDeleteFile_Present` — 404 → idempotent
- `TestDeleteFile_Absent` — already absent → idempotent
- `TestIsNotFound` — recognizes typed sentinels + HTTPError 404
- `TestIsConflict` — recognizes HTTPError 409

## 5. What CC2 explicitly does NOT do

- No new Gitea API methods beyond the union of existing usage.
- No deploy-manifest changes.
- No dep-version bumps.
- No behavior change for any existing call site.

## 6. Promotion to pkg/ (EPIC-2 Slice L, #1097)

EPIC-2 Slice L (catalog-svc, `core/services/catalyst-catalog/`) introduced
the FIRST consumer of this client OUTSIDE the `core/controllers/` Go module.
Catalog-svc is a SERVICE not a CONTROLLER and lives in its own go.mod under
`core/services/catalyst-catalog/`.

Go's `internal/` packaging rule prohibits import from outside the parent
sub-tree (`core/controllers/`). To preserve "one canonical Gitea client,
zero per-service variants," CC2 promoted this package from
`core/controllers/internal/gitea` to `core/controllers/pkg/gitea`.

The 5 Group C controllers were updated atomically in the same PR. No
behavior change — only the import path.

After this promotion, ANY new Go module under the `openova` repo that
needs Gitea access imports `github.com/openova-io/openova/core/controllers/pkg/gitea`
via a `replace` directive (catalog-svc's go.mod uses
`replace github.com/openova-io/openova/core/controllers => ../../controllers`)
or a published-module path once the controllers module is tagged.

The `internal/` → `pkg/` move is the canonical signal that this surface
is a SHARED-LIBRARY contract, not a controllers-private helper.
