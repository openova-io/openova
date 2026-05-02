// Package jtistore provides a flat-file-backed, single-use JWT ID (jti) store.
//
// Purpose: the /auth/handover endpoint receives a one-time JWT from
// Catalyst-Zero. Each JWT carries a unique jti (UUID4). The store
// guarantees that every jti can be used at most once — replay attacks
// (re-use of a captured token) return a 401 immediately.
//
// Design constraints:
//   - Must survive Pod restarts: the log file persists on the node PVC.
//   - Append-only file + in-memory hash set: O(1) lookup, O(1) append.
//   - Thread-safe via sync.Mutex.
//   - No external dependencies.
//
// The file format is one jti (UUID) per line followed by '\n'. On first
// Mark() call the existing file (if any) is read into the in-memory set.
// Subsequent Mark() calls append to the file without re-reading.
//
// DefaultPath is `/var/lib/catalyst/jti.log` — the same base directory
// used for the handover public key and the RSA keypair.
package jtistore

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

// DefaultPath is the canonical location of the JTI log file on a Sovereign node.
const DefaultPath = "/var/lib/catalyst/jti.log"

// Store is a flat-file-backed single-use JTI store.
// Zero value is NOT ready for use; call New to obtain an initialised Store.
type Store struct {
	path string
	mu   sync.Mutex
	seen map[string]struct{}
}

// New returns an initialised Store backed by the file at path.
// The file need not exist yet — it is created lazily on the first Mark call.
func New(path string) *Store {
	return &Store{
		path: path,
		seen: nil, // lazy load on first Mark
	}
}

// Mark records jti as consumed.
//
// Returns (true, nil) when the jti is seen for the first time (first use).
// Returns (false, nil) when the jti was already consumed (replay detected).
// Returns (false, err) on I/O error.
//
// On the first call, the existing log file (if present) is read into memory.
func (s *Store) Mark(jti string) (bool, error) {
	if jti == "" {
		return false, fmt.Errorf("jtistore: empty jti")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Lazy load.
	if s.seen == nil {
		if err := s.load(); err != nil {
			return false, err
		}
	}

	if _, exists := s.seen[jti]; exists {
		return false, nil // replay
	}

	// Append to file.
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return false, fmt.Errorf("jtistore: open %s: %w", s.path, err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s\n", jti); err != nil {
		return false, fmt.Errorf("jtistore: write %s: %w", s.path, err)
	}

	s.seen[jti] = struct{}{}
	return true, nil
}

// load reads the existing jti log file into the in-memory set.
// Must be called with s.mu held.
func (s *Store) load() error {
	s.seen = make(map[string]struct{})

	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil // file doesn't exist yet — empty set
	}
	if err != nil {
		return fmt.Errorf("jtistore: open for read %s: %w", s.path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			s.seen[line] = struct{}{}
		}
	}
	return sc.Err()
}
