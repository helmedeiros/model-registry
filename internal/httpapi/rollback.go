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

// RollbackDeps is the dependency bundle POST /rollback needs. Same
// shape as PromoteDeps; the two could share a struct, but keeping
// them distinct lets future endpoints (e.g. challenger
// promote/reject) land their own bundles without churning either.
type RollbackDeps struct {
	Artifacts store.Reader
	EnvState  envstate.Store
	Audit     audit.Writer
	Discovery instances.Discovery
	Deployer  deployer.Deployer
	ULID      ULIDSource
	Logger    AccessSink
	Now       func() time.Time
}

// Rollback returns the POST /rollback handler per ADR-0005.
func Rollback(deps RollbackDeps) http.Handler {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	for _, c := range []struct {
		ok   bool
		name string
	}{
		{deps.Artifacts != nil, "Artifacts"},
		{deps.EnvState != nil, "EnvState"},
		{deps.Audit != nil, "Audit"},
		{deps.Discovery != nil, "Discovery"},
		{deps.Deployer != nil, "Deployer"},
		{deps.ULID != nil, "ULID"},
		{deps.Logger != nil, "Logger"},
	} {
		if !c.ok {
			panic("httpapi.Rollback: " + c.name + " is required")
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req RollbackRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body")
			return
		}
		switch {
		case req.Env == "":
			writeError(w, http.StatusBadRequest, "invalid_env")
			return
		case req.Operator == "":
			writeError(w, http.StatusBadRequest, "invalid_operator")
			return
		case req.Reason == "":
			// Reason is mandatory on rollback — the audit trail's value
			// comes from operators having to articulate WHY they
			// reverted, even at 3am.
			writeError(w, http.StatusBadRequest, "reason_required")
			return
		}

		ctx := r.Context()

		preview, err := deps.EnvState.PreviousChampion(ctx, req.Env)
		if errors.Is(err, envstate.ErrNoChampion) || errors.Is(err, envstate.ErrNoPreviousChampion) {
			writeError(w, http.StatusBadRequest, "no_history")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "preview_failed")
			return
		}

		bundle, err := deps.Artifacts.GetBundle(ctx, preview)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "bundle_lookup_failed")
			return
		}
		if bundle.State == store.StateDeprecated {
			writeError(w, http.StatusBadRequest, "hash_deprecated")
			return
		}

		sourceBytes, contentType, err := deps.Artifacts.GetMember(ctx, preview, store.MemberSource)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "member_fetch_failed")
			return
		}

		targets, err := deps.Discovery.Instances(ctx, req.Env)
		if errors.Is(err, instances.ErrNoInstances) {
			writeError(w, http.StatusBadRequest, "invalid_env")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "discovery_failed")
			return
		}

		deployResult, err := deps.Deployer.Deploy(ctx, targets, deployer.Body{
			ContentType: string(contentType),
			Bytes:       sourceBytes,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "deploy_failed")
			return
		}
		view := deployView(deployResult)

		previousState, _ := deps.EnvState.Get(ctx, req.Env)
		previousHash := roleHashOrEmpty(previousState.Champion)

		if deployResult.Outcome == deployer.OutcomeFailed {
			writeJSON(w, http.StatusBadGateway, RollbackResponse{
				Env:          req.Env,
				PreviousHash: previousHash,
				RolledTo:     "",
				Deploy:       view,
			})
			return
		}

		rolledTo, err := deps.EnvState.RollbackChampion(ctx, req.Env, req.Operator, req.Reason)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "envstate_failed")
			return
		}

		// A divergence between the preview and the committed rollback
		// means a concurrent /promote landed between the preview and
		// the commit. The deploy shipped `preview`'s source bytes but
		// state says rolledTo. Log the divergence so the operator can
		// reconcile via a follow-up rollback.
		if rolledTo != preview {
			deps.Logger.Info("registry.rollback.race_detected", map[string]any{
				"env":            req.Env,
				"preview_hash":   string(preview),
				"committed_hash": string(rolledTo),
				"operator":       req.Operator,
			})
		}

		if err := deps.recordRollback(ctx, req, rolledTo); err != nil {
			deps.Logger.Info("registry.audit.write_failed", map[string]any{
				"action":        "rollback",
				"env":           req.Env,
				"artifact_hash": string(rolledTo),
				"operator":      req.Operator,
				"error":         err.Error(),
			})
		}

		if deployResult.Outcome == deployer.OutcomePartial {
			w.Header().Set("X-Partial-Deploy", "true")
		}
		writeJSON(w, http.StatusOK, RollbackResponse{
			Env:          req.Env,
			PreviousHash: previousHash,
			RolledTo:     string(rolledTo),
			Deploy:       view,
		})
	})
}

func (deps RollbackDeps) recordRollback(ctx context.Context, req RollbackRequest, rolledTo store.Hash) error {
	id, err := deps.ULID.New()
	if err != nil {
		return err
	}
	return deps.Audit.Record(ctx, audit.Entry{
		ID:           id,
		Operator:     req.Operator,
		Action:       "rollback",
		Target:       "env/" + req.Env + "/champion",
		ArtifactHash: rolledTo,
		Reason:       req.Reason,
		At:           deps.Now(),
	})
}
