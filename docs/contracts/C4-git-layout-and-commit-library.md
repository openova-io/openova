# Contract C4: Git layout and commit library

Part 1 is the Go package `gitwrite`, the one way platform code commits intent to Git.
Part 2 is where intent lives in the platform's Git repositories. The work packages named
in parentheses build what they name. A change to this contract is a pull request of the
control-plane area (CTL), or of the package that a line below names.

## 1. Go package `gitwrite`

Import path `github.com/openova-io/openova/core/controllers/pkg/gitwrite` (CTL-110). It
calls the existing client `core/controllers/pkg/gitea` and nothing else. It is the only
package allowed to author files under an `intent/` path.

```go
package gitwrite

// Repo names one repository. Branch "" means "main".
type Repo struct {
	Owner  string
	Name   string
	Branch string
}

// Identity is a Git author or committer.
type Identity struct {
	Name  string
	Email string
}

type Op string

const (
	OpUpsert Op = "upsert" // create the file or replace its content
	OpDelete Op = "delete" // delete the file; deleting an absent file is not an error
)

// AbsentSHA as IfMatch means "the file must not exist".
const AbsentSHA = "absent"

// FileChange is one file in a commit.
type FileChange struct {
	Path    string // repository-relative, no leading slash, no ".." element
	Op      Op
	Content []byte // ignored for OpDelete
	IfMatch string // "" no precondition; AbsentSHA; or the blob SHA the file must have
}

type Via string

const (
	ViaConsole    Via = "console"
	ViaAPI        Via = "api"
	ViaMCP        Via = "mcp"
	ViaEvent      Via = "event"      // a projected database decision
	ViaController Via = "controller" // adoption commits and rendered files
)

// Mode is how Commit delivers a Change (CTL-700).
type Mode string

const (
	ModeDirect      Mode = "direct"       // commit to the branch of Repo; "" means the same
	ModePullRequest Mode = "pull-request" // commit to ChangeBranch(ChangeID), open a pull request into main
)

// Change is one atomic commit.
type Change struct {
	Scope    Scope             // selects the robot and the allowed paths; required
	Repo     Repo              // required
	Files    []FileChange      // 1..200; paths unique
	Summary  string            // first line of the message, 1..72 characters, required
	Author   Identity          // the person; zero value means the robot is also the author
	ChangeID string            // required; from NewChangeID
	Via      Via               // required
	Trailers map[string]string // extra trailers; keys match ^[A-Z][A-Za-z-]*$; default nil
	Mode     Mode              // "" or ModeDirect: commit to Repo's branch; ModePullRequest: semantics 11
}

type FileResult struct {
	Path    string
	BlobSHA string // "" for a deleted file
}

type Result struct {
	Commit         string // commit SHA; the current head when Committed is false
	Committed      bool   // false when every file already had the wanted content
	Files          []FileResult
	PullRequestURL string // the pull request of a ModePullRequest change; "" otherwise
}

type Entry struct {
	Path    string
	BlobSHA string
	IsDir   bool
}

// Writer is what handlers, controllers and consumers depend on.
type Writer interface {
	// Commit applies all files in one commit or none.
	Commit(ctx context.Context, c Change) (Result, error)
	// Read returns a file and its blob SHA. A missing file returns ErrNotFound.
	Read(ctx context.Context, s Scope, r Repo, path string) (content []byte, blobSHA string, err error)
	// List returns the entries directly under dir. A missing dir returns an empty slice.
	List(ctx context.Context, s Scope, r Repo, dir string) ([]Entry, error)
}

// Scope binds a robot identity to the path prefixes it may write.
type Scope struct {
	Name     string   // "fleet-intent", "org-intent", "catalog", "fleet-rendered", "org-rendered"
	Robot    string   // Gitea user whose token is used and who is the committer
	Prefixes []string // a Change may only touch paths under one of these; "" allows every path
}

func FleetIntent(sovereignFQDN string) Scope   // robot "catalyst-fleet-bot", prefix "clusters/<fqdn>/intent/"
func OrgIntent(slug string) Scope              // robot "<slug>-iac-bot",     prefix "intent/"
func Catalog() Scope                           // robot "catalyst-fleet-bot", prefix ""
func FleetRendered(sovereignFQDN string) Scope // robot SharedRobot, prefix "clusters/<fqdn>/regions/"  (first used by CTL-260)
func OrgRendered() Scope                       // robot SharedRobot, prefixes "regions/", "vcluster/", "continuum/"  (first used by CTL-260)

// SharedRobot names the identity behind the token the controllers use today (CATALYST_GITEA_TOKEN).
const SharedRobot = "catalyst-shared"

// TokenSource returns the access token of a robot.
type TokenSource interface {
	Token(ctx context.Context, robot string) (string, error)
}

// SharedTokenSource returns the same token for every robot. With it, the committer
// of every commit is SharedRobot whatever Scope.Robot says. Used until CTL-126.
func SharedTokenSource(token string) TokenSource

type Config struct {
	BaseURL     string        // Gitea base URL, in-cluster; required
	Tokens      TokenSource   // required
	RobotDomain string        // e-mail domain of robot identities; required; "robots.<sovereign-fqdn>"
	HTTPTimeout time.Duration // default 15s
	MaxRetries  int           // default 3; used only when no FileChange has IfMatch set
}

func New(cfg Config) (*Client, error) // *Client implements Writer
func NewChangeID() string             // 26-character ULID
func ChangeBranch(changeID string) string // "change/" + changeID: the branch of a ModePullRequest change

var (
	ErrNotFound      = errors.New("gitwrite: not found")
	ErrForbiddenPath = errors.New("gitwrite: path outside the scope")
	ErrSecretContent = errors.New("gitwrite: content holds a Secret object")
	ErrInvalid       = errors.New("gitwrite: invalid change")
	ErrUnavailable   = errors.New("gitwrite: git server unavailable")
)

// ConflictError is returned when an IfMatch precondition fails.
type ConflictError struct {
	Path    string
	Want    string // the IfMatch value
	Current string // current blob SHA, "" if the file is absent
}

func (e *ConflictError) Error() string
func IsConflict(err error) bool
```

Package `gitwrite/fake` provides `fake.New() *fake.Writer`, an in-memory `Writer` with the
same semantics, for tests.

Region scopes (CTL-112): `RegionPlatform(fqdn, modules, regionIDs)` has robot
`catalyst-fleet-bot` and allows only `clusters/<fqdn>/regions/<listed region>/platform/<listed module>/**`;
an empty list refuses every commit. `Regions` lists the region folders. `WriteAllRegions`
writes the same files into every listed region folder in one commit, with compare-and-swap
on the file of the first region in sorted order.

Semantics:

1. **Atomic.** One call to `POST /api/v1/repos/{owner}/{repo}/contents` with a `files` array (`operation` `create`, `update` or `delete`; `path`; base64 `content`; `sha`). The library reads each file's current blob SHA first to choose `create` or `update`. If the spike CTL-050 shows that this call is not all-or-nothing, the library uses the Git data sequence of `commitOnceGitData` in `core/services/provisioning/github/client.go` behind the same interface.
2. **No-op.** If every upsert equals the current content byte for byte and every delete targets an absent file, no commit is made and `Committed` is false.
3. **Compare-and-swap.** A non-empty `IfMatch` is checked against the blob SHA read in item 1 and is also sent as `sha`. A mismatch at either point returns `*ConflictError`. With no `IfMatch` on any file, a server-side mismatch is retried up to `MaxRetries` times from item 1.
4. **Path policy.** A path outside `Scope.Prefixes`, or with a `..` element, returns `ErrForbiddenPath` before any network call.
5. **No secrets.** Each upsert whose path ends in `.yaml`, `.yml` or `.json` is parsed as a stream of documents. A document with `apiVersion: v1` and `kind: Secret` returns `ErrSecretContent`, whatever fields it has. A file that does not parse is committed as it is (the caller validated it).
6. **Field checks.** A missing required field, an empty `Files`, more than 200 files, a duplicate path, or a summary outside 1 to 72 characters returns `ErrInvalid`.
7. **Message.** Exactly the lines below; the extra trailers are sorted by key, one per line.
8. **Identity.** `author` is `Change.Author` when set, otherwise the committer. `committer` is `{Name: <robot>, Email: <robot> + "@" + RobotDomain}`, where `<robot>` is `Scope.Robot`, or `SharedRobot` when the token source is `SharedTokenSource`.
9. **No index files.** No caller writes a `kustomization.yaml` into an `intent/` directory. Flux generates one from the directory content.
10. **Errors from Gitea.** HTTP 5xx, a time-out or a connection error returns `ErrUnavailable` (wrapped). HTTP 401 or 403 returns the Gitea error wrapped, not `ErrUnavailable`.
11. **Pull-request mode** (CTL-700). `Mode` is `""`, `ModeDirect` or `ModePullRequest`; any other value returns `ErrInvalid`. `ModePullRequest` needs the base branch `main` (`Repo.Branch` `""` or `"main"`), or returns `ErrInvalid`. Items 4 to 8 and 10 hold unchanged. The library reads the files on `main`: when every file has the wanted state it returns the head of `main` with `Committed` false and makes no branch and no pull request; an `IfMatch` mismatch returns `*ConflictError` and creates nothing. Otherwise it creates the branch `ChangeBranch(ChangeID)`, that is `change/<ChangeID>`, from `main` (`POST /api/v1/repos/{owner}/{repo}/branches`; an existing branch is kept), commits the files there in one call as in item 1, and opens a pull request from that branch into `main` (`POST /api/v1/repos/{owner}/{repo}/pulls`) whose title is `Summary` and whose body is the commit message of item 7. `Result.Commit` is the commit on the branch and `Result.PullRequestURL` the pull request's address. `main` is never written, and the library does not wait for the merge or read the pull request's state again. A person merges the pull request in Gitea, and the merge reaches the cluster like any commit to `main`. The same `Change` again makes no second commit and returns the open pull request, so a call that failed after its commit can be repeated. In package `intent` of catalyst-api, `Submit` takes the mode as an optional last argument; in this mode it does not wait for the cache and returns `State` `awaiting-approval`, `PullRequestURL`, and `Status` 202.

```
<Summary>

Change-Id: <ChangeID>
Requested-By: <Author.Email, or the robot name if Author is zero>
Requested-Via: <Via>
<extra trailers>
```

Writers that move onto `gitwrite`: `commitApplicationCRToGit` in `application_iac_git.go` (CTL-162); the `git` command-line writer in `marketplace_settings.go` (CTL-220); `internal/giteapr` (CTL-250); the legacy overlay writer in `organization_gitops.go` and `core/services/provisioning/github/client.go` (CTL-207); direct `PutFile` and `DeleteFile` calls in the API's catalogue handlers (CTL-240) and in the controllers (CTL-260).

## 2. Repository layout

The layout of ADR-0015 is unchanged. Paths marked "new" are added. Nothing is moved or renamed.

### 2.1 Fleet repository `openova/sovereign-regions`, branch `main`

```
clusters/<sovereign-fqdn>/
├── intent/                               new. Authored. Only writer: catalyst-api, scope fleet-intent.
│   │                                     Applied in the writer region only. Writer-only intent.
│   ├── intent-root.yaml                  placeholder ConfigMap, never deleted (CTL-120)
│   ├── organizations/<slug>.yaml         one Organization (orgs.openova.io/v1, cluster-scoped)   (CTL-180)
│   ├── access/<name>.yaml                one UserAccess (access.openova.io/v1alpha1)              (CTL-210)
│   ├── access/<plural>/<name>.yaml       reserved: the access module's own kinds
│   ├── environments/<name>.yaml          one Environment (catalyst.openova.io/v1), only if the spike CTL-050 finds a route that creates them
│   ├── settings/platform-values.yaml     ConfigMap catalyst-platform-settings, namespace flux-system  (CTL-220)
│   ├── settings/console-sidebar.yaml     the ConfigMap the sidebar route writes today                  (CTL-222)
│   ├── platform/placement.yaml           ConfigMap platform-placement, namespace flux-system           (CTL-174, which may move it)
│   ├── schedules/<namespace>/<name>.yaml one CronJob per ad-hoc schedule                               (CTL-224)
│   ├── catalog/                          reserved: catalogue choices (spec.catalog of the Sovereign)
│   ├── commerce/                         reserved: the billing module
│   └── migration/                        reserved: the migration module
├── sovereign/                            reserved for the Sovereign-lifecycle area; empty
├── evidence/                             record folder of the compliance area; no Kustomization applies it
├── continuum/transitions/<group>.yaml    exists today: the lease of a topology transition (ADR-0015)
└── regions/<region-id>/                  exists today. Applied by that region's sovereign-region Kustomization.
    ├── orgs/<slug>.yaml                  per-Organization Flux objects, rendered by controllers
    ├── platform/<app>.yaml               platform delivery, rendered by controllers
    ├── platform/<module>/**              every-region intent (section 2.2); modules root, policy, machine-ops
    └── platform/infrastructure/<plural>/<name>.yaml   this region's infrastructure intent (section 2.2)
```

Rules: one object per file, except the two settings files and the placement file. File name equals `metadata.name`. Every object carries the label `app.kubernetes.io/managed-by: flux` and the annotation `catalyst.openova.io/change-id`. A delete is a file delete; the Kustomization prunes. A directory is created by the package named beside it, with its first file; no package creates an empty directory. A module writes `intent/<module>/<plural>/<name>.yaml` through `gitwrite` with scope fleet-intent. The folders `intent/policy/` and `intent/infra/` are released: nothing is written there.

### 2.2 The three places of intent

| Place | Path | Scope | Applied by |
|---|---|---|---|
| Writer-only intent (Organization, Application, catalogue choices) | `clusters/<fqdn>/intent/**` in the fleet repository; `intent/**` in an Organization repository | `FleetIntent(fqdn)`; `OrgIntent(slug)` | `sovereign-intent` in the writer region; `catalyst-tenant-<slug>-intent` in the Organization's home region |
| Every-region intent (policy mode, the compliance model, machine policies) | the same file in every region folder: `clusters/<fqdn>/regions/<region-id>/platform/<module>/**`, modules `root`, `policy`, `machine-ops` | `RegionPlatform(fqdn, modules, every region)` and `WriteAllRegions` | each region's `sovereign-region` Kustomization |
| One region's infrastructure (node pool, virtual machine, network extension) | `clusters/<fqdn>/regions/<region-id>/platform/infrastructure/<plural>/<name>.yaml`; the file carries no region field | `RegionPlatform(fqdn, ["infrastructure"], [region-id])` | that region's `sovereign-region` Kustomization |

Other names map onto the one function: `sovereign-root` is modules `root` and `infrastructure`; `FleetPolicy` is module `policy`; `FleetMachineOps` is module `machine-ops`. The variables document of a Sovereign lives in each region folder under `platform/root/`. The scope of the record folder `evidence/` arrives with CMP-071.

### 2.3 Organization repository `<slug>/catalyst-tenant`, branch `main`

```
intent/                                   new. Authored. Only writer: catalyst-api, scope org-intent.
└── applications/<name>.yaml              one Application (apps.openova.io/v1), namespace <slug>   (CTL-160)
regions/<region-id>/apps/<app>.yaml       exists today; rendered by the application-controller
vcluster/                                 exists today; rendered by the organization-controller
continuum/transitions/<group>.yaml        exists today; written by the Continuum controller
```

`vcluster/apps/` and `vcluster/host-apps/` are written today by the legacy pipelines. They stay until CTL-207 and are then empty.

Serialisation of every intent file: the object without `status`, `metadata.managedFields`, `metadata.resourceVersion`, `metadata.uid`, `metadata.creationTimestamp`, `metadata.generation` and `metadata.selfLink`; keys sorted; two-space YAML; one trailing newline. Two calls with the same object give the same bytes.

### 2.4 Flux objects that read intent

Fleet intent (CTL-120), writer region only, file `clusters/_template/sovereign-region/base/intent-kustomization.yaml`: a Kustomization `sovereign-intent` in `flux-system` with `interval: 10m`, `retryInterval: 1m`, `timeout: 3m`, `path: ./clusters/${SOVEREIGN_FQDN}/intent`, `prune: true`, `wait: false` and source `GitRepository/sovereign-region`. That source gains the ignore line `!/clusters/${SOVEREIGN_FQDN}/intent`. The follower overlay removes the Kustomization with a delete patch. Flux fails on a missing path, so `intent-root.yaml` (a ConfigMap `intent-root` in `flux-system`, no data) is committed with it.

Organization intent (CTL-122), appended by the organization-controller to the file it renders for the Organization's home region, `regions/<home-region-id>/orgs/<slug>.yaml`: a ServiceAccount `org-intent-<slug>` in `flux-system`, and a Kustomization `catalyst-tenant-<slug>-intent` in `flux-system` with `interval: 10m`, `retryInterval: 1m`, `path: ./intent`, `prune: true`, `serviceAccountName: org-intent-<slug>`, `targetNamespace: <slug>`, `dependsOn: catalyst-tenant-<slug>-vcluster` and source `GitRepository/catalyst-tenant-<slug>` (the names `perOrgFluxNames` in `core/controllers/organization/internal/controller/per_org_flux.go` returns). A suspended Organization gets `suspend: true` through `parkIfSuspended`. The controller commits `intent/.keep.yaml` (a ConfigMap `org-intent-root` in `<slug>`, no data) when it first renders the Kustomization. In the `vcluster/` tree a Role and RoleBinding `org-intent` in `<slug>` give the account every verb on `applications.apps.openova.io`, `create` on `configmaps`, and the other verbs on the ConfigMap `org-intent-root` only. A file of any other kind in an Organization's `intent/` fails to apply.

### 2.5 Retired

| Retired | Removed by |
|---|---|
| Repository `<org>/iac`, its bootstrap tree, branch protection and workflow | CTL-162 (writes stop), CTL-250 (creation stops) |
| Repository `sovereign/cluster-config` and the invented pull-request URL | CTL-230 |
| Branch `org-tenants` of `openova/openova` and its Flux source | CTL-208 |
| Endpoint pull-request pipeline | CTL-030 |
| ADR-0009 (pull request per change into `iac`) | CTL-060 |

Catalogue repositories (`catalog-sovereign/<blueprint>`, `<org>/shared-blueprints`, branch `catalog-sovereign`) are owned by the service-model area. Their writers in the API move onto `gitwrite` (CTL-240).
