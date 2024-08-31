package main

import (
	"bytes"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func TestEvaluateGate_PassWhenAllThresholdsMet(t *testing.T) {
	var stdout, stderr bytes.Buffer
	view := httpapi.ShadowStatsView{
		AgreementRate:    0.995,
		AgreementSamples: 15000,
		FactorDeltaP99:   0.02,
	}
	code := evaluateGate(&stdout, &stderr, view, 0.99, 10000, 0.05)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
}

func TestEvaluateGate_FailsOnLowAgreement(t *testing.T) {
	var stdout, stderr bytes.Buffer
	view := httpapi.ShadowStatsView{
		AgreementRate:    0.95,
		AgreementSamples: 15000,
		FactorDeltaP99:   0.02,
	}
	code := evaluateGate(&stdout, &stderr, view, 0.99, 10000, 0.05)
	if code != 3 {
		t.Fatalf("exit=%d want 3", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("agreement 0.9500 < 0.9900")) {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestEvaluateGate_FailsOnLowSamples(t *testing.T) {
	var stdout, stderr bytes.Buffer
	view := httpapi.ShadowStatsView{
		AgreementRate:    0.999,
		AgreementSamples: 500,
		FactorDeltaP99:   0.01,
	}
	code := evaluateGate(&stdout, &stderr, view, 0.99, 10000, 0.05)
	if code != 3 {
		t.Fatalf("exit=%d want 3", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("samples 500 < 10000")) {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestEvaluateGate_FailsOnLargeFactorDelta(t *testing.T) {
	var stdout, stderr bytes.Buffer
	view := httpapi.ShadowStatsView{
		AgreementRate:    0.999,
		AgreementSamples: 15000,
		FactorDeltaP99:   0.12,
	}
	code := evaluateGate(&stdout, &stderr, view, 0.99, 10000, 0.05)
	if code != 3 {
		t.Fatalf("exit=%d want 3", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("factor_delta_p99 0.1200 > 0.0500")) {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestEvaluateGate_ReportsAllFailures(t *testing.T) {
	var stdout, stderr bytes.Buffer
	view := httpapi.ShadowStatsView{
		AgreementRate:    0.5,
		AgreementSamples: 100,
		FactorDeltaP99:   1.0,
	}
	code := evaluateGate(&stdout, &stderr, view, 0.99, 10000, 0.05)
	if code != 3 {
		t.Fatalf("exit=%d want 3", code)
	}
	s := stderr.String()
	for _, want := range []string{"agreement", "samples", "factor_delta_p99"} {
		if !bytes.Contains([]byte(s), []byte(want)) {
			t.Errorf("stderr missing %q: %s", want, s)
		}
	}
}
