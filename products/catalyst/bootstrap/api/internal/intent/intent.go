// Package intent is the write path of catalyst-api for intent: an object
// that a route changes is validated, serialised, committed to Git through
// the commit library gitwrite, and then awaited in the informer cache until
// Flux has applied it. The package writes nothing to the cluster, polls
// nothing and keeps no state of its own. Nothing calls it until the routes
// move onto it, one batch at a time (CTL-162 and the batches after it).
package intent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
)

const (
	// ChangeIDAnnotation carries the change identifier of the commit that
	// last wrote the object's file.
	ChangeIDAnnotation = "catalyst.openova.io/change-id"
	// ManagedByLabel and ManagedByValue mark every intent object as applied by Flux.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "flux"
	// DefaultSyncWait is the wait when Deps.SyncWait is not above zero.
	DefaultSyncWait = 15 * time.Second
)

// The values of Outcome.State: the "state" field of the 202 answer of an
// intent route.
const (
	// StateCommitted: the change is committed to the branch the cluster
	// applies, and the cache did not show it applied within SyncWait.
	StateCommitted = "committed"
	// StateAwaitingApproval: the change is committed to its own branch and
	// waits in a pull request for a person to merge it. Nothing reaches
	// the cluster before that merge.
	StateAwaitingApproval = "awaiting-approval"
)

// Deps is what the package needs; built once in cmd/api.
type Deps struct {
	Git       gitwrite.Writer
	Cache     ObjectCache   // satisfied by *k8scache.Factory through a small adapter in cmd/api
	ClusterID string        // the cache's identifier of the cluster that applies the intent
	FQDN      string        // this Sovereign's FQDN
	SyncWait  time.Duration // CATALYST_INTENT_SYNC_WAIT, default 15s
}

// ObjectCache is the part of the informer cache the package uses. A kind is
// the cache's kind name: the object's kind in lower case.
type ObjectCache interface {
	// Get returns the cached object, or nil when it is absent.
	Get(clusterID, kind, namespace, name string) *unstructured.Unstructured
	// Watch delivers every later add, update and delete of one kind; cancel stops it.
	Watch(clusterID, kind string) (events <-chan CacheEvent, cancel func())
}

// CacheEvent is one change of an object in the cache.
type CacheEvent struct {
	Type   string // "ADDED", "MODIFIED", "DELETED"
	Object *unstructured.Unstructured
}

// Target is one object of a change and the file that holds it.
type Target struct {
	APIVersion, Kind, Namespace, Name string
	Path                              string // repository path
	Op                                gitwrite.Op
}

// Outcome is the result of Submit.
type Outcome struct {
	ChangeID       string
	Commit         string
	Noop           bool
	Applied        bool // every target carried ChangeID (or was gone) within SyncWait
	Targets        []Target
	ETag           string
	State          string // "" when Applied; otherwise StateCommitted or StateAwaitingApproval
	PullRequestURL string // the pull request of a change in gitwrite.ModePullRequest; "" otherwise
}

// Status is the HTTP status of an intent route's answer for o: applied,
// the route's own status, when the change was applied within SyncWait, and
// 202 Accepted otherwise, with o.State as the "state" of the answer.
func (o Outcome) Status(applied int) int {
	if o.State == "" {
		return applied
	}
	return http.StatusAccepted
}

// withState sets State to StateCommitted when the change was not applied.
func withState(o Outcome) Outcome {
	if !o.Applied {
		o.State = StateCommitted
	}
	return o
}

// SchemaError is returned by Submit when an object fails its schema.
// Nothing was committed.
type SchemaError struct {
	Kind   string
	Name   string
	Errors field.ErrorList
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("intent: %s %s fails its schema: %s", e.Kind, e.Name, e.Errors.ToAggregate().Error())
}

// ErrInvalid is wrapped by every refusal of a malformed call to Submit.
var ErrInvalid = errors.New("intent: invalid call")

// Serialize returns the canonical bytes of an intent file: the object
// without status, metadata.managedFields, metadata.resourceVersion,
// metadata.uid, metadata.creationTimestamp, metadata.generation and
// metadata.selfLink; keys sorted; two-space YAML; one trailing newline.
func Serialize(obj *unstructured.Unstructured) ([]byte, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: no object", ErrInvalid)
	}
	c := obj.DeepCopy()
	delete(c.Object, "status")
	if md, ok := c.Object["metadata"].(map[string]interface{}); ok {
		for _, k := range []string{"managedFields", "resourceVersion", "uid", "creationTimestamp", "generation", "selfLink"} {
			delete(md, k)
		}
	}
	out, err := yaml.Marshal(c.Object)
	if err != nil {
		return nil, fmt.Errorf("intent: serialise %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}
	return out, nil
}

// cacheKind is the cache's kind name of an object kind.
func cacheKind(kind string) string {
	return strings.ToLower(kind)
}

// withoutChangeID returns the canonical bytes of obj with the change-id
// annotation removed, and the value it had.
func withoutChangeID(obj *unstructured.Unstructured) ([]byte, string, error) {
	c := obj.DeepCopy()
	ann := c.GetAnnotations()
	id := ann[ChangeIDAnnotation]
	if ann != nil {
		delete(ann, ChangeIDAnnotation)
		if len(ann) == 0 {
			ann = nil
		}
		c.SetAnnotations(ann)
	}
	b, err := Serialize(c)
	return b, id, err
}

// parseFile reads one object from the bytes of an intent file.
func parseFile(content []byte) (*unstructured.Unstructured, bool) {
	m := map[string]interface{}{}
	if err := yaml.Unmarshal(content, &m); err != nil || len(m) == 0 {
		return nil, false
	}
	return &unstructured.Unstructured{Object: m}, true
}

// fileState is the state of one target's file before the change.
type fileState struct {
	exists  bool
	content []byte
	sha     string
}

func readState(ctx context.Context, d Deps, scope gitwrite.Scope, repo gitwrite.Repo, path string) (fileState, error) {
	content, sha, err := d.Git.Read(ctx, scope, repo, path)
	if errors.Is(err, gitwrite.ErrNotFound) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	return fileState{exists: true, content: content, sha: sha}, nil
}

// waitTarget is what Submit waits for: the object carrying wantID, or the
// object being absent when absent is true. wantID "" means present.
type waitTarget struct {
	kind, namespace, name string
	wantID                string
	absent                bool
}

func (w waitTarget) done(d Deps) bool {
	obj := d.Cache.Get(d.ClusterID, w.kind, w.namespace, w.name)
	if w.absent {
		return obj == nil
	}
	if obj == nil {
		return false
	}
	return w.wantID == "" || obj.GetAnnotations()[ChangeIDAnnotation] == w.wantID
}

func allDone(d Deps, ws []waitTarget) bool {
	for _, w := range ws {
		if !w.done(d) {
			return false
		}
	}
	return true
}

// await watches every kind of ws and reports whether every target is done
// before SyncWait passes or ctx ends. It reads the cache once at the start
// and again after each event; it never polls.
func await(ctx context.Context, d Deps, ws []waitTarget) bool {
	wait := d.SyncWait
	if wait <= 0 {
		wait = DefaultSyncWait
	}
	wake := make(chan struct{}, 1)
	stop := make(chan struct{})
	seen := map[string]bool{}
	var cancels []func()
	for _, w := range ws {
		if seen[w.kind] {
			continue
		}
		seen[w.kind] = true
		events, cancel := d.Cache.Watch(d.ClusterID, w.kind)
		cancels = append(cancels, cancel)
		go func() {
			for {
				select {
				case _, ok := <-events:
					if !ok {
						return
					}
					select {
					case wake <- struct{}{}:
					default:
					}
				case <-stop:
					return
				}
			}
		}()
	}
	defer func() {
		close(stop)
		for _, cancel := range cancels {
			cancel()
		}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		if allDone(d, ws) {
			return true
		}
		select {
		case <-wake:
		case <-timer.C:
			return allDone(d, ws)
		case <-ctx.Done():
			return allDone(d, ws)
		}
	}
}

// Submit validates each upsert, sets the label and the change-id
// annotation, detects a no-op (equal to the current file ignoring the
// change-id annotation), commits all files in one commit, and waits up to
// SyncWait for the informer cache. ifMatch applies to the first object
// only: the first upsert, or the first delete when there is no upsert.
// pathOf gives the repository path of each upsert.
//
// mode is optional and given at most once. Without it, or with
// gitwrite.ModeDirect, the change is committed to repo's branch. With
// gitwrite.ModePullRequest it is committed to its own branch and a pull
// request into main is opened: Submit then does not wait, and returns
// State StateAwaitingApproval with the address of the pull request. The
// change reaches the cluster when a person merges the pull request.
func Submit(ctx context.Context, d Deps, scope gitwrite.Scope, repo gitwrite.Repo, upserts []*unstructured.Unstructured, deletes []Target, pathOf func(*unstructured.Unstructured) string, summary string, author gitwrite.Identity, via gitwrite.Via, ifMatch string, trailers map[string]string, mode ...gitwrite.Mode) (Outcome, error) {
	if d.Git == nil || d.Cache == nil {
		return Outcome{}, fmt.Errorf("%w: Deps needs Git and Cache", ErrInvalid)
	}
	var m gitwrite.Mode
	if len(mode) > 1 {
		return Outcome{}, fmt.Errorf("%w: mode is given %d times", ErrInvalid, len(mode))
	}
	if len(mode) == 1 {
		m = mode[0]
	}
	if m != "" && m != gitwrite.ModeDirect && m != gitwrite.ModePullRequest {
		return Outcome{}, fmt.Errorf("%w: mode %q is not direct or pull-request", ErrInvalid, m)
	}
	if len(upserts)+len(deletes) == 0 {
		return Outcome{}, fmt.Errorf("%w: no object to change", ErrInvalid)
	}
	if len(upserts) > 0 && pathOf == nil {
		return Outcome{}, fmt.Errorf("%w: upserts need pathOf", ErrInvalid)
	}
	changeID := gitwrite.NewChangeID()

	type upsert struct {
		obj    *unstructured.Unstructured
		path   string
		bytes  []byte
		bare   []byte // canonical bytes without the change-id annotation
		state  fileState
		fileID string // change-id annotation of the current file
	}
	var ups []upsert
	var targets []Target
	for _, in := range upserts {
		if in == nil {
			return Outcome{}, fmt.Errorf("%w: a nil upsert", ErrInvalid)
		}
		if errs := Validate(in); len(errs) > 0 {
			return Outcome{}, &SchemaError{Kind: in.GetKind(), Name: in.GetName(), Errors: errs}
		}
		obj := in.DeepCopy()
		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[ManagedByLabel] = ManagedByValue
		obj.SetLabels(labels)
		ann := obj.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[ChangeIDAnnotation] = changeID
		obj.SetAnnotations(ann)
		path := pathOf(in)
		if path == "" {
			return Outcome{}, fmt.Errorf("%w: pathOf gave no path for %s %s", ErrInvalid, in.GetKind(), in.GetName())
		}
		b, err := Serialize(obj)
		if err != nil {
			return Outcome{}, err
		}
		bare, _, err := withoutChangeID(obj)
		if err != nil {
			return Outcome{}, err
		}
		ups = append(ups, upsert{obj: obj, path: path, bytes: b, bare: bare})
		targets = append(targets, Target{APIVersion: obj.GetAPIVersion(), Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName(), Path: path, Op: gitwrite.OpUpsert})
	}
	for _, t := range deletes {
		if t.Path == "" || t.Kind == "" || t.Name == "" {
			return Outcome{}, fmt.Errorf("%w: a delete needs Kind, Name and Path", ErrInvalid)
		}
		t.Op = gitwrite.OpDelete
		targets = append(targets, t)
	}

	// Read every current file and detect a no-op.
	noop := true
	for i := range ups {
		st, err := readState(ctx, d, scope, repo, ups[i].path)
		if err != nil {
			return Outcome{}, err
		}
		ups[i].state = st
		if !st.exists {
			noop = false
			continue
		}
		cur, ok := parseFile(st.content)
		if !ok {
			noop = false
			continue
		}
		bare, id, err := withoutChangeID(cur)
		if err != nil || string(bare) != string(ups[i].bare) {
			noop = false
			continue
		}
		ups[i].fileID = id
	}
	delStates := make([]fileState, len(deletes))
	for i, t := range deletes {
		st, err := readState(ctx, d, scope, repo, t.Path)
		if err != nil {
			return Outcome{}, err
		}
		delStates[i] = st
		if st.exists {
			noop = false
		}
	}

	// ifMatch is checked here as well as by the library, because the
	// library reports a no-op before it compares IfMatch.
	if ifMatch != "" {
		first, firstPath := fileState{}, ""
		if len(ups) > 0 {
			first, firstPath = ups[0].state, ups[0].path
		} else {
			first, firstPath = delStates[0], deletes[0].Path
		}
		if (ifMatch == gitwrite.AbsentSHA && first.exists) || (ifMatch != gitwrite.AbsentSHA && ifMatch != first.sha) {
			return Outcome{}, &gitwrite.ConflictError{Path: firstPath, Want: ifMatch, Current: first.sha}
		}
	}

	change := gitwrite.Change{Scope: scope, Repo: repo, Summary: summary, Author: author, ChangeID: changeID, Via: via, Trailers: trailers}
	if noop {
		// Ask the library for the head with the files as they are: each
		// file must still have the SHA just read, so a file that moved in
		// between is a conflict, never overwritten.
		for _, u := range ups {
			change.Files = append(change.Files, gitwrite.FileChange{Path: u.path, Op: gitwrite.OpUpsert, Content: u.state.content, IfMatch: u.state.sha})
		}
		for _, t := range deletes {
			change.Files = append(change.Files, gitwrite.FileChange{Path: t.Path, Op: gitwrite.OpDelete, IfMatch: gitwrite.AbsentSHA})
		}
		res, err := d.Git.Commit(ctx, change)
		if err != nil {
			return Outcome{}, err
		}
		out := Outcome{ChangeID: changeID, Commit: res.Commit, Noop: true, Targets: targets}
		if len(ups) > 0 {
			out.ETag = ups[0].state.sha
			if ups[0].fileID != "" {
				out.ChangeID = ups[0].fileID
			}
		}
		var ws []waitTarget
		for _, u := range ups {
			ws = append(ws, waitTarget{kind: cacheKind(u.obj.GetKind()), namespace: u.obj.GetNamespace(), name: u.obj.GetName(), wantID: u.fileID})
		}
		for _, t := range deletes {
			ws = append(ws, waitTarget{kind: cacheKind(t.Kind), namespace: t.Namespace, name: t.Name, absent: true})
		}
		out.Applied = allDone(d, ws)
		return withState(out), nil
	}

	change.Mode = m
	for i, u := range ups {
		fc := gitwrite.FileChange{Path: u.path, Op: gitwrite.OpUpsert, Content: u.bytes}
		if i == 0 {
			fc.IfMatch = ifMatch
		}
		change.Files = append(change.Files, fc)
	}
	for i, t := range deletes {
		fc := gitwrite.FileChange{Path: t.Path, Op: gitwrite.OpDelete}
		if i == 0 && len(ups) == 0 {
			fc.IfMatch = ifMatch
		}
		change.Files = append(change.Files, fc)
	}
	res, err := d.Git.Commit(ctx, change)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{ChangeID: changeID, Commit: res.Commit, Noop: !res.Committed, Targets: targets}
	if len(res.Files) > 0 {
		out.ETag = res.Files[0].BlobSHA
	}
	if res.PullRequestURL != "" {
		// The change waits in its pull request; nothing reaches the
		// cluster before a person merges it, so Submit does not wait.
		out.State, out.PullRequestURL = StateAwaitingApproval, res.PullRequestURL
		return out, nil
	}
	var ws []waitTarget
	for _, u := range ups {
		ws = append(ws, waitTarget{kind: cacheKind(u.obj.GetKind()), namespace: u.obj.GetNamespace(), name: u.obj.GetName(), wantID: changeID})
	}
	for _, t := range deletes {
		ws = append(ws, waitTarget{kind: cacheKind(t.Kind), namespace: t.Namespace, name: t.Name, absent: true})
	}
	if !res.Committed {
		out.Applied = allDone(d, ws)
		return withState(out), nil
	}
	out.Applied = await(ctx, d, ws)
	return withState(out), nil
}
