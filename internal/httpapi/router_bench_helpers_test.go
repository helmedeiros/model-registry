package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	regotel "github.com/helmedeiros/model-registry/internal/observability/otel"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

// newRouterDepsForBench mirrors newRouterDeps but uses the no-op
// tracer — the production posture when the binary boots without an
// OTLP exporter — so the bench reads the dominant production cost.
func newRouterDepsForBench(b *testing.B) (httpapi.Deps, http.HandlerFunc) {
	b.Helper()
	tracer, shutdown, err := regotel.Bootstrap(context.Background(), regotel.Config{
		Exporter:            regotel.ExporterNone,
		InstrumentationName: "router-bench",
	})
	if err != nil {
		b.Fatalf("bootstrap: %v", err)
	}
	b.Cleanup(func() { _ = shutdown(context.Background()) })

	deps := httpapi.Deps{
		AccessLog: &accessSink{},
		Metrics:   &metricsStub{},
		PanicSink: &stubSink{},
		Tracer:    tracer,
		Ready:     func() (string, bool) { return "", true },
		Artifacts: memstore.New(),
		EnvState:  memstate.New(),
		Audit:     memaudit.New(),
	}
	scrape := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "# exposition stub\n")
	}
	return deps, scrape
}

func newTestRouter(b *testing.B, deps httpapi.Deps, scrape http.HandlerFunc) http.Handler {
	b.Helper()
	return httpapi.NewRouter(deps, scrape)
}
