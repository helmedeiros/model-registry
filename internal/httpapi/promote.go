package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/ratelimit"
	"github.com/helmedeiros/model-registry/internal/store"
)

const (
	rolePromoteChampion   = "champion"
	rolePromoteChallenger = "challenger"
)

// PromoteDeps is the dependency bundle POST /promote needs. The cmd
// shell wires every field; nil values panic at construction so a
// misconfiguration cannot silently degrade a promote.
type PromoteDeps struct {
	Artifacts store.Reader
	EnvState  envstate.Store
	Audit     audit.Writer
	Discovery instances.Discovery
	Deployer  deployer.Deployer
	ULID      ULIDSource
	Logger    AccessSink
	Now       func() time.Time
	Metrics   PromoteMetrics
	// Canary, when non-nil, extends a successful promote with a post-
	// deploy observation window (ADR-0007); nil preserves v0.0.4
	// behaviour.
	Canary CanaryObserver
	// Limiter caps the per-env /promote rate (ADR-0008). Nil = no
	// limit (v0.0.4 behaviour).
	Limiter ratelimit.Limiter
}

// Promote returns the POST /promote handler per ADR-0005.
func Promote(deps PromoteDeps) http.Handler {
	deps = deps.withDefaults()
	deps.mustValidate("Promote")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req PromoteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			deps.Metrics.RecordPromotion("", "", "invalid")
			writeError(w, http.StatusBadRequest, "invalid_body")
			return
		}
		if reason := validatePromote(req); reason != "" {
			deps.Metrics.RecordPromotion(req.Env, req.Role, reason)
			writeError(w, http.StatusBadRequest, reason)
			return
		}

		if deps.Limiter != nil {
			if ok, retry := deps.Limiter.Allow(req.Env); !ok {
				deps.Metrics.RecordPromotion(req.Env, req.Role, "rate_limited")
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
				writeError(w, http.StatusTooManyRequests, "promote_rate_limited")
				return
			}
		}

		switch req.Role {
		case rolePromoteChampion:
			deps.runChampionPromote(r.Context(), w, req)
		case rolePromoteChallenger:
			deps.runChallengerPromote(r.Context(), w, req)
		default:
			deps.Metrics.RecordPromotion(req.Env, req.Role, "invalid_role")
			writeError(w, http.StatusBadRequest, "invalid_role")
		}
	})
}

func (deps PromoteDeps) runChampionPromote(ctx context.Context, w http.ResponseWriter, req PromoteRequest) {
	hash := store.Hash(req.Hash)
	bundle, err := deps.Artifacts.GetBundle(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "hash_unknown")
		writeError(w, http.StatusBadRequest, "hash_unknown")
		return
	}
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "substrate_error")
		writeError(w, http.StatusInternalServerError, "bundle_lookup_failed")
		return
	}
	if bundle.State == store.StateDeprecated {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "hash_deprecated")
		writeError(w, http.StatusBadRequest, "hash_deprecated")
		return
	}

	sourceBytes, contentType, err := deps.Artifacts.GetMember(ctx, hash, store.MemberSource)
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "substrate_error")
		writeError(w, http.StatusInternalServerError, "member_fetch_failed")
		return
	}

	targets, err := deps.Discovery.Instances(ctx, req.Env)
	if errors.Is(err, instances.ErrNoInstances) {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "invalid_env")
		writeError(w, http.StatusBadRequest, "invalid_env")
		return
	}
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "discovery_error")
		writeError(w, http.StatusInternalServerError, "discovery_failed")
		return
	}

	deployStart := deps.Now()
	deployResult, err := deps.Deployer.Deploy(ctx, targets, deployer.Body{
		ContentType: string(contentType),
		Bytes:       sourceBytes,
	})
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "deploy_error")
		writeError(w, http.StatusInternalServerError, "deploy_failed")
		return
	}
	// Observe duration + per-instance counts only on a successful
	// Deploy return — keeps the histogram's count aligned with
	// registry_deploys_total so a future Grafana rate ratio is honest.
	deps.Metrics.ObserveDeployDuration(ctx, deps.Now().Sub(deployStart))
	// On a Diagnose rejection: one instance gets outcome=diagnose_rejected,
	// the rest get outcome=skipped. registry_promotions_total fires once
	// regardless. A panel summing deploys by outcome should sum
	// diagnose_rejected + skipped to recover the fleet size on rejection.
	for _, ir := range deployResult.Instances {
		deps.Metrics.RecordDeploy(string(ir.Status))
	}
	view := deployView(deployResult)

	if deployResult.Outcome == deployer.OutcomeDiagnoseRejected {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "diagnose_rejected")
		auditCtx, auditSpan := startChildSpan(ctx, "registry.audit.record")
		if err := deps.recordPromoteRejected(auditCtx, req, hash); err != nil {
			auditSpan.RecordError(err)
			logInfoWithTrace(deps.Logger, auditCtx, "registry.audit.write_failed", map[string]any{
				"action":        "promote_rejected",
				"env":           req.Env,
				"artifact_hash": req.Hash,
				"operator":      req.Operator,
				"error":         err.Error(),
			})
		}
		auditSpan.End()
		writeJSON(w, http.StatusUnprocessableEntity, PromoteRejectedResponse{
			Env:      req.Env,
			NewHash:  req.Hash,
			Reason:   "diagnose_rejected",
			Diagnose: diagnoseDetailsView(deployResult.Instances),
			Deploy:   view,
		})
		return
	}

	// Partial-deploy commits state per ADR-0005; full failure does
	// NOT — the upload survives but the promotion does not.
	if deployResult.Outcome == deployer.OutcomeFailed {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "failed")
		previous, _ := deps.EnvState.Get(ctx, req.Env)
		writeJSON(w, http.StatusBadGateway, PromoteResponse{
			Env:          req.Env,
			PreviousHash: roleHashOrEmpty(previous.Champion),
			NewHash:      req.Hash,
			Deploy:       view,
		})
		return
	}

	commitCtx, commitSpan := startChildSpan(ctx, "registry.champion.commit_state")
	previousHash, err := deps.EnvState.PromoteChampion(commitCtx, req.Env, hash, req.Operator, req.Reason)
	commitSpan.End()
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "envstate_error")
		writeError(w, http.StatusInternalServerError, "envstate_failed")
		return
	}

	auditCtx, auditSpan := startChildSpan(ctx, "registry.audit.record")
	if err := deps.recordPromote(auditCtx, req, hash); err != nil {
		auditSpan.RecordError(err)
		logInfoWithTrace(deps.Logger, auditCtx, "registry.audit.write_failed", map[string]any{
			"action":        "promote",
			"env":           req.Env,
			"artifact_hash": req.Hash,
			"operator":      req.Operator,
			"error":         err.Error(),
		})
	}
	auditSpan.End()

	if deployResult.Outcome == deployer.OutcomePartial {
		w.Header().Set("X-Partial-Deploy", "true")
		deps.Metrics.RecordPromotion(req.Env, req.Role, "partial")
	} else {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "ok")
	}

	if deps.Canary != nil {
		go deps.Canary.Observe(context.WithoutCancel(ctx), req.Env, req.Hash, req.Operator)
	}

	writeJSON(w, http.StatusOK, PromoteResponse{
		Env:          req.Env,
		PreviousHash: string(previousHash),
		NewHash:      req.Hash,
		Deploy:       view,
	})
}

func roleHashOrEmpty(r *envstate.Role) string {
	if r == nil {
		return ""
	}
	return string(r.Hash)
}

// runChallengerPromote sets the challenger role on the env, records
// the audit entry, and fans the bytes out to markup-svc's
// /admin/load-challenger across all instances configured for the env.
// Push failure does NOT roll back the envstate write — the
// challenger is metadata-class and operators can retry the push
// without re-running envstate semantics. The push result surfaces
// via the response's Deploy block + a per-outcome counter label.
func (deps PromoteDeps) runChallengerPromote(ctx context.Context, w http.ResponseWriter, req PromoteRequest) {
	hash := store.Hash(req.Hash)
	bundle, err := deps.Artifacts.GetBundle(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "hash_unknown")
		writeError(w, http.StatusBadRequest, "hash_unknown")
		return
	}
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "substrate_error")
		writeError(w, http.StatusInternalServerError, "bundle_lookup_failed")
		return
	}
	if bundle.State == store.StateDeprecated {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "hash_deprecated")
		writeError(w, http.StatusBadRequest, "hash_deprecated")
		return
	}

	source, sourceCT, err := deps.Artifacts.GetMember(ctx, hash, store.MemberSource)
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "substrate_error")
		writeError(w, http.StatusInternalServerError, "source_lookup_failed")
		return
	}

	targets, err := deps.Discovery.Instances(ctx, req.Env)
	if errors.Is(err, instances.ErrNoInstances) {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "invalid_env")
		writeError(w, http.StatusBadRequest, "invalid_env")
		return
	}
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "discovery_error")
		writeError(w, http.StatusInternalServerError, "discovery_failed")
		return
	}

	commitCtx, commitSpan := startChildSpan(ctx, "registry.challenger.commit_state")
	err = deps.EnvState.PromoteChallenger(commitCtx, req.Env, hash, req.Operator, req.Reason)
	commitSpan.End()
	if err != nil {
		deps.Metrics.RecordPromotion(req.Env, req.Role, "envstate_error")
		writeError(w, http.StatusInternalServerError, "envstate_failed")
		return
	}

	auditCtx, auditSpan := startChildSpan(ctx, "registry.audit.record")
	if err := deps.recordPromoteAction(auditCtx, req, hash, "promote_challenger", "env/"+req.Env+"/challenger"); err != nil {
		auditSpan.RecordError(err)
		logInfoWithTrace(deps.Logger, auditCtx, "registry.audit.write_failed", map[string]any{
			"action":        "promote_challenger",
			"env":           req.Env,
			"artifact_hash": req.Hash,
			"operator":      req.Operator,
			"error":         err.Error(),
		})
	}
	auditSpan.End()

	pushCtx, pushSpan := startChildSpan(ctx, "registry.challenger.fan_out_push")
	deployResult, _ := deps.Deployer.DeployChallenger(pushCtx, targets, deployer.Body{Bytes: source, ContentType: string(sourceCT)})
	pushSpan.End()

	deps.Metrics.RecordPromotion(req.Env, req.Role, outcomeFromChallengerDeploy(deployResult))
	writeJSON(w, http.StatusOK, PromoteResponse{
		Env:     req.Env,
		NewHash: req.Hash,
		Deploy:  deployView(deployResult),
	})
}

// outcomeFromChallengerDeploy maps the rolling deployer's outcome to
// the registry_promotions_total{outcome} label space. Distinct from
// the champion outcome map because partial / failed do NOT roll back
// envstate for challenger; the operator's audit is "envstate wrote +
// push <outcome>".
func outcomeFromChallengerDeploy(r deployer.DeployResult) string {
	switch r.Outcome {
	case deployer.OutcomeOK:
		return "ok"
	case deployer.OutcomePartial:
		return "challenger_partial"
	case deployer.OutcomeDiagnoseRejected:
		return "diagnose_rejected"
	default:
		return "challenger_failed"
	}
}

func (deps PromoteDeps) recordPromote(ctx context.Context, req PromoteRequest, hash store.Hash) error {
	return deps.recordPromoteAction(ctx, req, hash, "promote", "env/"+req.Env+"/champion")
}

func (deps PromoteDeps) recordPromoteRejected(ctx context.Context, req PromoteRequest, hash store.Hash) error {
	return deps.recordPromoteAction(ctx, req, hash, "promote_rejected", "env/"+req.Env+"/champion")
}

func (deps PromoteDeps) recordPromoteAction(ctx context.Context, req PromoteRequest, hash store.Hash, action, target string) error {
	id, err := deps.ULID.New()
	if err != nil {
		return err
	}
	return deps.Audit.Record(ctx, audit.Entry{
		ID:           id,
		Operator:     req.Operator,
		Action:       action,
		Target:       target,
		ArtifactHash: hash,
		Reason:       req.Reason,
		At:           deps.Now(),
		TraceID:      traceIDFromCtx(ctx),
	})
}

func validatePromote(req PromoteRequest) string {
	switch {
	case req.Hash == "":
		return "invalid_hash"
	case req.Env == "":
		return "invalid_env"
	case req.Role == "":
		return "invalid_role"
	case req.Operator == "":
		return "invalid_operator"
	}
	return ""
}

func deployView(r deployer.DeployResult) DeployView {
	out := DeployView{Outcome: string(r.Outcome)}
	for _, ir := range r.Instances {
		out.Instances = append(out.Instances, InstanceResultView{
			URL:        ir.URL,
			Status:     string(ir.Status),
			DurationMS: ir.Duration.Milliseconds(),
			Error:      ir.Error,
		})
	}
	return out
}

func diagnoseDetailsView(results []deployer.InstanceResult) *DiagnoseDetailsView {
	for _, ir := range results {
		if ir.DiagnoseDetails == nil {
			continue
		}
		d := ir.DiagnoseDetails
		out := &DiagnoseDetailsView{
			Healthy:  d.Healthy,
			Errors:   make([]DiagnoseIssueView, 0, len(d.Errors)),
			Warnings: make([]DiagnoseIssueView, 0, len(d.Warnings)),
		}
		for _, e := range d.Errors {
			out.Errors = append(out.Errors, DiagnoseIssueView{Kind: e.Kind, Rule: e.Rule, Detail: e.Detail})
		}
		for _, w := range d.Warnings {
			out.Warnings = append(out.Warnings, DiagnoseIssueView{Kind: w.Kind, Rule: w.Rule, Detail: w.Detail})
		}
		return out
	}
	return nil
}

func (d PromoteDeps) withDefaults() PromoteDeps {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Metrics == nil {
		d.Metrics = noopMetrics{}
	}
	return d
}

func (d PromoteDeps) mustValidate(name string) {
	for _, c := range []struct {
		ok   bool
		name string
	}{
		{d.Artifacts != nil, "Artifacts"},
		{d.EnvState != nil, "EnvState"},
		{d.Audit != nil, "Audit"},
		{d.Discovery != nil, "Discovery"},
		{d.Deployer != nil, "Deployer"},
		{d.ULID != nil, "ULID"},
		{d.Logger != nil, "Logger"},
	} {
		if !c.ok {
			panic("httpapi." + name + ": " + c.name + " is required")
		}
	}
}
