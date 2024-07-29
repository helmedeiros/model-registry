package envstate

import "errors"

var (
	// ErrNotImplemented is returned by v0.0.3 Writer methods on
	// backings that ship the Reader projection only. ADR-0005 lifecycle
	// implementations replace these with the real transitions.
	ErrNotImplemented = errors.New("envstate: writer not implemented in this release")
)
