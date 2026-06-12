package params

import (
	"testing"

	"pgregory.net/rapid"
)

// TestPropertyMergeMatchesClosedForm checks CP-2: for any layers, Merge's
// result for every key equals the closed-form precedence lookup
// override > global > defaults.
func TestPropertyMergeMatchesClosedForm(t *testing.T) {
	keyGen := rapid.SampledFrom([]string{"a", "b", "c", "d", "e"})

	rapid.Check(t, func(t *rapid.T) {
		defaults := drawLayer(t, "def", keyGen)
		global := drawLayer(t, "glob", keyGen)
		override := drawLayer(t, "ovr", keyGen)

		merged := Merge(defaults, global, override)

		// Universe of keys.
		all := map[string]bool{}
		for k := range defaults {
			all[k] = true
		}
		for k := range global {
			all[k] = true
		}
		for k := range override {
			all[k] = true
		}

		for k := range all {
			want, wok := Effective(k, defaults, global, override)
			got, gok := merged[k]
			if wok != gok || want != got {
				t.Fatalf("key %q: merged=(%v,%v) closed-form=(%v,%v)", k, got, gok, want, wok)
			}
			// Override must always win when present.
			if ov, ok := override[k]; ok && merged[k] != ov {
				t.Fatalf("override should win for %q: got %v want %v", k, merged[k], ov)
			}
		}
		// No spurious keys.
		if len(merged) != len(all) {
			t.Fatalf("merged has %d keys, universe has %d", len(merged), len(all))
		}
	})
}

func drawLayer(t *rapid.T, label string, keyGen *rapid.Generator[string]) map[string]any {
	m := map[string]any{}
	n := rapid.IntRange(0, 5).Draw(t, label+"-n")
	for i := 0; i < n; i++ {
		k := keyGen.Draw(t, label+"-k")
		v := rapid.IntRange(0, 100).Draw(t, label+"-v")
		m[k] = v
	}
	return m
}
