package inspection

import "strings"

// Preview is for bounded operational table cells, never for original evidence.
func Preview(value string, limit int) string {
	value = strings.Join(strings.Fields(Redact(value)), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}
