package envstate

import (
	"errors"
	"fmt"
)

// ErrNotImplemented wraps errors.ErrUnsupported so callers can use
// `errors.Is(err, errors.ErrUnsupported)` to detect the missing
// projection without importing this package. ADR-0005 lifecycle
// implementations replace the stub with real transitions.
var ErrNotImplemented = fmt.Errorf("envstate: writer not implemented: %w", errors.ErrUnsupported)
