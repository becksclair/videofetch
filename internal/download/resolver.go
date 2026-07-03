package download

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const browserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"

var (
	metaTagPattern     = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
	titlePattern       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	hiddenInputPattern = regexp.MustCompile(`(?is)<input\s+[^>]*type=["']hidden["'][^>]*>`)
	attrPattern        = regexp.MustCompile(`(?is)([a-zA-Z_:-]+)=["']([^"']*)["']`)
	formPattern        = regexp.MustCompile(`(?is)<form\s+[^>]*action=["']/download["'][^>]*>.*?</form>`)
	qualityPattern     = regexp.MustCompile(`(?i)(2160p|1080p|720p|480p|web|bluray|brrip)`)
)

type resolvedTarget struct {
	URL      string
	Info     MediaInfo
	Quality  string
	Position int
}

// ResolveDownloadTarget maps unsupported webpage URLs to a concrete download URL.
func ResolveDownloadTarget(ctx context.Context, inputURL string) (string, MediaInfo, error) {
	parsed, err := url.Parse(inputURL)
	if err != nil {
		return "", MediaInfo{}, err
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "movihub.net" && host != "www.movihub.net" {
		return "", MediaInfo{}, fmt.Errorf("no resolver for host %s", parsed.Hostname())
	}
	return resolveMoviHub(ctx, inputURL)
}

func resolveMoviHub(ctx context.Context, inputURL string) (string, MediaInfo, error) {
	body, err := fetchText(ctx, http.MethodGet, inputURL, "", "")
	if err != nil {
		return "", MediaInfo{}, err
	}

	info := MediaInfo{
		Title:        firstNonEmpty(cleanTitle(metaContent(body, "og:title")), cleanTitle(htmlTitle(body)), inputURL),
		ThumbnailURL: metaContent(body, "og:image"),
	}
	if strings.TrimSpace(info.Title) == "" {
		return "", MediaInfo{}, ErrNoMediaInfo
	}

	form := url.Values{}
	form.Set("q", info.Title)
	form.Set("source", "movies")
	searchBody, err := fetchText(ctx, http.MethodPost, "https://dlhub.cc/search", "application/x-www-form-urlencoded", form.Encode())
	if err != nil {
		return "", MediaInfo{}, err
	}

	target, err := selectDLHubTarget(searchBody)
	if err != nil {
		return "", MediaInfo{}, err
	}
	return target.URL, info, nil
}

func fetchText(ctx context.Context, method, rawURL, contentType, body string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, rawURL, reader)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status %d from %s", resp.StatusCode, rawURL)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func selectDLHubTarget(page string) (resolvedTarget, error) {
	matches := formPattern.FindAllString(page, -1)
	if len(matches) == 0 {
		return resolvedTarget{}, ErrNoMediaInfo
	}

	targets := make([]resolvedTarget, 0, len(matches))
	for i, form := range matches {
		fields := hiddenFields(form)
		rawURL := firstNonEmpty(fields["torrent_url"], fields["q"], fields["magnet"])
		if rawURL == "" {
			continue
		}
		rawURL = html.UnescapeString(rawURL)
		quality := strings.ToLower(firstMatch(qualityPattern, form))
		targets = append(targets, resolvedTarget{URL: rawURL, Quality: quality, Position: i})
	}
	if len(targets) == 0 {
		return resolvedTarget{}, ErrNoMediaInfo
	}

	best := targets[0]
	bestScore := targetScore(best)
	for _, candidate := range targets[1:] {
		score := targetScore(candidate)
		if score > bestScore {
			best = candidate
			bestScore = score
		}
	}
	return best, nil
}

func targetScore(target resolvedTarget) int {
	score := 100 - target.Position
	switch target.Quality {
	case "1080p":
		score += 1000
	case "720p":
		score += 700
	case "2160p":
		score += 600
	case "480p":
		score += 400
	}
	return score
}

func hiddenFields(fragment string) map[string]string {
	out := make(map[string]string)
	for _, input := range hiddenInputPattern.FindAllString(fragment, -1) {
		attrs := make(map[string]string)
		for _, match := range attrPattern.FindAllStringSubmatch(input, -1) {
			attrs[strings.ToLower(match[1])] = html.UnescapeString(match[2])
		}
		name := attrs["name"]
		if name == "" {
			continue
		}
		out[name] = attrs["value"]
	}
	return out
}

func metaContent(page, key string) string {
	key = strings.ToLower(key)
	for _, tag := range metaTagPattern.FindAllString(page, -1) {
		attrs := make(map[string]string)
		for _, match := range attrPattern.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(match[1])] = html.UnescapeString(strings.TrimSpace(match[2]))
		}
		if strings.ToLower(attrs["property"]) == key || strings.ToLower(attrs["name"]) == key {
			if content := strings.TrimSpace(attrs["content"]); content != "" {
				return content
			}
		}
	}
	return ""
}

func htmlTitle(page string) string {
	match := titlePattern.FindStringSubmatch(page)
	if len(match) < 2 {
		return ""
	}
	return html.UnescapeString(strings.TrimSpace(match[1]))
}

func cleanTitle(title string) string {
	title = strings.TrimSpace(title)
	for _, suffix := range []string{" - MoviHub", " - Player Page"} {
		title = strings.TrimSuffix(title, suffix)
	}
	return strings.TrimSpace(title)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstMatch(pattern *regexp.Regexp, value string) string {
	match := pattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}
