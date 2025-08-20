package ui

import "unicode/utf8"

// ShortID trims a long hex ID for display in the dashboard table.
// Handles UTF-8 properly by counting runes, not bytes.
func ShortID(id string) string {
	const maxLen = 8
	if utf8.RuneCountInString(id) <= maxLen {
		return id
	}
	
	// Truncate at rune boundary
	count := 0
	for i := range id {
		if count >= maxLen {
			return id[:i]
		}
		count++
	}
	return id
}
