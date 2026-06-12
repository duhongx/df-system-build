// Package params provides the canonical parameter-merge precedence for
// deployment-management: component defaults < global config < component
// override. It is the single source of truth for "effective parameters" so the
// service, render path, and UI all agree (Requirement 3.4, CP-2).
package params

// Merge returns the effective parameter map by layering, in increasing
// precedence: defaults, then global, then override. A key present in a
// higher-precedence layer always wins; keys absent from all layers are absent
// from the result. Inputs are not mutated.
func Merge(defaults, global, override map[string]any) map[string]any {
	out := make(map[string]any, len(defaults)+len(global)+len(override))
	for k, v := range defaults {
		out[k] = v
	}
	for k, v := range global {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// Effective is the closed-form lookup for a single key, matching Merge. Returns
// the value and whether the key is defined in any layer.
func Effective(key string, defaults, global, override map[string]any) (any, bool) {
	if v, ok := override[key]; ok {
		return v, true
	}
	if v, ok := global[key]; ok {
		return v, true
	}
	if v, ok := defaults[key]; ok {
		return v, true
	}
	return nil, false
}
