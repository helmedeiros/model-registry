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
	StatusDeployed         Status = "deployed"
	StatusFailed           Status = "failed"
	StatusSkipped          Status = "skipped"
	StatusDiagnoseRejected Status = "diagnose_rejected"
)

// Outcome aggregates the per-instance Statuses per ADR-0005/0006.
type Outcome string

const (
	OutcomeOK               Outcome = "ok"
	OutcomePartial          Outcome = "partial"
	OutcomeFailed           Outcome = "failed"
	OutcomeDiagnoseRejected Outcome = "diagnose_rejected"
)

// InstanceResult records one instance's deploy attempt.
// DiagnoseDetails is non-nil only when Status == StatusDiagnoseRejected.
type InstanceResult struct {
	URL             string
	Status          Status
	Duration        time.Duration
	Error           string
	DiagnoseDetails *DiagnoseDetails
}

// DiagnoseDetails is the rule-level verdict surfaced when a deploy is
// rejected for Diagnose (markup-svc/ADR-0026). The port carries the
// domain shape; adapters own wire-format marshalling.
type DiagnoseDetails struct {
	Healthy  bool
	Errors   []DiagnoseIssue
	Warnings []DiagnoseIssue
}

type DiagnoseIssue struct {
	Kind   string
	Rule   string
	Detail string
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
	// DeployChallenger pushes the body to each target's
	// /admin/load-challenger endpoint. Failure semantics differ from
	// Deploy: the challenger is metadata-class; a push failure is
	// surfaced via the result envelope but does NOT roll back the
	// registry-side envstate write that motivated the push.
	DeployChallenger(ctx context.Context, targets []instances.Instance, body Body) (DeployResult, error)
	// ClearChallenger sends DELETE /admin/challenger to each target.
	// Idempotent — markup-svc returns 204 whether or not a challenger
	// was loaded. Result envelope reports per-instance outcomes the
	// same way Deploy does.
	ClearChallenger(ctx context.Context, targets []instances.Instance) (DeployResult, error)
}

// ErrNoTargets is returned by Deploy when targets is empty. The
// caller (HTTP handler) translates this to a 400 invalid_env without
// retrying — there is no fleet to push to.
var ErrNoTargets = errors.New("deployer: no targets to deploy to")

// SummariseChallengerOutcome aggregates per-instance Statuses for the
// challenger fan-out (DeployChallenger / ClearChallenger). Unlike
// SummariseOutcome it does NOT make StatusDiagnoseRejected sticky: a
// mixed fleet (some instances accepted, one rejected on Diagnose)
// reports OutcomePartial so the operator sees the partial-acceptance
// signal. See ADR-0012.
func SummariseChallengerOutcome(results []InstanceResult) Outcome {
	if len(results) == 0 {
		return OutcomeFailed
	}
	var deployed, diagnose int
	for _, r := range results {
		switch r.Status {
		case StatusDeployed:
			deployed++
		case StatusDiagnoseRejected:
			diagnose++
		}
	}
	switch {
	case deployed == len(results):
		return OutcomeOK
	case diagnose == len(results):
		return OutcomeDiagnoseRejected
	case deployed > 0:
		return OutcomePartial
	default:
		return OutcomeFailed
	}
}

// SummariseOutcome maps per-instance Statuses to the aggregate Outcome.
// StatusDiagnoseRejected is sticky because a Diagnose verdict applies
// to the rule set, not the instance.
func SummariseOutcome(results []InstanceResult) Outcome {
	if len(results) == 0 {
		return OutcomeFailed
	}
	for _, r := range results {
		if r.Status == StatusDiagnoseRejected {
			return OutcomeDiagnoseRejected
		}
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
