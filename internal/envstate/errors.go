package envstate

import (
	"errors"
	"fmt"
)

// ErrNotImplemented wraps errors.ErrUnsupported so callers can use
// `errors.Is(err, errors.ErrUnsupported)` to detect the missing
// projection without importing this package. ADR-0005 lifecycle
// retains it on Writer slots that are not yet implemented
// (challenger methods until ADR-0006).
var ErrNotImplemented = fmt.Errorf("envstate: writer not implemented: %w", errors.ErrUnsupported)

// ErrEnvRequired is returned by Writer methods when env is empty.
var ErrEnvRequired = errors.New("envstate: env required")

// ErrHashRequired is returned by PromoteChampion / PromoteChallenger
// when the artifact hash is empty.
var ErrHashRequired = errors.New("envstate: hash required")

// ErrOperatorRequired is returned by Writer methods when the operator
// identity is empty — the audit trail loses meaning without it.
var ErrOperatorRequired = errors.New("envstate: operator required")

// ErrNoChampion is returned by RollbackChampion when the env has no
// current champion to roll back from.
var ErrNoChampion = errors.New("envstate: no champion set; nothing to roll back")

// ErrNoPreviousChampion is returned by RollbackChampion when the env
// has a current champion but no prior promotion to restore.
var ErrNoPreviousChampion = errors.New("envstate: no previous champion in history")
