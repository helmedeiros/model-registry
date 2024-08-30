package rolling_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/deployer/rolling"
	"github.com/helmedeiros/model-registry/internal/instances"
)

func TestDeployChallengerPostsToLoadChallengerEndpoint(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/load-challenger" && r.Method == http.MethodPost {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"rule_count":1,"model_version":"v0"}`))
			return
		}
		http.Error(w, "wrong path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := rolling.New(rolling.WithHTTPClient(srv.Client()))
	result, err := d.DeployChallenger(context.Background(),
		[]instances.Instance{{URL: srv.URL, Env: "production"}},
		deployer.Body{Bytes: []byte("alpha,a,1.0,1\n"), ContentType: "text/csv"})
	if err != nil {
		t.Fatalf("DeployChallenger: %v", err)
	}
	if result.Outcome != deployer.OutcomeOK {
		t.Fatalf("outcome=%s want ok; result=%+v", result.Outcome, result)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("/admin/load-challenger hits=%d want 1", hits)
	}
}

func TestDeployChallengerSurfacesDiagnoseRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"reload rejected: rule set failed Diagnose","healthy":false,"errors":[{"kind":"invalid_factor","rule":"r1","detail":"factor is negative"}]}`))
	}))
	defer srv.Close()

	d := rolling.New(rolling.WithHTTPClient(srv.Client()))
	result, _ := d.DeployChallenger(context.Background(),
		[]instances.Instance{{URL: srv.URL, Env: "production"}},
		deployer.Body{Bytes: []byte("x"), ContentType: "text/csv"})
	if result.Outcome != deployer.OutcomeDiagnoseRejected {
		t.Fatalf("outcome=%s want diagnose_rejected", result.Outcome)
	}
	if len(result.Instances) != 1 || result.Instances[0].Status != deployer.StatusDiagnoseRejected {
		t.Fatalf("instance status: %+v", result.Instances)
	}
}

func TestClearChallengerSendsDELETE(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/challenger" && r.Method == http.MethodDelete {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "wrong path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := rolling.New(rolling.WithHTTPClient(srv.Client()))
	result, err := d.ClearChallenger(context.Background(),
		[]instances.Instance{{URL: srv.URL, Env: "production"}})
	if err != nil {
		t.Fatalf("ClearChallenger: %v", err)
	}
	if result.Outcome != deployer.OutcomeOK {
		t.Fatalf("outcome=%s want ok", result.Outcome)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("DELETE hits=%d want 1", hits)
	}
}

func TestDeployChallengerMixedFleetReportsPartial(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"rule_count":1,"model_version":"v0"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"reload rejected: rule set failed Diagnose","healthy":false,"errors":[{"kind":"invalid_factor"}]}`))
	}))
	defer srv.Close()
	d := rolling.New(rolling.WithHTTPClient(srv.Client()))
	result, _ := d.DeployChallenger(context.Background(),
		[]instances.Instance{
			{URL: srv.URL, Env: "production"},
			{URL: srv.URL, Env: "production"},
		},
		deployer.Body{Bytes: []byte("x"), ContentType: "text/csv"})
	if result.Outcome != deployer.OutcomePartial {
		t.Fatalf("outcome=%s want partial; instances=%+v", result.Outcome, result.Instances)
	}
}

func TestClearChallengerSurfaces5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	d := rolling.New(rolling.WithHTTPClient(srv.Client()))
	result, _ := d.ClearChallenger(context.Background(),
		[]instances.Instance{{URL: srv.URL, Env: "production"}})
	if result.Outcome != deployer.OutcomeFailed {
		t.Fatalf("outcome=%s want failed", result.Outcome)
	}
}
