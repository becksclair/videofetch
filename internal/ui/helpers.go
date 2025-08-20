package ui

// ShortID trims a long hex ID for display in the dashboard table.
func ShortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
