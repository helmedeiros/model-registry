// Package static is the JSON-config instances.Discovery backing
// (ADR-0005). The config maps env name → []base URL; the registry
// reads the file once at boot and serves Instances queries from the
// in-memory map.
package static

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/helmedeiros/model-registry/internal/instances"
)

// Discovery is a static-config instances.Discovery. Construct via
// Load (from a file path) or NewFromMap (for tests + composition
// from cmd flags).
type Discovery struct {
	envs map[string][]instances.Instance
}

// Errors at construction time. Mis-shaped config fails at boot so a
// typo cannot silently degrade a promote at the first call.
var (
	ErrEmptyConfig      = errors.New("instances/static: config has no envs configured")
	ErrEmptyEnv         = errors.New("instances/static: env name cannot be empty")
	ErrEmptyURLList     = errors.New("instances/static: env has no urls configured")
	ErrInvalidURL       = errors.New("instances/static: url failed to parse as http(s)")
	ErrMissingURLScheme = errors.New("instances/static: url missing http(s) scheme")
)

// Load reads a JSON file mapping env names to lists of base URLs and
// returns a constructed Discovery. The file shape is:
//
//	{"production": ["http://markup-svc-1:8080", "http://markup-svc-2:8080"]}
func Load(path string) (*Discovery, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("instances/static: read %s: %w", path, err)
	}
	var raw map[string][]string
	if err := json.Unmarshal(bytes, &raw); err != nil {
		return nil, fmt.Errorf("instances/static: parse %s: %w", path, err)
	}
	return NewFromMap(raw)
}

// NewFromMap constructs a Discovery from a parsed env→urls map.
// Validates URLs eagerly: any malformed entry rejects the whole map
// rather than silently dropping the bad row.
func NewFromMap(raw map[string][]string) (*Discovery, error) {
	if len(raw) == 0 {
		return nil, ErrEmptyConfig
	}
	envs := make(map[string][]instances.Instance, len(raw))
	for env, urls := range raw {
		if env == "" {
			return nil, ErrEmptyEnv
		}
		if len(urls) == 0 {
			return nil, fmt.Errorf("%w: %q", ErrEmptyURLList, env)
		}
		parsed := make([]instances.Instance, 0, len(urls))
		for _, rawURL := range urls {
			u, err := url.Parse(rawURL)
			if err != nil {
				return nil, fmt.Errorf("%w: %q: %w", ErrInvalidURL, rawURL, err)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				return nil, fmt.Errorf("%w: %q", ErrMissingURLScheme, rawURL)
			}
			parsed = append(parsed, instances.Instance{URL: rawURL, Env: env})
		}
		envs[env] = parsed
	}
	return &Discovery{envs: envs}, nil
}

// Instances implements instances.Discovery.
// Envs implements instances.EnvLister.
func (d *Discovery) Envs() []string {
	out := make([]string, 0, len(d.envs))
	for env := range d.envs {
		out = append(out, env)
	}
	return out
}

func (d *Discovery) Instances(_ context.Context, env string) ([]instances.Instance, error) {
	got, ok := d.envs[env]
	if !ok || len(got) == 0 {
		return nil, instances.ErrNoInstances
	}
	out := make([]instances.Instance, len(got))
	copy(out, got)
	return out, nil
}
