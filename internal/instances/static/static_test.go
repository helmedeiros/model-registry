package static_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/instances/instancestest"
	"github.com/helmedeiros/model-registry/internal/instances/static"
)

func TestConformance(t *testing.T) {
	instancestest.RunConformance(t, func(t *testing.T, seed map[string][]string) instances.Discovery {
		t.Helper()
		d, err := static.NewFromMap(seed)
		if err != nil {
			t.Fatalf("NewFromMap: %v", err)
		}
		return d
	})
}

func TestNewFromMapRejectsEmpty(t *testing.T) {
	if _, err := static.NewFromMap(nil); !errors.Is(err, static.ErrEmptyConfig) {
		t.Fatalf("err=%v want ErrEmptyConfig", err)
	}
}

func TestNewFromMapRejectsEmptyURLList(t *testing.T) {
	_, err := static.NewFromMap(map[string][]string{"production": {}})
	if !errors.Is(err, static.ErrEmptyURLList) {
		t.Fatalf("err=%v want ErrEmptyURLList", err)
	}
}

func TestNewFromMapRejectsEmptyEnv(t *testing.T) {
	_, err := static.NewFromMap(map[string][]string{"": {"http://x:8080"}})
	if !errors.Is(err, static.ErrEmptyEnv) {
		t.Fatalf("err=%v want ErrEmptyEnv", err)
	}
}

func TestNewFromMapRejectsBadURL(t *testing.T) {
	_, err := static.NewFromMap(map[string][]string{"production": {"::::::not a url"}})
	if !errors.Is(err, static.ErrInvalidURL) {
		t.Fatalf("err=%v want ErrInvalidURL", err)
	}
}

func TestNewFromMapRejectsMissingScheme(t *testing.T) {
	_, err := static.NewFromMap(map[string][]string{"production": {"markup-svc-1:8080"}})
	if !errors.Is(err, static.ErrMissingURLScheme) {
		t.Fatalf("err=%v want ErrMissingURLScheme", err)
	}
}

func TestLoadReadsJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instances.json")
	if err := os.WriteFile(path, []byte(`{"production":["http://markup-svc-1:8080"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := static.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := d.Instances(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "http://markup-svc-1:8080" {
		t.Fatalf("loaded instance wrong: %+v", got)
	}
}

func TestLoadFailsOnMissingFile(t *testing.T) {
	if _, err := static.Load("/no/such/file.json"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadFailsOnInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := static.Load(path); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
