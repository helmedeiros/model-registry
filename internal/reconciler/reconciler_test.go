package reconciler_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	_ "github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/reconciler"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

type stubDeployer struct {
	mu    sync.Mutex
	calls int32
	out   deployer.DeployResult
}

func (s *stubDeployer) Deploy(context.Context, []instances.Instance, deployer.Body) (deployer.DeployResult, error) {
	return s.out, nil
}
func (s *stubDeployer) DeployChallenger(_ context.Context, _ []instances.Instance, _ deployer.Body) (deployer.DeployResult, error) {
	atomic.AddInt32(&s.calls, 1)
	return s.out, nil
}
func (s *stubDeployer) ClearChallenger(context.Context, []instances.Instance) (deployer.DeployResult, error) {
	return deployer.DeployResult{}, nil
}

type stubDiscovery struct{}

func (stubDiscovery) Instances(_ context.Context, _ string) ([]instances.Instance, error) {
	return []instances.Instance{{URL: "http://markup-svc-1:8080", Env: "production"}}, nil
}

type captureLogger struct {
	mu     sync.Mutex
	events []string
}

func (c *captureLogger) Info(msg string, _ map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, msg)
}

func (c *captureLogger) seen(msg string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e == msg {
			return true
		}
	}
	return false
}

func TestReconciler_TicksRePushChallengerWhenLoaded(t *testing.T) {
	st := memstore.New()
	source := []byte("alpha,rule,1.0,1\n")
	h, _ := st.Put(context.Background(), store.PutRequest{SourceBytes: source, ContentType: store.ContentTypeCSV})

	envState := memstate.New()
	if err := envState.PromoteChallenger(context.Background(), "production", h, "alice", "shadow trial"); err != nil {
		t.Fatal(err)
	}

	dep := &stubDeployer{out: deployer.DeployResult{Outcome: deployer.OutcomeOK}}
	log := &captureLogger{}
	rec := reconciler.New([]string{"production"}, envState, st, stubDiscovery{}, dep, log, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go rec.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&dep.calls) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if atomic.LoadInt32(&dep.calls) < 2 {
		t.Fatalf("DeployChallenger called %d times in 2s; want >= 2 from 10ms ticker", dep.calls)
	}
	if !log.seen("registry.reconciler.reconciled") {
		t.Fatalf("reconciled log event not seen: %+v", log.events)
	}
}

func TestReconciler_NoChallengerSkipsPush(t *testing.T) {
	st := memstore.New()
	envState := memstate.New() // no challenger
	dep := &stubDeployer{}
	rec := reconciler.New([]string{"production"}, envState, st, stubDiscovery{}, dep, nil, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go rec.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()
	if got := atomic.LoadInt32(&dep.calls); got != 0 {
		t.Fatalf("DeployChallenger called %d times despite empty challenger envstate", got)
	}
}

var _ = audit.Reader(nil) // keep import for future direct asserts
var _ = memaudit.New      // ditto
