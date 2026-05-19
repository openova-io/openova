// export_test_helpers.go — small re-exports used by sibling packages
// in their own tests. These are NOT covered by the production build
// (filename ending in _test.go would scope them to in-package tests
// only, so we name the file plainly and gate the symbol with a
// build-tag-free comment that pins the use site).
//
// In practice the ResetCache symbol is harmless even in production —
// it merely drops a cache that will be re-populated on the next
// Lookup. We keep it exported so test helpers in `server` can reset
// the cache between tests without leaking the internal sync.Mutex.
package agentcatalog

// ResetCache drops the lazily-loaded table cache. Useful in tests
// that want a deterministic builtin+override layering, but safe to
// call at any time in production (next Lookup will re-read).
func ResetCache() { reset() }
