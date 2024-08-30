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

type RejectDeps struct {
	EnvState  envstate.Store
	Audit     audit.Writer
	ULID      ULIDSource
	Logger    AccessSink
	Now       func() time.Time
	Metrics   RejectMetrics
	// Discovery + Deployer are optional. When both are set, /reject
	// fans out DELETE /admin/challenger to each target after the
	// envstate clear + audit. Push failure does NOT roll back the
	// envstate clear; the operator sees the partial outcome via the
	// response Deploy block + the per-outcome metric label.
	Discovery instances.Discovery
	Deployer  deployer.Deployer
}

type RejectMetrics interface {
	RecordReject(env, outcome string)
}

func Reject(deps RejectDeps) http.Handler {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Metrics == nil {
		deps.Metrics = noopMetrics{}
	}
	for _, c := range []struct {
		ok   bool
		name string
	}{
		{deps.EnvState != nil, "EnvState"},
		{deps.Audit != nil, "Audit"},
		{deps.ULID != nil, "ULID"},
		{deps.Logger != nil, "Logger"},
	} {
		if !c.ok {
			panic("httpapi.Reject: " + c.name + " is required")
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req RejectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			deps.Metrics.RecordReject("", "invalid")
			writeError(w, http.StatusBadRequest, "invalid_body")
			return
		}
		switch {
		case req.Env == "":
			deps.Metrics.RecordReject("", "invalid_env")
			writeError(w, http.StatusBadRequest, "invalid_env")
			return
		case req.Operator == "":
			deps.Metrics.RecordReject(req.Env, "invalid_operator")
			writeError(w, http.StatusBadRequest, "invalid_operator")
			return
		case req.Reason == "":
			deps.Metrics.RecordReject(req.Env, "reason_required")
			writeError(w, http.StatusBadRequest, "reason_required")
			return
		}

		ctx := r.Context()
		previousState, _ := deps.EnvState.Get(ctx, req.Env)
		previousHash := roleHashOrEmpty(previousState.Challenger)

		err := deps.EnvState.RejectChallenger(ctx, req.Env, req.Operator, req.Reason)
		if errors.Is(err, envstate.ErrNoChallenger) {
			deps.Metrics.RecordReject(req.Env, "no_challenger")
			writeError(w, http.StatusBadRequest, "no_challenger")
			return
		}
		if err != nil {
			deps.Metrics.RecordReject(req.Env, "envstate_error")
			writeError(w, http.StatusInternalServerError, "envstate_failed")
			return
		}

		if err := deps.recordReject(ctx, req); err != nil {
			logInfoWithTrace(deps.Logger, ctx, "registry.audit.write_failed", map[string]any{
				"action":   "reject_challenger",
				"env":      req.Env,
				"operator": req.Operator,
				"error":    err.Error(),
			})
		}

		clearResult, outcome := deps.fanOutClear(ctx, req.Env)
		deps.Metrics.RecordReject(req.Env, outcome)
		writeJSON(w, http.StatusOK, RejectResponse{
			Env:          req.Env,
			RejectedHash: previousHash,
			Deploy:       deployView(clearResult),
		})
	})
}

func (deps RejectDeps) fanOutClear(ctx context.Context, env string) (deployer.DeployResult, string) {
	if deps.Discovery == nil || deps.Deployer == nil {
		return deployer.DeployResult{}, "ok"
	}
	targets, err := deps.Discovery.Instances(ctx, env)
	if err != nil {
		logInfoWithTrace(deps.Logger, ctx, "registry.reject.discovery_failed", map[string]any{
			"env":   env,
			"error": err.Error(),
		})
		return deployer.DeployResult{Outcome: deployer.OutcomeFailed}, "discovery_error"
	}
	res, _ := deps.Deployer.ClearChallenger(ctx, targets)
	switch res.Outcome {
	case deployer.OutcomeOK:
		return res, "ok"
	case deployer.OutcomePartial:
		return res, "challenger_partial"
	default:
		return res, "challenger_failed"
	}
}

func (deps RejectDeps) recordReject(ctx context.Context, req RejectRequest) error {
	id, err := deps.ULID.New()
	if err != nil {
		return err
	}
	return deps.Audit.Record(ctx, audit.Entry{
		ID:           id,
		Operator:     req.Operator,
		Action:       "reject_challenger",
		Target:       "env/" + req.Env + "/challenger",
		ArtifactHash: store.Hash(roleHashOrEmpty(challengerFor(ctx, deps, req.Env))),
		Reason:       req.Reason,
		At:           deps.Now(),
		TraceID:      traceIDFromCtx(ctx),
	})
}

func challengerFor(ctx context.Context, deps RejectDeps, env string) *envstate.Role {
	st, err := deps.EnvState.Get(ctx, env)
	if err != nil {
		return nil
	}
	return st.Challenger
}
