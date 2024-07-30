package memaudit

import (
	"testing"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/audittest"
)

// White-box conformance dispatch — the seeding closure reaches the
// package-private entries slice directly so the seam stays out of the
// production API surface.
func TestConformance(t *testing.T) {
	audittest.RunConformance(t, func(_ *testing.T) (audit.Store, audittest.SeedFunc) {
		s := New()
		seed := func(entries []audit.Entry) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.entries = append(s.entries, entries...)
		}
		return s, seed
	})
}
