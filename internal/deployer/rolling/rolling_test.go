package rolling_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"time"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/deployer/rolling"
	"github.com/helmedeiros/model-registry/internal/instances"
)

func TestDeployRollsAllHealthyTargetsAsOK(t *testing.T) {
	srv1 := newMarkupSvcStub(t, alwaysReady)
	srv2 := newMarkupSvcStub(t, alwaysReady)
	defer srv1.Close()
	defer srv2.Close()

	d := rolling.New(
		rolling.WithHTTPClient(srv1.Client()),
		rolling.WithReadyzInterval(time.Millisecond),
	)
	targets := []instances.Instance{
		{URL: srv1.URL, Env: "production"},
		{URL: srv2.URL, Env: "production"},
	}
	res, err := d.Deploy(context.Background(), targets, deployer.Body{
		ContentType: "text/csv",
		Bytes:       []byte("alpha,rule,1.0,1\n"),
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if res.Outcome != deployer.OutcomeOK {
		t.Fatalf("outcome=%v want OutcomeOK", res.Outcome)
	}
	for _, ir := range res.Instances {
		if ir.Status != deployer.StatusDeployed {
			t.Fatalf("instance status=%v want StatusDeployed (%+v)", ir.Status, ir)
		}
		if ir.Duration <= 0 {
			t.Fatalf("duration must be positive: %+v", ir)
		}
	}
}

func TestDeployMixedHealthYieldsPartial(t *testing.T) {
	good := newMarkupSvcStub(t, alwaysReady)
	bad := newMarkupSvcStub(t, alwaysReloadError)
	defer good.Close()
	defer bad.Close()

	d := rolling.New(
		rolling.WithHTTPClient(good.Client()),
		rolling.WithReadyzInterval(time.Millisecond),
	)
	targets := []instances.Instance{
		{URL: good.URL, Env: "production"},
		{URL: bad.URL, Env: "production"},
	}
	res, _ := d.Deploy(context.Background(), targets, deployer.Body{ContentType: "text/csv", Bytes: []byte("x")})
	if res.Outcome != deployer.OutcomePartial {
		t.Fatalf("outcome=%v want OutcomePartial: %+v", res.Outcome, res)
	}
	if res.Instances[0].Status != deployer.StatusDeployed {
		t.Fatalf("good first: %+v", res.Instances[0])
	}
	if res.Instances[1].Status != deployer.StatusFailed || res.Instances[1].Error == "" {
		t.Fatalf("bad second: %+v", res.Instances[1])
	}
}

func TestDeployAllFailedYieldsFailed(t *testing.T) {
	bad := newMarkupSvcStub(t, alwaysReloadError)
	defer bad.Close()

	d := rolling.New(rolling.WithHTTPClient(bad.Client()), rolling.WithReadyzInterval(time.Millisecond))
	targets := []instances.Instance{{URL: bad.URL, Env: "production"}}
	res, _ := d.Deploy(context.Background(), targets, deployer.Body{ContentType: "text/csv", Bytes: []byte("x")})
	if res.Outcome != deployer.OutcomeFailed {
		t.Fatalf("outcome=%v want OutcomeFailed", res.Outcome)
	}
}

func TestDeployEmptyTargetsReturnsErrNoTargets(t *testing.T) {
	d := rolling.New()
	_, err := d.Deploy(context.Background(), nil, deployer.Body{ContentType: "text/csv", Bytes: []byte("x")})
	if !errors.Is(err, deployer.ErrNoTargets) {
		t.Fatalf("err=%v want ErrNoTargets", err)
	}
}

func TestDeployTimesOutOnSlowReadyz(t *testing.T) {
	srv := newMarkupSvcStub(t, neverReady)
	defer srv.Close()

	d := rolling.New(
		rolling.WithHTTPClient(srv.Client()),
		rolling.WithInstanceTimeout(50*time.Millisecond),
		rolling.WithReadyzInterval(5*time.Millisecond),
	)
	targets := []instances.Instance{{URL: srv.URL, Env: "production"}}
	res, _ := d.Deploy(context.Background(), targets, deployer.Body{ContentType: "text/csv", Bytes: []byte("x")})
	if res.Outcome != deployer.OutcomeFailed {
		t.Fatalf("outcome=%v want OutcomeFailed", res.Outcome)
	}
	if !strings.Contains(res.Instances[0].Error, rolling.ErrReadyzTimeout.Error()) {
		t.Fatalf("error did not surface timeout sentinel: %+v", res.Instances[0])
	}
}

func TestDeployInjectsTraceparent(t *testing.T) {
	prev := otelapi.GetTextMapPropagator()
	otelapi.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otelapi.SetTextMapPropagator(prev) })

	var sawTraceparent atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("traceparent") != "" {
			sawTraceparent.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := rolling.New(rolling.WithHTTPClient(srv.Client()), rolling.WithReadyzInterval(time.Millisecond))
	ctx := injectKnownTrace(t)
	_, err := d.Deploy(ctx, []instances.Instance{{URL: srv.URL}}, deployer.Body{ContentType: "text/csv", Bytes: []byte("x")})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !sawTraceparent.Load() {
		t.Fatal("traceparent header never reached the server")
	}
}

// TestDeployEmitsPushAndReadyzChildSpans asserts every successful
// instance deploy records the registry.deploy.push_to_instance +
// registry.deploy.readyz spans under the caller's trace so operators
// can see per-instance + per-phase decomposition in Jaeger.
func TestDeployEmitsPushAndReadyzChildSpans(t *testing.T) {
	prev := otelapi.GetTextMapPropagator()
	otelapi.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otelapi.SetTextMapPropagator(prev) })

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	prevTP := otelapi.GetTracerProvider()
	otelapi.SetTracerProvider(tp)
	t.Cleanup(func() { otelapi.SetTracerProvider(prevTP) })

	srv := newMarkupSvcStub(t, alwaysReady)
	defer srv.Close()
	d := rolling.New(rolling.WithHTTPClient(srv.Client()), rolling.WithReadyzInterval(time.Millisecond))

	ctx, parent := tp.Tracer("rolling-test").Start(context.Background(), "operator.promote")
	defer parent.End()

	_, err := d.Deploy(ctx, []instances.Instance{
		{URL: srv.URL, Env: "production"},
		{URL: srv.URL, Env: "production"},
	}, deployer.Body{ContentType: "text/csv", Bytes: []byte("x")})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	parent.End()
	wantTrace := parent.SpanContext().TraceID().String()
	pushCount, readyzCount := 0, 0
	for _, s := range rec.Ended() {
		if s.SpanContext().TraceID().String() != wantTrace {
			continue
		}
		switch s.Name() {
		case "registry.deploy.push_to_instance":
			pushCount++
		case "registry.deploy.readyz":
			readyzCount++
		}
	}
	if pushCount != 2 || readyzCount != 2 {
		t.Fatalf("spans: push=%d readyz=%d want both 2 (one per instance)", pushCount, readyzCount)
	}
}

func TestDeployDiagnoseRejectedShortCircuits(t *testing.T) {
	rejectBody := `{"error":"reload rejected: rule set failed Diagnose","healthy":false,"errors":[{"kind":"duplicate_name","rule":"dup","detail":"twice"}]}`
	// Discriminator sanity: the same 400 body without the sentinel
	// should NOT be classified as Diagnose-rejected (covered by a
	// distinct test below).
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/admin/reload" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(rejectBody))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := rolling.New(rolling.WithHTTPClient(srv.Client()), rolling.WithReadyzInterval(time.Millisecond))
	targets := []instances.Instance{{URL: srv.URL}, {URL: srv.URL}, {URL: srv.URL}}
	got, err := d.Deploy(context.Background(), targets, deployer.Body{ContentType: "text/csv", Bytes: []byte("x")})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if got.Outcome != deployer.OutcomeDiagnoseRejected {
		t.Fatalf("outcome=%q want %q", got.Outcome, deployer.OutcomeDiagnoseRejected)
	}
	if len(got.Instances) != 3 {
		t.Fatalf("instances=%d want 3 (1 rejected + 2 skipped)", len(got.Instances))
	}
	if got.Instances[0].Status != deployer.StatusDiagnoseRejected {
		t.Fatalf("instances[0].Status=%q want %q", got.Instances[0].Status, deployer.StatusDiagnoseRejected)
	}
	if got.Instances[0].DiagnoseDetails == nil || got.Instances[0].DiagnoseDetails.Healthy {
		t.Fatalf("DiagnoseDetails missing or healthy: %+v", got.Instances[0].DiagnoseDetails)
	}
	if got.Instances[1].Status != deployer.StatusSkipped || got.Instances[2].Status != deployer.StatusSkipped {
		t.Fatalf("subsequent instances not Skipped: %+v %+v", got.Instances[1], got.Instances[2])
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits=%d want 1 (short-circuit)", hits.Load())
	}
}

func TestDeployGeneric400WithoutSentinelStillFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"auth required","healthy":false}`))
	}))
	defer srv.Close()
	d := rolling.New(rolling.WithHTTPClient(srv.Client()), rolling.WithReadyzInterval(time.Millisecond))
	got, err := d.Deploy(context.Background(), []instances.Instance{{URL: srv.URL}}, deployer.Body{ContentType: "text/csv", Bytes: []byte("x")})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if got.Outcome != deployer.OutcomeFailed {
		t.Fatalf("outcome=%q want failed (no Diagnose sentinel = not a Diagnose rejection)", got.Outcome)
	}
	if got.Instances[0].Status != deployer.StatusFailed {
		t.Fatalf("status=%q want failed", got.Instances[0].Status)
	}
}

// --- helpers ---

type behavior struct {
	reloadStatus int
	readyzStatus int
}

var (
	alwaysReady        = behavior{reloadStatus: http.StatusOK, readyzStatus: http.StatusOK}
	alwaysReloadError  = behavior{reloadStatus: http.StatusInternalServerError, readyzStatus: http.StatusOK}
	neverReady         = behavior{reloadStatus: http.StatusOK, readyzStatus: http.StatusServiceUnavailable}
)

func newMarkupSvcStub(t *testing.T, b behavior) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/reload":
			w.WriteHeader(b.reloadStatus)
		case "/readyz":
			w.WriteHeader(b.readyzStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func injectKnownTrace(t *testing.T) context.Context {
	t.Helper()
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("rolling-test").Start(context.Background(), "cli.root")
	t.Cleanup(func() { span.End() })
	return ctx
}
