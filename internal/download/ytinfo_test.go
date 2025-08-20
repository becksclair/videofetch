package download

import (
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantError bool
		errorMsg  string
	}{
		{
			name:      "valid http URL",
			url:       "http://example.com/video",
			wantError: false,
		},
		{
			name:      "valid https URL",
			url:       "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			wantError: false,
		},
		{
			name:      "empty URL",
			url:       "",
			wantError: true,
			errorMsg:  "empty URL",
		},
		{
			name:      "too long URL",
			url:       "https://example.com/" + repeatString("a", 2048),
			wantError: true,
			errorMsg:  "URL too long",
		},
		{
			name:      "malformed URL",
			url:       "not-a-url",
			wantError: true,
			errorMsg:  "unsupported scheme",
		},
		{
			name:      "unsupported scheme - ftp",
			url:       "ftp://example.com/file",
			wantError: true,
			errorMsg:  "unsupported scheme",
		},
		{
			name:      "unsupported scheme - file",
			url:       "file:///etc/passwd",
			wantError: true,
			errorMsg:  "unsupported scheme",
		},
		{
			name:      "missing host",
			url:       "http://",
			wantError: true,
			errorMsg:  "missing host",
		},
		{
			name:      "dangerous character - semicolon",
			url:       "https://example.com/video;rm -rf /",
			wantError: true,
			errorMsg:  "dangerous character",
		},
		{
			name:      "dangerous character - pipe",
			url:       "https://example.com/video|cat /etc/passwd",
			wantError: true,
			errorMsg:  "dangerous character",
		},
		{
			name:      "dangerous character - backtick",
			url:       "https://example.com/video`whoami`",
			wantError: true,
			errorMsg:  "dangerous character",
		},
		{
			name:      "dangerous character - dollar",
			url:       "https://example.com/video$(id)",
			wantError: true,
			errorMsg:  "dangerous character",
		},
		{
			name:      "dangerous character - newline",
			url:       "https://example.com/video\nrm -rf /",
			wantError: true,
			errorMsg:  "malformed URL", // URL parsing catches this first
		},
		{
			name:      "valid URL with query params",
			url:       "https://example.com/video?id=123&quality=hd",
			wantError: false,
		},
		{
			name:      "valid URL with fragment",
			url:       "https://example.com/video#chapter1",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.url)

			if tt.wantError {
				if err == nil {
					t.Errorf("validateURL(%q) expected error containing %q, got nil", tt.url, tt.errorMsg)
					return
				}
				if !containsString(err.Error(), tt.errorMsg) {
					t.Errorf("validateURL(%q) expected error containing %q, got %q", tt.url, tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("validateURL(%q) expected no error, got %v", tt.url, err)
				}
			}
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxBytes int
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			maxBytes: 10,
			expected: "",
		},
		{
			name:     "shorter than limit",
			input:    "hello",
			maxBytes: 10,
			expected: "hello",
		},
		{
			name:     "exactly at limit",
			input:    "hello",
			maxBytes: 5,
			expected: "hello",
		},
		{
			name:     "ASCII longer than limit",
			input:    "hello world",
			maxBytes: 5,
			expected: "hello",
		},
		{
			name:     "UTF-8 within limit",
			input:    "héllo",
			maxBytes: 10,
			expected: "héllo",
		},
		{
			name:     "UTF-8 truncation needed",
			input:    "héllo world",
			maxBytes: 7,
			expected: "héllo ", // "héllo " is 7 bytes (h=1, é=2, l=1, l=1, o=1, space=1)
		},
		{
			name:     "emoji within limit",
			input:    "🎵🎶",
			maxBytes: 10,
			expected: "🎵🎶",
		},
		{
			name:     "emoji truncation",
			input:    "🎵🎶🎵",
			maxBytes: 8,
			expected: "🎵🎶",
		},
		{
			name:     "zero limit",
			input:    "hello",
			maxBytes: 0,
			expected: "",
		},
		{
			name:     "negative limit",
			input:    "hello",
			maxBytes: -1,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateUTF8(tt.input, tt.maxBytes)
			if result != tt.expected {
				t.Errorf("truncateUTF8(%q, %d) = %q, expected %q", tt.input, tt.maxBytes, result, tt.expected)
			}
		})
	}
}

// Helper functions

func repeatString(s string, count int) string {
	if count <= 0 {
		return ""
	}
	result := make([]byte, 0, len(s)*count)
	for i := 0; i < count; i++ {
		result = append(result, s...)
	}
	return string(result)
}

func containsString(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}

	for i := 0; i <= len(s)-len(substr); i++ {
		found := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}
