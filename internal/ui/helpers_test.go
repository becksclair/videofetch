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
		{"🎵🎶🎵🎶🎵🎶", "🎵🎶🎵🎶🎵🎶"},      // 6 runes, within limit
		{"🎵🎶🎵🎶🎵🎶🎵🎶🎵", "🎵🎶🎵🎶🎵🎶🎵🎶"}, // 9 runes, should truncate to 8
	}

	for _, test := range tests {
		result := ShortID(test.input)
		if result != test.expected {
			t.Errorf("ShortID(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestShortID_EdgeCases(t *testing.T) {
	// Test edge cases for better coverage
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "exactly 8 characters",
			input:    "12345678",
			expected: "12345678",
		},
		{
			name:     "exactly 8 runes with multibyte chars",
			input:    "αβγδεζηθ",
			expected: "αβγδεζηθ",
		},
		{
			name:     "mixed ASCII and multibyte",
			input:    "ab🎵cd🎶ef🎵ghij",
			expected: "ab🎵cd🎶ef", // 8 runes: a,b,🎵,c,d,🎶,e,f
		},
		{
			name:     "long hex ID simulation",
			input:    "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d",
			expected: "1a2b3c4d",
		},
		{
			name:     "mixed case hex",
			input:    "AbCdEfGhIjKlMnOp",
			expected: "AbCdEfGh",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ShortID(test.input)
			if result != test.expected {
				t.Errorf("ShortID(%q) = %q, expected %q", test.input, result, test.expected)
			}
		})
	}
}

func TestShortID_Performance(t *testing.T) {
	// Test with a very long string to ensure performance is reasonable
	longID := ""
	for i := 0; i < 1000; i++ {
		longID += "a"
	}

	result := ShortID(longID)
	expected := "aaaaaaaa"

	if result != expected {
		t.Errorf("ShortID with long string failed: got %q, expected %q", result, expected)
	}

	if len(result) != 8 {
		t.Errorf("ShortID result length should be 8, got %d", len(result))
	}
}
