// Package secretfile reads a secret that the Secrets Store CSI driver mounts as a
// file: one file per value, named by the objectName of the pod's SecretProviderClass,
// under the pod's mount directory (/var/run/secrets/openova).
//
// The first copy lives in core/services/auth (SEC-212); this is the copy of the
// chargeback engine (SEC-246), which reads its master key through it. Each Go module
// that reads a mounted secret holds its own copy under internal/secretfile; there is
// no shared module for these few lines.
package secretfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Dir is the mount directory. Tests may change it.
var Dir = "/var/run/secrets/openova"

// ErrNotMounted means the file of the value does not exist: the volume is not
// mounted, or it does not hold that object.
var ErrNotMounted = errors.New("secretfile: not mounted")

// ErrEmpty means the file exists and holds nothing but line ends.
var ErrEmpty = errors.New("secretfile: empty")

// Read returns the content of Dir/<name> without the line ends at its end. It opens
// the file on every call, so a call after the driver rotated the file returns the new
// value. A name that is not one plain file name is refused.
func Read(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("secretfile: %q is not a file name", name)
	}
	path := filepath.Join(Dir, name)
	b, err := fs.ReadFile(os.DirFS(Dir), name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrNotMounted, path)
	}
	if err != nil {
		return "", fmt.Errorf("secretfile: %w", err)
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("%w: %s", ErrEmpty, path)
	}
	return v, nil
}
