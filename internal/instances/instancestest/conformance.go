// Package instancestest is the reusable conformance suite every
// instances.Discovery backing runs against.
package instancestest

import (
	"context"
	"errors"
	"testing"

	"github.com/helmedeiros/model-registry/internal/instances"
)

// Factory builds a fresh Discovery seeded with the supplied env→urls
// map. Backings translate the map into their native config form (file
// on disk for static; CRD in K8s for the future controller-based
// variant). Returning nil indicates the backing cannot represent the
// fixture; the suite then skips the relevant case.
type Factory func(t *testing.T, seed map[string][]string) instances.Discovery

// RunConformance exercises every behaviour Discovery promises.
func RunConformance(t *testing.T, factory Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, factory Factory)
	}{
		{"InstancesReturnsConfiguredURLs", testInstancesReturnsConfigured},
		{"InstancesUnknownEnvReturnsErrNoInstances", testInstancesUnknownEnv},
		{"InstancesIsolatedAcrossEnvs", testInstancesIsolated},
		{"InstancesReturnsDefensiveCopy", testInstancesDefensiveCopy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, factory) })
	}
}

func ctx() context.Context { return context.Background() }

func testInstancesReturnsConfigured(t *testing.T, mk Factory) {
	d := mk(t, map[string][]string{
		"production": {"http://markup-1:8080", "http://markup-2:8080"},
	})
	if d == nil {
		t.Skip("backing cannot represent this fixture")
	}
	got, err := d.Instances(ctx(), "production")
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 instances, got %d", len(got))
	}
	for _, inst := range got {
		if inst.Env != "production" {
			t.Fatalf("Env=%q want production", inst.Env)
		}
	}
}

func testInstancesUnknownEnv(t *testing.T, mk Factory) {
	d := mk(t, map[string][]string{
		"production": {"http://markup-1:8080"},
	})
	if d == nil {
		t.Skip("backing cannot represent this fixture")
	}
	_, err := d.Instances(ctx(), "staging")
	if !errors.Is(err, instances.ErrNoInstances) {
		t.Fatalf("Instances(staging): err=%v want ErrNoInstances", err)
	}
}

func testInstancesIsolated(t *testing.T, mk Factory) {
	d := mk(t, map[string][]string{
		"production": {"http://prod-1:8080"},
		"staging":    {"http://staging-1:8080"},
	})
	if d == nil {
		t.Skip("backing cannot represent this fixture")
	}
	prod, err := d.Instances(ctx(), "production")
	if err != nil {
		t.Fatalf("Instances(production): %v", err)
	}
	staging, err := d.Instances(ctx(), "staging")
	if err != nil {
		t.Fatalf("Instances(staging): %v", err)
	}
	if prod[0].URL == staging[0].URL {
		t.Fatalf("envs aliased: prod=%s staging=%s", prod[0].URL, staging[0].URL)
	}
	if prod[0].Env != "production" || staging[0].Env != "staging" {
		t.Fatalf("env label leaked: %+v %+v", prod[0], staging[0])
	}
}

func testInstancesDefensiveCopy(t *testing.T, mk Factory) {
	d := mk(t, map[string][]string{
		"production": {"http://markup-1:8080"},
	})
	if d == nil {
		t.Skip("backing cannot represent this fixture")
	}
	first, _ := d.Instances(ctx(), "production")
	first[0].URL = "http://MUTATED"

	second, _ := d.Instances(ctx(), "production")
	if second[0].URL == "http://MUTATED" {
		t.Fatal("caller's mutation bled through into the discovery's backing")
	}
}
