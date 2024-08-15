package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/httpapi"
)

// TestRollbackOneChampionPromotedReturns400 covers the gap between
// "no history at all" and "history but no DISTINCT prior champion":
// after a single PromoteChampion the env has one champion_promoted
// transition; rollback has no prior to fall back to and must return
// 400 no_history (the same code as the empty-history case). Without
// this test a future Reader implementation could mistakenly return
// the current champion as its own predecessor.
func TestRollbackOneChampionPromotedReturns400(t *testing.T) {
	deps, st, envState, au, _ := newRollbackDeps(t, okResult())
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))
	if _, err := envState.PromoteChampion(context.Background(), "production", h, "ci-bot", "first"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", rollbackBody(t, httpapi.RollbackRequest{
		Env: "production", Operator: "alice", Reason: "trial",
	}))
	httpapi.Rollback(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 with one history entry", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "no_history" {
		t.Fatalf("reason=%q want no_history", got)
	}
	// State must be untouched.
	state, _ := envState.Get(context.Background(), "production")
	if state.Champion == nil || state.Champion.Hash != h {
		t.Fatalf("champion mutated by rejected rollback: %+v", state)
	}
	// And no rollback audit entry should land for the rejected call.
	page, _ := au.List(context.Background(), audit.ListOptions{})
	for _, e := range page.Items {
		if e.Action == "rollback" {
			t.Fatalf("rejected rollback minted an audit entry: %+v", e)
		}
	}
}

// TestPromoteConcurrentSameEnvSerialises fires N concurrent /promote
// calls against the same env. The substrate is memstate which uses a
// sync.RWMutex around state mutations; the test asserts (a) every
// call returns 200, (b) the final champion is one of the inputs,
// (c) the audit log records every transition exactly once with
// monotonic at timestamps. This is the in-memory analogue of the
// fsstate WAL-serialised path the ADR-0005 §194 framing covers; it
// runs in-process so the contention is visible without bench
// machinery.
func TestPromoteConcurrentSameEnvSerialises(t *testing.T) {
	deps, st, envState, au, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))

	const n = 10
	hashes := make([]string, n)
	for i := 0; i < n; i++ {
		h := putRule(t, st, []byte("alpha,rule,1.0,"+string(rune('0'+i))+"\n"))
		hashes[i] = string(h)
	}
	handler := httpapi.Promote(deps)

	var wg sync.WaitGroup
	var okCount int64
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
				Hash: hashes[i], Env: "production", Role: "champion", Operator: "bench-bot", Reason: "concurrent",
			}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				atomic.AddInt64(&okCount, 1)
			}
		}()
	}
	wg.Wait()

	if okCount != n {
		t.Fatalf("ok=%d want all %d concurrent /promote to succeed", okCount, n)
	}
	state, _ := envState.Get(context.Background(), "production")
	if state.Champion == nil {
		t.Fatal("no champion after concurrent promotes")
	}
	want := map[string]bool{}
	for _, h := range hashes {
		want[h] = true
	}
	if !want[string(state.Champion.Hash)] {
		t.Fatalf("final champion %q is not one of the inputs", state.Champion.Hash)
	}

	page, _ := au.List(context.Background(), audit.ListOptions{Limit: 100})
	if len(page.Items) != n {
		t.Fatalf("audit entries=%d want %d", len(page.Items), n)
	}
	// at-timestamps must be monotonic (newest first; later in slice = older).
	for i := 1; i < len(page.Items); i++ {
		if !page.Items[i-1].At.After(page.Items[i].At) && !page.Items[i-1].At.Equal(page.Items[i].At) {
			t.Fatalf("audit at-order broke at i=%d: %v then %v", i, page.Items[i-1].At, page.Items[i].At)
		}
	}
}

// TestUploadTruncatedMultipartReturns400 ensures a malformed body
// reaches a clean 400 invalid_multipart rather than a panic or a 500.
// The fixture truncates a valid multipart body 8 bytes from the end
// — past the source data but before the closing boundary — which is
// exactly the failure mode a partial-write proxy could surface.
func TestUploadTruncatedMultipartReturns400(t *testing.T) {
	deps, _, _, _ := newUploadDeps(t)
	body, ct := multipartBody(t, map[string]uploadPart{
		"source": {filename: "rules.csv", contentType: "text/csv", body: []byte("alpha,rule,1.0,1\n")},
	})
	all, _ := readAll(body)
	if len(all) < 16 {
		t.Fatalf("test fixture too small to truncate: %d", len(all))
	}
	truncated := all[:len(all)-8]

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(truncated))
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 on truncated multipart; body=%s", rec.Code, rec.Body.String())
	}
}

func readAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}
