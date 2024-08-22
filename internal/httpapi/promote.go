package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/instances"
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

		switch req.Role {
		case rolePromoteChampion:
			deps.runChampionPromote(r.Context(), w, req)
		case rolePromoteChallenger:
			deps.Metrics.RecordPromotion(req.Env, req.Role, "challenger_not_implemented")
			writeError(w, http.StatusNotImplemented, "challenger_not_implemented")
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
	deps.Metrics.ObserveDeployDuration(deps.Now().Sub(deployStart))
	for _, ir := range deployResult.Instances {
		deps.Metrics.RecordDeploy(string(ir.Status))
	}
	view := deployView(deployResult)

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
		deps.Logger.Info("registry.audit.write_failed", map[string]any{
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

func (deps PromoteDeps) recordPromote(ctx context.Context, req PromoteRequest, hash store.Hash) error {
	id, err := deps.ULID.New()
	if err != nil {
		return err
	}
	return deps.Audit.Record(ctx, audit.Entry{
		ID:           id,
		Operator:     req.Operator,
		Action:       "promote",
		Target:       "env/" + req.Env + "/champion",
		ArtifactHash: hash,
		Reason:       req.Reason,
		At:           deps.Now(),
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
