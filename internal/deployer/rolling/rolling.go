// Package rolling implements deployer.Deployer with the rolling-push
// strategy: one instance at a time, post the artifact body to
// /admin/reload, poll /readyz until healthy or --instance-timeout
// fires, advance.
package rolling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

type diagnoseRejectedErr struct {
	details *deployer.DiagnoseDetails
}

func (e *diagnoseRejectedErr) Error() string {
	return "deployer/rolling: rule set failed Diagnose"
}

// Deployer is the rolling-push backing. Construct via New; the HTTP
// client is injected so tests can drive against httptest.Server and
// production runs use a configured http.Client with timeouts the cmd
// shell controls.
type Deployer struct {
	client            *http.Client
	instanceTimeout   time.Duration
	readyzInterval    time.Duration
	challengerRetries int
	challengerBackoff time.Duration
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

// WithChallengerRetries sets the number of additional attempts on
// transient challenger-push / challenger-clear failures (network,
// 5xx). Default 2 (3 total attempts: initial + 2 retries). 0 disables
// retry. Diagnose rejections are NEVER retried — same bytes, same
// rejection.
func WithChallengerRetries(n int) Option { return func(d *Deployer) { d.challengerRetries = n } }

// WithChallengerBackoff sets the base backoff between challenger
// retries. Each retry waits backoff × 4^attempt. Default 100ms gives
// 100ms / 400ms / 1.6s waits.
func WithChallengerBackoff(t time.Duration) Option {
	return func(d *Deployer) { d.challengerBackoff = t }
}

// New constructs a rolling Deployer with the ADR-0005 defaults.
func New(opts ...Option) *Deployer {
	d := &Deployer{
		client:            &http.Client{Timeout: 30 * time.Second},
		instanceTimeout:   10 * time.Second,
		readyzInterval:    200 * time.Millisecond,
		challengerRetries: 2,
		challengerBackoff: 100 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Deploy short-circuits on the first StatusDiagnoseRejected — the
// remaining instances would reject the same bytes the same way.
func (d *Deployer) Deploy(ctx context.Context, targets []instances.Instance, body deployer.Body) (deployer.DeployResult, error) {
	if len(targets) == 0 {
		return deployer.DeployResult{}, deployer.ErrNoTargets
	}
	results := make([]deployer.InstanceResult, 0, len(targets))
	for i, target := range targets {
		res := d.deployOne(ctx, target, body)
		results = append(results, res)
		if res.Status == deployer.StatusDiagnoseRejected {
			for _, skipped := range targets[i+1:] {
				results = append(results, deployer.InstanceResult{
					URL:    skipped.URL,
					Status: deployer.StatusSkipped,
					Error:  "diagnose rejected on upstream instance; skipped",
				})
			}
			break
		}
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
		var dre *diagnoseRejectedErr
		if errors.As(err, &dre) {
			span.SetAttributes(attribute.String("diagnose.verdict", "rejected"))
			span.SetStatus(codes.Error, "diagnose rejected")
			return deployer.InstanceResult{
				URL:             target.URL,
				Status:          deployer.StatusDiagnoseRejected,
				Duration:        time.Since(start),
				Error:           err.Error(),
				DiagnoseDetails: dre.details,
			}
		}
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
	return d.postBody(ctx, target.URL+"/admin/reload", body)
}

func (d *Deployer) postBody(ctx context.Context, url string, body deployer.Body) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body.Bytes))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", body.ContentType)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusBadRequest {
			if details, ok := parseDiagnoseRejection(b); ok {
				return &diagnoseRejectedErr{details: details}
			}
		}
		return fmt.Errorf("%s %s: %s", url, resp.Status, string(b))
	}
	return nil
}

// DeployChallenger pushes the body to each target's
// /admin/load-challenger endpoint. Unlike Deploy it does NOT short-
// circuit on Diagnose rejection (markup-svc's challenger reload uses
// the same Diagnose pipeline as champion reload, but failure on one
// instance does not predict success on another for the metadata-class
// challenger lifecycle — the registry still wants to know which
// instances accepted the push). It also does not poll /readyz: the
// challenger swap is metadata-only on the markup-svc side, no
// rolling-restart equivalent.
func (d *Deployer) DeployChallenger(ctx context.Context, targets []instances.Instance, body deployer.Body) (deployer.DeployResult, error) {
	if len(targets) == 0 {
		return deployer.DeployResult{}, deployer.ErrNoTargets
	}
	results := make([]deployer.InstanceResult, 0, len(targets))
	for _, target := range targets {
		results = append(results, d.challengerPushOne(ctx, target, body))
	}
	return deployer.DeployResult{
		Instances: results,
		Outcome:   deployer.SummariseChallengerOutcome(results),
	}, nil
}

// ClearChallenger sends DELETE /admin/challenger to every target.
// Idempotent on the markup-svc side.
func (d *Deployer) ClearChallenger(ctx context.Context, targets []instances.Instance) (deployer.DeployResult, error) {
	if len(targets) == 0 {
		return deployer.DeployResult{}, deployer.ErrNoTargets
	}
	results := make([]deployer.InstanceResult, 0, len(targets))
	for _, target := range targets {
		results = append(results, d.challengerClearOne(ctx, target))
	}
	return deployer.DeployResult{
		Instances: results,
		Outcome:   deployer.SummariseChallengerOutcome(results),
	}, nil
}

func (d *Deployer) challengerPushOne(ctx context.Context, target instances.Instance, body deployer.Body) deployer.InstanceResult {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "registry.deploy.challenger_push",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("instance.url", target.URL),
			attribute.String("instance.env", target.Env),
		),
	)
	defer span.End()

	start := time.Now()
	var lastErr error
	for attempt := 0; attempt <= d.challengerRetries; attempt++ {
		if attempt > 0 {
			wait := d.challengerBackoff * time.Duration(1<<(2*attempt-2))
			if !sleepCtx(ctx, wait) {
				lastErr = ctx.Err()
				break
			}
		}
		pushCtx, cancel := context.WithTimeout(ctx, d.instanceTimeout)
		err := d.postBody(pushCtx, target.URL+"/admin/load-challenger", body)
		cancel()
		if err == nil {
			if attempt > 0 {
				span.SetAttributes(attribute.Int("retry.attempts", attempt))
			}
			return deployer.InstanceResult{
				URL:      target.URL,
				Status:   deployer.StatusDeployed,
				Duration: time.Since(start),
			}
		}
		var dre *diagnoseRejectedErr
		if errors.As(err, &dre) {
			span.SetAttributes(attribute.String("diagnose.verdict", "rejected"))
			span.SetStatus(codes.Error, "diagnose rejected")
			return deployer.InstanceResult{
				URL:             target.URL,
				Status:          deployer.StatusDiagnoseRejected,
				Duration:        time.Since(start),
				Error:           err.Error(),
				DiagnoseDetails: dre.details,
			}
		}
		lastErr = err
	}
	span.RecordError(lastErr)
	span.SetAttributes(attribute.Int("retry.attempts", d.challengerRetries))
	span.SetStatus(codes.Error, "challenger push failed after retries")
	return deployer.InstanceResult{
		URL:      target.URL,
		Status:   deployer.StatusFailed,
		Duration: time.Since(start),
		Error:    lastErr.Error(),
	}
}

// sleepCtx waits d unless ctx ends first. Returns false on ctx end.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (d *Deployer) challengerClearOne(ctx context.Context, target instances.Instance) deployer.InstanceResult {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "registry.deploy.challenger_clear",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("instance.url", target.URL),
			attribute.String("instance.env", target.Env),
		),
	)
	defer span.End()

	start := time.Now()
	var lastErr error
	for attempt := 0; attempt <= d.challengerRetries; attempt++ {
		if attempt > 0 {
			wait := d.challengerBackoff * time.Duration(1<<(2*attempt-2))
			if !sleepCtx(ctx, wait) {
				lastErr = ctx.Err()
				break
			}
		}
		err := d.singleClearAttempt(ctx, target)
		if err == nil {
			if attempt > 0 {
				span.SetAttributes(attribute.Int("retry.attempts", attempt))
			}
			return deployer.InstanceResult{URL: target.URL, Status: deployer.StatusDeployed, Duration: time.Since(start)}
		}
		lastErr = err
	}
	span.RecordError(lastErr)
	span.SetAttributes(attribute.Int("retry.attempts", d.challengerRetries))
	span.SetStatus(codes.Error, "challenger clear failed after retries")
	return deployer.InstanceResult{URL: target.URL, Status: deployer.StatusFailed, Duration: time.Since(start), Error: lastErr.Error()}
}

func (d *Deployer) singleClearAttempt(parent context.Context, target instances.Instance) error {
	clearCtx, cancel := context.WithTimeout(parent, d.instanceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(clearCtx, http.MethodDelete, target.URL+"/admin/challenger", nil)
	if err != nil {
		return err
	}
	otel.GetTextMapPropagator().Inject(clearCtx, propagation.HeaderCarrier(req.Header))
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DELETE /admin/challenger %s: %s", resp.Status, string(b))
	}
	return nil
}

// reloadRejectBody mirrors markup-svc/ADR-0026's reject envelope. The
// `error` sentinel is the discriminator: any 400 carrying healthy:false
// alone could be a generic envelope from a future markup-svc error
// branch (auth, content-type, body-too-large). markup-svc's Diagnose
// reject always carries the literal "reload rejected: rule set failed
// Diagnose" string.
type reloadRejectBody struct {
	Error    string `json:"error"`
	Healthy  bool   `json:"healthy"`
	Errors   []reloadIssue `json:"errors,omitempty"`
	Warnings []reloadIssue `json:"warnings,omitempty"`
}

type reloadIssue struct {
	Kind   string `json:"kind"`
	Rule   string `json:"rule,omitempty"`
	Detail string `json:"detail"`
}

const diagnoseRejectSentinel = "rule set failed Diagnose"

func parseDiagnoseRejection(body []byte) (*deployer.DiagnoseDetails, bool) {
	var b reloadRejectBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, false
	}
	if b.Healthy || !strings.Contains(b.Error, diagnoseRejectSentinel) {
		return nil, false
	}
	d := &deployer.DiagnoseDetails{
		Healthy:  b.Healthy,
		Errors:   make([]deployer.DiagnoseIssue, 0, len(b.Errors)),
		Warnings: make([]deployer.DiagnoseIssue, 0, len(b.Warnings)),
	}
	for _, e := range b.Errors {
		d.Errors = append(d.Errors, deployer.DiagnoseIssue{Kind: e.Kind, Rule: e.Rule, Detail: e.Detail})
	}
	for _, w := range b.Warnings {
		d.Warnings = append(d.Warnings, deployer.DiagnoseIssue{Kind: w.Kind, Rule: w.Rule, Detail: w.Detail})
	}
	return d, true
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
