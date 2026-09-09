// Package textutil contains UTF-8-safe helpers shared across trace ingestion
// and judge rendering.
package textutil

import "unicode/utf8"

// TruncateRunes limits text to at most limit runes, including marker. Invalid
// UTF-8 is replaced with the standard replacement rune so callers can safely
// serialize the result. When the marker itself exceeds the budget, its prefix
// takes the available space.
func TruncateRunes(text string, limit int, marker string) string {
	if limit <= 0 {
		return ""
	}

	runes := []rune(text)
	if len(runes) <= limit {
		if utf8.ValidString(text) {
			return text
		}

		return string(runes)
	}

	markerRunes := []rune(marker)
	if len(markerRunes) > limit {
		markerRunes = markerRunes[:limit]
	}

	contentLimit := limit - len(markerRunes)

	return string(runes[:contentLimit]) + string(markerRunes)
}
