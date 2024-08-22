// Package rolling implements deployer.Deployer with the rolling-push
// strategy: one instance at a time, post the artifact body to
// /admin/reload, poll /readyz until healthy or --instance-timeout
// fires, advance.
package rolling

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/instances"
)

// tracerName is the OTel Tracer name the rolling deployer registers
// against the global TracerProvider. Operators see spans whose
// instrumentation scope is this string in Jaeger.
const tracerName = "github.com/helmedeiros/model-registry/internal/deployer/rolling"

// ErrReadyzTimeout is returned by Deploy's per-instance result when
// the readyz poll did not see a 200 inside the configured per-instance
// timeout. Exposed as a wrapped sentinel so tests and metrics labels
// can branch on the cause without parsing the human-readable string.
var ErrReadyzTimeout = errors.New("deployer/rolling: readyz timed out")

// Deployer is the rolling-push backing. Construct via New; the HTTP
// client is injected so tests can drive against httptest.Server and
// production runs use a configured http.Client with timeouts the cmd
// shell controls.
type Deployer struct {
	client          *http.Client
	instanceTimeout time.Duration
	readyzInterval  time.Duration
}

// Option configures a Deployer at construction.
type Option func(*Deployer)

// WithHTTPClient overrides the default *http.Client.
func WithHTTPClient(c *http.Client) Option { return func(d *Deployer) { d.client = c } }

// WithInstanceTimeout caps the per-instance deploy wall-clock (reload
// + /readyz poll). Default 10s per ADR-0005.
func WithInstanceTimeout(t time.Duration) Option {
	return func(d *Deployer) { d.instanceTimeout = t }
}

// WithReadyzInterval is the gap between /readyz polls. Default 200ms.
func WithReadyzInterval(t time.Duration) Option {
	return func(d *Deployer) { d.readyzInterval = t }
}

// New constructs a rolling Deployer with the ADR-0005 defaults.
func New(opts ...Option) *Deployer {
	d := &Deployer{
		client:          &http.Client{Timeout: 30 * time.Second},
		instanceTimeout: 10 * time.Second,
		readyzInterval:  200 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Deploy implements deployer.Deployer.
func (d *Deployer) Deploy(ctx context.Context, targets []instances.Instance, body deployer.Body) (deployer.DeployResult, error) {
	if len(targets) == 0 {
		return deployer.DeployResult{}, deployer.ErrNoTargets
	}
	results := make([]deployer.InstanceResult, 0, len(targets))
	for _, target := range targets {
		results = append(results, d.deployOne(ctx, target, body))
	}
	return deployer.DeployResult{
		Instances: results,
		Outcome:   deployer.SummariseOutcome(results),
	}, nil
}

func (d *Deployer) deployOne(ctx context.Context, target instances.Instance, body deployer.Body) deployer.InstanceResult {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "registry.deploy.push_to_instance",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("instance.url", target.URL),
			attribute.String("instance.env", target.Env),
		),
	)
	defer span.End()

	start := time.Now()
	deployCtx, cancel := context.WithTimeout(ctx, d.instanceTimeout)
	defer cancel()

	if err := d.postReload(deployCtx, target, body); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "post_reload failed")
		return deployer.InstanceResult{
			URL:      target.URL,
			Status:   deployer.StatusFailed,
			Duration: time.Since(start),
			Error:    err.Error(),
		}
	}
	if err := d.waitReadyz(deployCtx, target); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "readyz failed")
		return deployer.InstanceResult{
			URL:      target.URL,
			Status:   deployer.StatusFailed,
			Duration: time.Since(start),
			Error:    err.Error(),
		}
	}
	span.SetAttributes(attribute.Float64("deploy.duration_ms", float64(time.Since(start).Milliseconds())))
	return deployer.InstanceResult{
		URL:      target.URL,
		Status:   deployer.StatusDeployed,
		Duration: time.Since(start),
	}
}

func (d *Deployer) postReload(ctx context.Context, target instances.Instance, body deployer.Body) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL+"/admin/reload", bytes.NewReader(body.Bytes))
	if err != nil {
		return fmt.Errorf("build reload request: %w", err)
	}
	req.Header.Set("Content-Type", body.ContentType)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("post /admin/reload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("/admin/reload %s: %s", resp.Status, string(b))
	}
	return nil
}

func (d *Deployer) waitReadyz(ctx context.Context, target instances.Instance) error {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "registry.deploy.readyz",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(attribute.String("instance.url", target.URL)),
	)
	defer span.End()

	prop := otel.GetTextMapPropagator()
	ticker := time.NewTicker(d.readyzInterval)
	defer ticker.Stop()
	polls := 0
	for {
		if err := ctx.Err(); err != nil {
			span.SetAttributes(attribute.Int("readyz.polls", polls))
			span.SetStatus(codes.Error, "readyz timeout")
			return fmt.Errorf("waitReadyz: %w", ErrReadyzTimeout)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL+"/readyz", nil)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "build request")
			return fmt.Errorf("build readyz request: %w", err)
		}
		prop.Inject(ctx, propagation.HeaderCarrier(req.Header))
		polls++
		resp, err := d.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				span.SetAttributes(attribute.Int("readyz.polls", polls))
				return nil
			}
		}
		select {
		case <-ctx.Done():
			span.SetAttributes(attribute.Int("readyz.polls", polls))
			span.SetStatus(codes.Error, "readyz timeout")
			return fmt.Errorf("waitReadyz: %w", ErrReadyzTimeout)
		case <-ticker.C:
		}
	}
}
