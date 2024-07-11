package memstore_test

import (
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
	"github.com/helmedeiros/model-registry/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.RunConformance(t, func(_ *testing.T, clock func() time.Time) store.Store {
		return memstore.New(memstore.WithClock(clock))
	})
}
