// Package intentroot keeps the root file of a Sovereign's fleet intent path
// (contract C4, part 2, section "Flux objects that read intent"; work
// package CTL-120).
//
// The Flux Kustomization sovereign-intent of the writer region applies
// clusters/<fqdn>/intent of the fleet repository, and Flux fails on a
// path that does not exist. So the organization-controller commits one
// placeholder there, clusters/<fqdn>/intent/intent-root.yaml: a ConfigMap
// intent-root in flux-system with no data. It is written once, with
// "must not exist", and never replaced or deleted.
package intentroot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
)

const (
	// Name and Namespace of the placeholder ConfigMap.
	Name      = "intent-root"
	Namespace = "flux-system"
	// FileName is the placeholder's file name under clusters/<fqdn>/intent/.
	FileName = "intent-root.yaml"
	// Summary is the first line of the commit that adds the placeholder.
	Summary = "Add the fleet intent root"
)

// Retry is the backoff Run uses in production: seven attempts, the last
// one about two minutes after the first.
var Retry = wait.Backoff{Duration: 2 * time.Second, Factor: 2, Jitter: 0.1, Steps: 7}

// Path is the repository path of the placeholder of the Sovereign fqdn.
func Path(fqdn string) string {
	return "clusters/" + fqdn + "/intent/" + FileName
}

// Content is the placeholder file: the ConfigMap with the label
// app.kubernetes.io/managed-by=flux and the annotation
// catalyst.openova.io/change-id, keys sorted, two-space YAML, one
// trailing newline.
func Content(changeID string) ([]byte, error) {
	return yaml.Marshal(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":        Name,
			"namespace":   Namespace,
			"labels":      map[string]interface{}{"app.kubernetes.io/managed-by": "flux"},
			"annotations": map[string]interface{}{"catalyst.openova.io/change-id": changeID},
		},
	})
}

// Ensure commits the placeholder to repo with scope fleet-intent and
// IfMatch AbsentSHA. A conflict means the file already exists: that is
// success, and the file is left as it is.
func Ensure(ctx context.Context, w gitwrite.Writer, repo gitwrite.Repo, fqdn string) error {
	if w == nil || fqdn == "" {
		return fmt.Errorf("%w: intentroot needs a writer and the Sovereign FQDN", gitwrite.ErrInvalid)
	}
	id := gitwrite.NewChangeID()
	content, err := Content(id)
	if err != nil {
		return fmt.Errorf("intentroot: serialise %s: %w", Path(fqdn), err)
	}
	_, err = w.Commit(ctx, gitwrite.Change{
		Scope: gitwrite.FleetIntent(fqdn),
		Repo:  repo,
		Files: []gitwrite.FileChange{{
			Path:    Path(fqdn),
			Op:      gitwrite.OpUpsert,
			Content: content,
			IfMatch: gitwrite.AbsentSHA,
		}},
		Summary:  Summary,
		ChangeID: id,
		Via:      gitwrite.ViaController,
	})
	if err == nil || gitwrite.IsConflict(err) {
		return nil
	}
	return fmt.Errorf("intentroot: commit %s: %w", Path(fqdn), err)
}

// Run calls Ensure until it succeeds, with backoff b between attempts. It
// stops at once on an error no retry can cure (an invalid change, a path
// outside the scope, Secret content) and returns it; when b is spent or
// ctx ends it returns the error of the last attempt. Every failed attempt
// is logged.
func Run(ctx context.Context, w gitwrite.Writer, repo gitwrite.Repo, fqdn string, b wait.Backoff, log logr.Logger) error {
	var last error
	err := wait.ExponentialBackoffWithContext(ctx, b, func(ctx context.Context) (bool, error) {
		last = Ensure(ctx, w, repo, fqdn)
		if last == nil {
			return true, nil
		}
		if permanent(last) {
			return false, last
		}
		log.Error(last, "fleet intent root not committed; trying again", "path", Path(fqdn))
		return false, nil
	})
	if err != nil && last != nil {
		return last
	}
	return err
}

// permanent reports whether err cannot be cured by trying again.
func permanent(err error) bool {
	return errors.Is(err, gitwrite.ErrInvalid) || errors.Is(err, gitwrite.ErrForbiddenPath) || errors.Is(err, gitwrite.ErrSecretContent)
}
