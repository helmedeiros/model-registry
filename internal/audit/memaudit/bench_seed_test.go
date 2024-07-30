package memaudit

import (
	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/audittest"
)

// SeedFromTestPackage exposes the package-private seeding closure to
// the external bench file. The function lives in a *_test.go so it
// does not ship in production binaries; the name signals the
// test-only intent.
func SeedFromTestPackage(s *Store) audittest.SeedFunc {
	return func(entries []audit.Entry) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.entries = append(s.entries, entries...)
	}
}
