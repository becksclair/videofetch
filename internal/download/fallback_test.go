package download

import "testing"

func TestShouldFallback_Matches(t *testing.T) {
	cases := []string{
		"HTTP Error 403: Forbidden",
		"fragment 1 not found, unable to continue",
		"Requested format is not available",
		"yt-dlp: exit status 1: 1 not found, unable to continue",
	}
	for _, c := range cases {
		if !shouldFallback(c) {
			t.Fatalf("shouldFallback(%q) = false; want true", c)
		}
	}
}

func TestShouldFallback_NonMatches(t *testing.T) {
	cases := []string{
		"network timeout",
		"some other error",
		"invalid url",
	}
	for _, c := range cases {
		if shouldFallback(c) {
			t.Fatalf("shouldFallback(%q) = true; want false", c)
		}
	}
}
