// Package instances is the markup-svc fleet discovery surface
// (ADR-0005). The Discovery contract returns the list of instance
// base URLs the deployer talks to for a given env. Static-config
// implementations land in subpackages; Kubernetes-native discovery
// is a separate ADR.
package instances

import (
	"context"
	"errors"
)

// Instance is one markup-svc replica's base URL and the env label
// it serves.
type Instance struct {
	URL string
	Env string
}

// Discovery returns the instances configured for an env. Backings
// must return ErrNoInstances (not an empty slice) when an env has no
// instances mapped — the deployer treats "empty slice" as a partial-
// deploy of zero successes, which is a different failure mode from
// "the env name was unrecognised".
type Discovery interface {
	Instances(ctx context.Context, env string) ([]Instance, error)
}

// ErrNoInstances is returned by Discovery.Instances when an env has
// no markup-svc base URLs configured. Callers (Promote / Rollback)
// treat this as a 400 invalid_env rather than retrying.
var ErrNoInstances = errors.New("instances: no markup-svc base URLs configured for env")
