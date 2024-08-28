package httpapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/canary"
	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

type fixedDecider struct {
	decision canary.Decision
	obs      canary.Observation
}

func (f fixedDecider) Decide(_ context.Context, _ string) (canary.Decision, canary.Observation, error) {
	return f.decision, f.obs, nil
}

type fakeCanaryMetrics struct {
	mu   sync.Mutex
	hits map[string]int
}

func (f *fakeCanaryMetrics) RecordCanary(env, decision string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	f.hits[env+"/"+decision]++
}

func TestCanarySupervisorRollsBackOnRolledBackDecision(t *testing.T) {
	st := newSeededStore(t,
		[]byte("alpha,rule,1.0,1\n"),
		[]byte("beta,rule,1.0,1\n"),
	)
	h1, h2 := storeHashes(t, st)

	envState := memstate.New()
	ctx := context.Background()
	if _, err := envState.PromoteChampion(ctx, "production", h1, "ci-bot", "seed"); err != nil {
		t.Fatal(err)
	}
	if _, err := envState.PromoteChampion(ctx, "production", h2, "ci-bot", "seed"); err != nil {
		t.Fatal(err)
	}

	disc := stubDiscovery{targets: []instances.Instance{{URL: "http://markup-svc-1:8080", Env: "production"}}}
	deploy := stubDeployer{out: okResult("http://markup-svc-1:8080")}
	au := memaudit.New()
	met := &fakeCanaryMetrics{}
	sup := httpapi.CanarySupervisor{
		Decider:   fixedDecider{decision: canary.DecisionRolledBack, obs: canary.Observation{ErrorRate: 0.05, SampleCount: 1000, Window: 5 * time.Minute, Threshold: 0.01}},
		Artifacts: st,
		EnvState:  envState,
		Discovery: disc,
		Deployer:  deploy,
		Audit:     au,
		ULID:      &stubULID{},
		Logger:    &discardLogger{},
		Metrics:   met,
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}

	sup.Observe(ctx, "production", string(h2), "alice")

	state, _ := envState.Get(ctx, "production")
	if state.Champion == nil || state.Champion.Hash != h1 {
		t.Fatalf("expected rollback to h1, got %+v", state.Champion)
	}
	met.mu.Lock()
	defer met.mu.Unlock()
	if met.hits["production/rolled_back"] != 1 {
		t.Fatalf("canary counter not ticked: %+v", met.hits)
	}

	page, _ := au.List(ctx, audit.ListOptions{Limit: 10})
	var auto, observed int
	for _, e := range page.Items {
		switch e.Action {
		case "auto_rollback":
			auto++
			if e.Operator != "registry.canary" {
				t.Fatalf("auto_rollback operator=%q want registry.canary", e.Operator)
			}
		case "canary_observed":
			observed++
		}
	}
	if auto != 1 || observed != 1 {
		t.Fatalf("audit actions: auto_rollback=%d canary_observed=%d", auto, observed)
	}
}

func TestCanarySupervisorKeepsOnKeptDecision(t *testing.T) {
	st := newSeededStore(t,
		[]byte("alpha,rule,1.0,1\n"),
		[]byte("beta,rule,1.0,1\n"),
	)
	h1, h2 := storeHashes(t, st)

	envState := memstate.New()
	ctx := context.Background()
	_, _ = envState.PromoteChampion(ctx, "production", h1, "ci-bot", "seed")
	_, _ = envState.PromoteChampion(ctx, "production", h2, "ci-bot", "seed")

	met := &fakeCanaryMetrics{}
	sup := httpapi.CanarySupervisor{
		Decider:   fixedDecider{decision: canary.DecisionKept, obs: canary.Observation{ErrorRate: 0.001, SampleCount: 5000}},
		Artifacts: st,
		EnvState:  envState,
		Discovery: stubDiscovery{targets: []instances.Instance{{URL: "http://markup-svc-1:8080", Env: "production"}}},
		Deployer:  stubDeployer{out: okResult("http://markup-svc-1:8080")},
		Audit:     memaudit.New(),
		ULID:      &stubULID{},
		Logger:    &discardLogger{},
		Metrics:   met,
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}

	sup.Observe(ctx, "production", string(h2), "alice")

	state, _ := envState.Get(ctx, "production")
	if state.Champion == nil || state.Champion.Hash != h2 {
		t.Fatalf("kept decision must NOT roll back: champion=%+v", state.Champion)
	}
	met.mu.Lock()
	defer met.mu.Unlock()
	if met.hits["production/kept"] != 1 {
		t.Fatalf("kept counter not ticked: %+v", met.hits)
	}
}

func newSeededStore(t *testing.T, bodies ...[]byte) store.Store {
	t.Helper()
	st := memstore.New()
	for _, b := range bodies {
		if _, err := st.Put(context.Background(), store.PutRequest{
			SourceBytes: b, ContentType: store.ContentTypeCSV,
			Metadata: store.Metadata{CreatedBy: "ci-bot"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func storeHashes(t *testing.T, s store.Store) (store.Hash, store.Hash) {
	t.Helper()
	page, err := s.List(context.Background(), store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) < 2 {
		t.Fatalf("expected 2 items, got %d", len(page.Items))
	}
	return page.Items[1].Hash, page.Items[0].Hash
}

type discardLogger struct{}

func (discardLogger) Info(string, map[string]any) {}

type capturingLogger struct {
	mu     sync.Mutex
	events []capturedEvent
}

func (l *capturingLogger) Info(msg string, attrs map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, capturedEvent{msg: msg, attrs: attrs})
}

type errDecider struct{ err error }

func (e errDecider) Decide(_ context.Context, _ string) (canary.Decision, canary.Observation, error) {
	return canary.DecisionInconclusive, canary.Observation{}, e.err
}

func TestCanarySupervisorUpstreamFailureLogsAndCountsInconclusive(t *testing.T) {
	st := newSeededStore(t, []byte("alpha,rule,1.0,1\n"))
	envState := memstate.New()
	met := &fakeCanaryMetrics{}
	logger := &capturingLogger{}
	sup := &httpapi.CanarySupervisor{
		Decider:   errDecider{err: errors.New("wrapped: " + canary.ErrUpstreamUnreachable.Error())},
		Artifacts: st,
		EnvState:  envState,
		Discovery: stubDiscovery{targets: []instances.Instance{{URL: "http://markup-svc-1:8080", Env: "production"}}},
		Deployer:  stubDeployer{out: okResult("http://markup-svc-1:8080")},
		Audit:     memaudit.New(),
		ULID:      &stubULID{},
		Logger:    logger,
		Metrics:   met,
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	sup.Observe(context.Background(), "production", "abc", "alice")
	met.mu.Lock()
	defer met.mu.Unlock()
	if met.hits["production/inconclusive"] != 1 {
		t.Fatalf("inconclusive counter not ticked on upstream failure: %+v", met.hits)
	}
	saw := false
	for _, e := range logger.events {
		if e.msg == "registry.canary.observe_failed" {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatal("expected registry.canary.observe_failed log event")
	}
}

func TestCanarySupervisorDoesNotCommitOnFailedDeploy(t *testing.T) {
	st := newSeededStore(t,
		[]byte("alpha,rule,1.0,1\n"),
		[]byte("beta,rule,1.0,1\n"),
	)
	h1, h2 := storeHashes(t, st)

	envState := memstate.New()
	ctx := context.Background()
	_, _ = envState.PromoteChampion(ctx, "production", h1, "ci-bot", "seed")
	_, _ = envState.PromoteChampion(ctx, "production", h2, "ci-bot", "seed")

	sup := &httpapi.CanarySupervisor{
		Decider:   fixedDecider{decision: canary.DecisionRolledBack, obs: canary.Observation{ErrorRate: 0.05}},
		Artifacts: st,
		EnvState:  envState,
		Discovery: stubDiscovery{targets: []instances.Instance{{URL: "http://markup-svc-1:8080", Env: "production"}}},
		Deployer:  stubDeployer{out: deployer.DeployResult{Outcome: deployer.OutcomeFailed, Instances: []deployer.InstanceResult{{URL: "http://markup-svc-1:8080", Status: deployer.StatusFailed, Error: "synthetic"}}}},
		Audit:     memaudit.New(),
		ULID:      &stubULID{},
		Logger:    &discardLogger{},
		Metrics:   &fakeCanaryMetrics{},
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}

	sup.Observe(ctx, "production", string(h2), "alice")

	state, _ := envState.Get(ctx, "production")
	if state.Champion == nil || state.Champion.Hash != h2 {
		t.Fatalf("failed rollback deploy must NOT commit state; champion=%+v", state.Champion)
	}
}
