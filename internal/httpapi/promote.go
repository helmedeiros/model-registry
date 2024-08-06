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
			writeError(w, http.StatusBadRequest, "invalid_body")
			return
		}
		if reason := validatePromote(req); reason != "" {
			writeError(w, http.StatusBadRequest, reason)
			return
		}

		switch req.Role {
		case rolePromoteChampion:
			deps.runChampionPromote(r.Context(), w, req)
		case rolePromoteChallenger:
			writeError(w, http.StatusNotImplemented, "challenger_not_implemented")
		default:
			writeError(w, http.StatusBadRequest, "invalid_role")
		}
	})
}

func (deps PromoteDeps) runChampionPromote(ctx context.Context, w http.ResponseWriter, req PromoteRequest) {
	hash := store.Hash(req.Hash)
	bundle, err := deps.Artifacts.GetBundle(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "hash_unknown")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bundle_lookup_failed")
		return
	}
	if bundle.State == store.StateDeprecated {
		writeError(w, http.StatusBadRequest, "hash_deprecated")
		return
	}

	sourceBytes, contentType, err := deps.Artifacts.GetMember(ctx, hash, store.MemberSource)
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

	// Partial-deploy commits state per ADR-0005; full failure does
	// NOT — the upload survives but the promotion does not.
	if deployResult.Outcome == deployer.OutcomeFailed {
		previous, _ := deps.EnvState.Get(ctx, req.Env)
		writeJSON(w, http.StatusBadGateway, PromoteResponse{
			Env:          req.Env,
			PreviousHash: roleHashOrEmpty(previous.Champion),
			NewHash:      req.Hash,
			Deploy:       view,
		})
		return
	}

	previousHash, err := deps.EnvState.PromoteChampion(ctx, req.Env, hash, req.Operator, req.Reason)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "envstate_failed")
		return
	}

	if err := deps.recordPromote(ctx, req, hash); err != nil {
		deps.Logger.Info("registry.audit.write_failed", map[string]any{
			"action":        "promote",
			"env":           req.Env,
			"artifact_hash": req.Hash,
			"operator":      req.Operator,
			"error":         err.Error(),
		})
	}

	if deployResult.Outcome == deployer.OutcomePartial {
		w.Header().Set("X-Partial-Deploy", "true")
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
