package ui

import "testing"

func TestShortID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"a", "a"},
		{"ab", "ab"},
		{"abc", "abc"},
		{"abcd", "abcd"},
		{"abcde", "abcde"},
		{"abcdef", "abcdef"},
		{"abcdefg", "abcdefg"},
		{"abcdefgh", "abcdefgh"},
		{"abcdefghi", "abcdefgh"},
		{"abcdefghijklmnop", "abcdefgh"},
		{"a1b2c3d4e5f6g7h8i9", "a1b2c3d4"},
		{"very-long-download-id-that-should-be-truncated", "very-lon"},
	}

	for _, test := range tests {
		result := ShortID(test.input)
		if result != test.expected {
			t.Errorf("ShortID(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestShortID_UTF8(t *testing.T) {
	// Test with UTF-8 characters
	tests := []struct {
		input    string
		expected string
	}{
		{"αβγδεζηθ", "αβγδεζηθ"},
		{"αβγδεζηθι", "αβγδεζηθ"},
		{"日本語文字列", "日本語文字列"},
		{"🎵🎶🎵🎶🎵", "🎵🎶🎵🎶🎵"},
		{"🎵🎶🎵🎶🎵🎶", "🎵🎶🎵🎶🎵🎶"}, // 6 runes, within limit
		{"🎵🎶🎵🎶🎵🎶🎵🎶🎵", "🎵🎶🎵🎶🎵🎶🎵🎶"}, // 9 runes, should truncate to 8
	}

	for _, test := range tests {
		result := ShortID(test.input)
		if result != test.expected {
			t.Errorf("ShortID(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}