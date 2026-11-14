// Package gitwrite is the one way platform code commits intent to Git.
// Its contract is docs/contracts/C4-git-layout-and-commit-library.md,
// part 1. It calls the client in core/controllers/pkg/gitea and nothing
// else, and it is the only package allowed to author files under an
// intent/ path.
//
// A Change is one atomic commit: every file of it is applied in one call
// to Gitea, or none is. Before any network call, Commit checks the fields,
// the paths against the Scope, and refuses every Secret document. The
// package gitwrite/fake gives tests an in-memory Writer with the same
// behaviour.
package gitwrite

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

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

// Op is what a FileChange does to its file.
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

// Via names the surface a change was requested through.
type Via string

const (
	ViaConsole    Via = "console"
	ViaAPI        Via = "api"
	ViaMCP        Via = "mcp"
	ViaEvent      Via = "event"      // a projected database decision
	ViaController Via = "controller" // adoption commits and rendered files
)

// Mode is how Commit delivers a Change.
type Mode string

const (
	// ModeDirect commits to the branch of Repo. Mode "" means the same.
	ModeDirect Mode = "direct"
	// ModePullRequest commits to a new branch ChangeBranch(ChangeID) and
	// opens a pull request from it into main. Nothing is committed to
	// main: a person merges the pull request, or nobody does.
	ModePullRequest Mode = "pull-request"
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
	Mode     Mode              // "" or ModeDirect: commit to Repo's branch; ModePullRequest: see Mode
}

// FileResult is the state of one file after Commit.
type FileResult struct {
	Path    string
	BlobSHA string // "" for a deleted file
}

// Result is the outcome of Commit.
type Result struct {
	Commit         string // commit SHA; the current head when Committed is false
	Committed      bool   // false when every file already had the wanted content
	Files          []FileResult
	PullRequestURL string // the pull request of a ModePullRequest change; "" otherwise
}

// Entry is one entry of a directory listing.
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

// SharedRobot names the identity behind the token the controllers use today (CATALYST_GITEA_TOKEN).
const SharedRobot = "catalyst-shared"

// fleetRobot is the robot of the fleet repository's writer-only intent and of the catalogue.
const fleetRobot = "catalyst-fleet-bot"

// FleetIntent is the scope of the writer-only intent of one Sovereign in
// the fleet repository: robot "catalyst-fleet-bot", prefix
// "clusters/<fqdn>/intent/".
func FleetIntent(sovereignFQDN string) Scope {
	return Scope{Name: "fleet-intent", Robot: fleetRobot, Prefixes: []string{"clusters/" + sovereignFQDN + "/intent/"}}
}

// OrgIntent is the scope of the intent of one Organization in its own
// repository: robot "<slug>-iac-bot", prefix "intent/".
func OrgIntent(slug string) Scope {
	return Scope{Name: "org-intent", Robot: slug + "-iac-bot", Prefixes: []string{"intent/"}}
}

// Catalog is the scope of the catalogue repositories: robot
// "catalyst-fleet-bot", every path.
func Catalog() Scope {
	return Scope{Name: "catalog", Robot: fleetRobot, Prefixes: []string{""}}
}

// FleetRendered is the scope of the files controllers render into the
// region folders of the fleet repository: robot SharedRobot, prefix
// "clusters/<fqdn>/regions/".
func FleetRendered(sovereignFQDN string) Scope {
	return Scope{Name: "fleet-rendered", Robot: SharedRobot, Prefixes: []string{"clusters/" + sovereignFQDN + "/regions/"}}
}

// OrgRendered is the scope of the files controllers render into an
// Organization repository: robot SharedRobot, prefixes "regions/",
// "vcluster/" and "continuum/".
func OrgRendered() Scope {
	return Scope{Name: "org-rendered", Robot: SharedRobot, Prefixes: []string{"regions/", "vcluster/", "continuum/"}}
}

// TokenSource returns the access token of a robot.
type TokenSource interface {
	Token(ctx context.Context, robot string) (string, error)
}

type sharedTokenSource struct {
	value string
}

func (s sharedTokenSource) Token(context.Context, string) (string, error) {
	return s.value, nil
}

// SharedTokenSource returns the same token for every robot. With it, the committer
// of every commit is SharedRobot whatever Scope.Robot says. Used until CTL-126.
func SharedTokenSource(token string) TokenSource {
	return sharedTokenSource{value: token}
}

// Config configures a Client.
type Config struct {
	BaseURL     string        // Gitea base URL, in-cluster; required
	Tokens      TokenSource   // required
	RobotDomain string        // e-mail domain of robot identities; required; "robots.<sovereign-fqdn>"
	HTTPTimeout time.Duration // default 15s
	MaxRetries  int           // default 3; used only when no FileChange has IfMatch set
}

// Client is the Writer that talks to Gitea.
type Client struct {
	cfg    Config
	hc     *http.Client
	shared bool
}

var _ Writer = (*Client)(nil)

// New checks cfg and returns a Client. BaseURL, Tokens and RobotDomain are
// required. HTTPTimeout 0 means 15 seconds and MaxRetries 0 means 3; a
// negative value of either is refused. Every refusal wraps ErrInvalid.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" || cfg.Tokens == nil || cfg.RobotDomain == "" {
		return nil, fmt.Errorf("%w: Config needs BaseURL, Tokens and RobotDomain", ErrInvalid)
	}
	if cfg.HTTPTimeout < 0 || cfg.MaxRetries < 0 {
		return nil, fmt.Errorf("%w: HTTPTimeout and MaxRetries must not be negative", ErrInvalid)
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 15 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	_, shared := cfg.Tokens.(sharedTokenSource)
	return &Client{cfg: cfg, hc: &http.Client{Timeout: cfg.HTTPTimeout}, shared: shared}, nil
}

// crockford is the alphabet of a ULID.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewChangeID returns a new 26-character ULID: ten characters of the
// time in milliseconds, then sixteen random characters.
func NewChangeID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic("gitwrite: no random source: " + err.Error())
	}
	var id [26]byte
	ms := time.Now().UnixMilli()
	for i := 9; i >= 0; i-- {
		id[i] = crockford[ms%32]
		ms /= 32
	}
	for i, b := range random {
		id[10+i] = crockford[b&31]
	}
	return string(id[:])
}

// validChangeID reports whether id has the form NewChangeID returns.
func validChangeID(id string) bool {
	if len(id) != 26 || id[0] > '7' {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune(crockford, r) {
			return false
		}
	}
	return true
}

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

func (e *ConflictError) Error() string {
	current := e.Current
	if current == "" {
		current = AbsentSHA
	}
	return fmt.Sprintf("gitwrite: %s changed: want %s, current %s", e.Path, e.Want, current)
}

// IsConflict reports whether err is or wraps a *ConflictError.
func IsConflict(err error) bool {
	var ce *ConflictError
	return errors.As(err, &ce)
}
