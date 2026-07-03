package download

import "testing"

func TestSelectDLHubTarget_Prefers1080p(t *testing.T) {
	page := `
<form method="post" action="/download" target="_blank">
  <div>365 Days: This Day (2022) 720p web</div>
  <input type="hidden" name="q" value="https://yts.gg/torrent/download/720">
  <input type="hidden" name="torrent_url" value="https://yts.gg/torrent/download/720">
</form>
<form method="post" action="/download" target="_blank">
  <div>365 Days: This Day (2022) 1080p web</div>
  <input type="hidden" name="q" value="https://yts.gg/torrent/download/1080">
  <input type="hidden" name="torrent_url" value="https://yts.gg/torrent/download/1080">
</form>
<form method="post" action="/download" target="_blank">
  <div>365 Days: This Day (2022) 2160p web</div>
  <input type="hidden" name="q" value="https://yts.gg/torrent/download/2160">
  <input type="hidden" name="torrent_url" value="https://yts.gg/torrent/download/2160">
</form>`

	target, err := selectDLHubTarget(page)
	if err != nil {
		t.Fatalf("selectDLHubTarget failed: %v", err)
	}
	if target.URL != "https://yts.gg/torrent/download/1080" {
		t.Fatalf("expected 1080p target, got %q", target.URL)
	}
}

func TestMoviHubMetadataParsing(t *testing.T) {
	page := `<html><head>
<meta property="og:title" content="365 Days: This Day (2022) - MoviHub" />
<meta property="og:image" content="https://example.com/thumb.jpg" />
<title>Fallback - Player Page</title>
</head></html>`

	if got := cleanTitle(metaContent(page, "og:title")); got != "365 Days: This Day (2022)" {
		t.Fatalf("unexpected title %q", got)
	}
	if got := metaContent(page, "og:image"); got != "https://example.com/thumb.jpg" {
		t.Fatalf("unexpected thumbnail %q", got)
	}
}

func TestMoviHubMetadataParsing_AllowsReversedMetaAttributes(t *testing.T) {
	page := `<html><head>
<meta content="365 Days: This Day (2022) - MoviHub" property="og:title" />
<meta content="https://example.com/thumb.jpg" name="og:image" />
</head></html>`

	if got := cleanTitle(metaContent(page, "og:title")); got != "365 Days: This Day (2022)" {
		t.Fatalf("unexpected title %q", got)
	}
	if got := metaContent(page, "og:image"); got != "https://example.com/thumb.jpg" {
		t.Fatalf("unexpected thumbnail %q", got)
	}
}
