// Package defaults exposes the legacy "factory" component parameter
// values (the contents of config/components/*.yml in the old ansible
// layout) as a Go map. These are the per-component knobs every
// installation needs filled in with sensible values: redis password,
// docker data_root, postgresql data_dir, minio access_key, and so on.
//
// dfctl-web uses this package in two places:
//
//  1. On first install, the SQLite seed routine pre-populates the
//     `component_overrides` table with these values so a fresh
//     database can already render a complete custom.yml — without
//     this seed, every ${redis.password} and ${docker.data_root}
//     reference would leak as a literal into generated config files.
//
//  2. The frontend "组件" page calls /api/components/defaults to show
//     operators what the factory value is and to power the "重置为默认值"
//     button.
//
// The values themselves are intentionally embedded from the legacy
// YAML files so updating defaults stays a one-file edit instead of
// touching Go source.
package defaults

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed components/*.yml
var componentsFS embed.FS

// loadOnce caches the parsed map so repeated callers (seed + http
// handler + render path) don't re-parse the embedded files.
var (
	loadOnce sync.Once
	cache    map[string]map[string]any
	loadErr  error
)

// All returns a defensive deep-copy of every component's default
// parameter map, keyed by component name. Callers may mutate the
// returned map without affecting other callers.
func All() (map[string]map[string]any, error) {
	loadOnce.Do(parseEmbedded)
	if loadErr != nil {
		return nil, loadErr
	}
	out := make(map[string]map[string]any, len(cache))
	for name, params := range cache {
		out[name] = cloneMap(params)
	}
	return out, nil
}

// For returns a defensive copy of the named component's default
// params. ok=false when the component has no embedded defaults.
func For(name string) (map[string]any, bool, error) {
	all, err := All()
	if err != nil {
		return nil, false, err
	}
	v, ok := all[name]
	if !ok {
		return nil, false, nil
	}
	return cloneMap(v), true, nil
}

// Names returns every component for which we ship factory defaults,
// sorted alphabetically. Used by tests that want to assert no new
// component slips through without defaults.
func Names() ([]string, error) {
	all, err := All()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for n := range all {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func parseEmbedded() {
	cache = map[string]map[string]any{}
	entries, err := componentsFS.ReadDir("components")
	if err != nil {
		loadErr = fmt.Errorf("read embedded defaults: %w", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		raw, err := componentsFS.ReadFile(filepath.Join("components", e.Name()))
		if err != nil {
			loadErr = fmt.Errorf("read %s: %w", e.Name(), err)
			return
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			loadErr = fmt.Errorf("parse %s: %w", e.Name(), err)
			return
		}
		// The legacy files use the component name as the top-level
		// key (`redis: { password: ..., ... }`). We normalize to a
		// flat name → params map so callers don't have to peel that
		// layer off.
		for compName, valuesRaw := range doc {
			values, ok := valuesRaw.(map[string]any)
			if !ok {
				loadErr = fmt.Errorf("%s: top-level %q must be a map", e.Name(), compName)
				return
			}
			cache[compName] = stringifyMap(values)
		}
	}
}

// stringifyMap recursively converts yaml.v3's map[interface{}]interface{}
// branches into map[string]any so consumers can JSON-encode without a
// custom marshaler.
func stringifyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = stringifyValue(v)
	}
	return out
}

func stringifyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return stringifyMap(t)
	case map[any]any:
		converted := make(map[string]any, len(t))
		for k, vv := range t {
			converted[fmt.Sprint(k)] = stringifyValue(vv)
		}
		return converted
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = stringifyValue(item)
		}
		return out
	default:
		return v
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	return stringifyMap(in)
}
