// Package deployer is the typed contract for pushing artifact bodies
// to running markup-svc instances. The Deployer surface separates the
// HTTP handlers from the chosen strategy; alternative strategies
// (blue/green, canary) satisfy the same contract.
package deployer

import (
	"context"
	"errors"
	"time"

	"github.com/helmedeiros/model-registry/internal/instances"
)

// Body is the artifact bytes + the content type the deployer sets on
// the POST /admin/reload call (markup-svc/ADR-0030).
type Body struct {
	ContentType string
	Bytes       []byte
}

// Status enumerates per-instance deploy outcomes.
type Status string

const (
	StatusDeployed Status = "deployed"
	StatusFailed   Status = "failed"
	StatusSkipped  Status = "skipped"
)

// Outcome enumerates aggregate deploy outcomes per ADR-0005's
// {ok, partial, failed} rules:
//   - ok: every instance reports StatusDeployed.
//   - partial: at least one StatusDeployed and at least one StatusFailed.
//   - failed: zero StatusDeployed.
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomePartial Outcome = "partial"
	OutcomeFailed  Outcome = "failed"
)

// InstanceResult records one instance's deploy attempt.
type InstanceResult struct {
	URL      string
	Status   Status
	Duration time.Duration
	Error    string
}

// DeployResult is what Deploy returns. Outcome summarises the
// per-instance Statuses according to the OutcomeOK/Partial/Failed
// rules above.
type DeployResult struct {
	Instances []InstanceResult
	Outcome   Outcome
}

// Deployer is the strategy seam. Deploy returns DeployResult for any
// reachable instances (with per-instance Outcome) and a non-nil error
// only for infrastructure failures the caller cannot inspect via the
// result envelope (context cancellation, no instances configured).
// Callers branch on both: error for "could not run the deploy at
// all", DeployResult for "ran; here is what happened per instance".
type Deployer interface {
	Deploy(ctx context.Context, targets []instances.Instance, body Body) (DeployResult, error)
}

// ErrNoTargets is returned by Deploy when targets is empty. The
// caller (HTTP handler) translates this to a 400 invalid_env without
// retrying — there is no fleet to push to.
var ErrNoTargets = errors.New("deployer: no targets to deploy to")

// SummariseOutcome maps the per-instance Statuses into the aggregate
// Outcome rules. Exposed so both internal strategies and tests
// produce a uniform result envelope.
func SummariseOutcome(results []InstanceResult) Outcome {
	if len(results) == 0 {
		return OutcomeFailed
	}
	deployed := 0
	for _, r := range results {
		if r.Status == StatusDeployed {
			deployed++
		}
	}
	switch {
	case deployed == len(results):
		return OutcomeOK
	case deployed == 0:
		return OutcomeFailed
	default:
		return OutcomePartial
	}
}
