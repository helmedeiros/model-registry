//go:build bench

// Boot-time bench for the v0.0.4 three-file SQLite posture
// (fsstore + fsstate + fsaudit under one --store-root). Run with:
//
//	make bench-substrate
//
// Bars are pre-registered in scientific/v0.0.4/REPORT.md.
package v0_0_4

import (
	"path/filepath"
	"testing"

	"github.com/helmedeiros/model-registry/internal/audit/fsaudit"
	"github.com/helmedeiros/model-registry/internal/envstate/fsstate"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
)

// BenchmarkBootTime_ThreeSQLiteFiles measures opening fsstore +
// fsstate + fsaudit against a fresh store-root inside the timed
// loop. This is the substrate proof for the analytic ~150 ms
// upper-bound estimate in ADR-0005 §223; if measured cold-boot ever
// approaches half the 500 ms cmd-shell budget the bench fails.
// Pre-registered bar: < 200 ms / op (cold-cache, sequential opens).
func BenchmarkBootTime_ThreeSQLiteFiles(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		root := b.TempDir()
		b.StartTimer()

		store, err := fsstore.New(root)
		if err != nil {
			b.Fatalf("fsstore.New: %v", err)
		}
		envState, err := fsstate.New(filepath.Join(root, "envstate.db"))
		if err != nil {
			b.Fatalf("fsstate.New: %v", err)
		}
		audit, err := fsaudit.New(filepath.Join(root, "audit.db"))
		if err != nil {
			b.Fatalf("fsaudit.New: %v", err)
		}

		b.StopTimer()
		_ = audit.Close()
		_ = envState.Close()
		_ = store.Close()
		b.StartTimer()
	}
}
