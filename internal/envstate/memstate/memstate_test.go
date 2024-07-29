package memstate

import (
	"testing"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/envstate/envstatetest"
)

// White-box conformance dispatch: the seeding closure reaches the
// package-private maps directly, so the seam stays inside the
// backing's own package rather than being exported on *Store. Future
// backings (fsstate) follow the same pattern from their own
// package-internal test file.
func TestConformance(t *testing.T) {
	envstatetest.RunConformance(t, func(_ *testing.T) (envstate.Store, envstatetest.SeedFunc) {
		s := New()
		seed := func(state envstate.State, history []envstate.Transition) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if state.Env != "" {
				s.state[state.Env] = state
			}
			for _, tr := range history {
				s.history[tr.Env] = append(s.history[tr.Env], tr)
			}
		}
		return s, seed
	})
}
