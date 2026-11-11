package controller

import (
	"context"
	"errors"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// Repo addresses one Git repository branch.
type Repo struct {
	Owner  string
	Name   string
	Branch string
}

// ErrConflict — a compare-and-swap write lost: the file moved since it was
// read. Not a failure; the next tick re-reads.
var ErrConflict = errors.New("continuum: git compare-and-swap lost")

// Store is the Git surface Continuum uses: read a file with its blob SHA, and
// write it only if the SHA still matches. It is the ONLY write path the
// controller has to the platform (plus status on its own CRs).
type Store interface {
	Read(ctx context.Context, repo Repo, path string) (data []byte, sha string, found bool, err error)
	Write(ctx context.Context, repo Repo, path string, data []byte, sha, message string) error
}

// GiteaStore is Store over the writer region's Gitea.
type GiteaStore struct {
	Client *gitea.Client
}

// Read implements Store.
func (g GiteaStore) Read(ctx context.Context, repo Repo, path string) ([]byte, string, bool, error) {
	f, err := g.Client.GetFile(ctx, repo.Owner, repo.Name, repo.Branch, path)
	if err != nil {
		if errors.Is(err, gitea.ErrFileNotFound) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	data, err := f.Decoded()
	if err != nil {
		return nil, "", false, err
	}
	return data, f.SHA, true, nil
}

// Write implements Store.
func (g GiteaStore) Write(ctx context.Context, repo Repo, path string, data []byte, sha, message string) error {
	_, err := g.Client.PutFileIfMatch(ctx, repo.Owner, repo.Name, repo.Branch, path, data, message, sha)
	if errors.Is(err, gitea.ErrConflict) {
		return ErrConflict
	}
	return err
}
