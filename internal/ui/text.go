package ui

// TruncateWithEllipsis truncates text to maxRunes and appends an ellipsis when needed.
func TruncateWithEllipsis(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
