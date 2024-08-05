package deployer_test

import (
	"testing"

	"github.com/helmedeiros/model-registry/internal/deployer"
)

func TestSummariseOutcomeEmptyReturnsFailed(t *testing.T) {
	if got := deployer.SummariseOutcome(nil); got != deployer.OutcomeFailed {
		t.Fatalf("got %v want OutcomeFailed", got)
	}
}

func TestSummariseOutcomeAllDeployedReturnsOK(t *testing.T) {
	results := []deployer.InstanceResult{
		{Status: deployer.StatusDeployed},
		{Status: deployer.StatusDeployed},
	}
	if got := deployer.SummariseOutcome(results); got != deployer.OutcomeOK {
		t.Fatalf("got %v want OutcomeOK", got)
	}
}

func TestSummariseOutcomeAllFailedReturnsFailed(t *testing.T) {
	results := []deployer.InstanceResult{
		{Status: deployer.StatusFailed},
		{Status: deployer.StatusFailed},
	}
	if got := deployer.SummariseOutcome(results); got != deployer.OutcomeFailed {
		t.Fatalf("got %v want OutcomeFailed", got)
	}
}

func TestSummariseOutcomeMixedReturnsPartial(t *testing.T) {
	results := []deployer.InstanceResult{
		{Status: deployer.StatusDeployed},
		{Status: deployer.StatusFailed},
	}
	if got := deployer.SummariseOutcome(results); got != deployer.OutcomePartial {
		t.Fatalf("got %v want OutcomePartial", got)
	}
}

func TestSummariseOutcomeAllSkippedReturnsFailed(t *testing.T) {
	// Skipped instances (a future short-circuit case) count as
	// non-deployed for the aggregate; zero StatusDeployed = OutcomeFailed.
	results := []deployer.InstanceResult{
		{Status: deployer.StatusSkipped},
		{Status: deployer.StatusSkipped},
	}
	if got := deployer.SummariseOutcome(results); got != deployer.OutcomeFailed {
		t.Fatalf("got %v want OutcomeFailed", got)
	}
}
