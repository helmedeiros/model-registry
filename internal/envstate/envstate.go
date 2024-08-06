// Package envstate is the per-env champion + challenger read model
// (ADR-0004). A Reader queries current state and the history of
// transitions; a Writer mutates state. v0.0.3 ships Reader
// implementations only — the Writer contract is fixed so ADR-0005's
// lifecycle endpoints land their implementations on the same typed
// surface without breaking shape.
package envstate

import (
	"context"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

// State is the env-state envelope returned by Reader.Get. An env that
// has never been touched returns the zero value with Env populated —
// dashboards can scrape any env before an operator action without
// having to handle a "not found" branch.
type State struct {
	Env        string
	Champion   *Role
	Challenger *Role
	UpdatedAt  time.Time
}

// Role records one promote/rollback assignment. The hash is the
// substrate artifact the assignment points at; PromotedBy is the
// operator who issued the change; PromotedAt is the wall-clock of the
// transition.
type Role struct {
	Hash        store.Hash
	PromotedBy  string
	PromotedAt  time.Time
}

// Kind is the discriminator on the env-history transition log.
type Kind string

const (
	KindChampionPromoted    Kind = "champion_promoted"
	KindChampionRolledBack  Kind = "champion_rolled_back"
	KindChallengerPromoted  Kind = "challenger_promoted"
	KindChallengerRejected  Kind = "challenger_rejected"
	KindChallengerEvaluated Kind = "challenger_evaluated"
)

// Transition is one row of the env-history log.
type Transition struct {
	Env      string
	Kind     Kind
	FromHash store.Hash
	ToHash   store.Hash
	Operator string
	Reason   string
	At       time.Time
}

// ListOptions paginate history queries. Cursor is exclusive and
// names the last transition returned on the previous page.
type ListOptions struct {
	Limit  int
	Cursor string
}

// HistoryPage is one paginated slice of an env's transition log.
type HistoryPage struct {
	Items      []Transition
	NextCursor string
}

// Reader is the read-only projection used by GET /env/{env}/state and
// /env/{env}/history.
type Reader interface {
	Get(ctx context.Context, env string) (State, error)
	History(ctx context.Context, env string, opts ListOptions) (HistoryPage, error)
}

// Writer mutates env state. Lifecycle endpoints in ADR-0005 supply the
// real implementations on champion methods; challenger methods stay
// stubbed with ErrNotImplemented until ADR-0006.
//
// PromoteChampion returns the hash that was the champion BEFORE the
// promote committed — captured under the same WLock that does the
// write, so a concurrent /promote cannot make the value lie. An empty
// return means the env had no prior champion.
type Writer interface {
	PromoteChampion(ctx context.Context, env string, h store.Hash, operator, reason string) (store.Hash, error)
	RollbackChampion(ctx context.Context, env string, operator, reason string) error
	PromoteChallenger(ctx context.Context, env string, h store.Hash, operator, reason string) error
	RejectChallenger(ctx context.Context, env string, operator, reason string) error
}

// Store is the union for callers that need both projections.
type Store interface {
	Reader
	Writer
}

// Pagination policy. Shared across backings so a future backing
// cannot silently diverge.
const (
	DefaultListLimit = 100
	MaxListLimit     = 1000
)
