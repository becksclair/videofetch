package download

import (
    "bufio"
    "encoding/json"
    "errors"
    "os/exec"
    "strings"
)

// MediaInfo contains minimal metadata extracted from yt-dlp -j.
type MediaInfo struct {
    Title        string
    DurationSec  int64
    ThumbnailURL string
}

// FetchMediaInfo runs `yt-dlp -j` and returns the first parsed media info.
// On failure, returns a zero MediaInfo and an error.
func FetchMediaInfo(url string) (MediaInfo, error) {
    if err := CheckYTDLP(); err != nil {
        return MediaInfo{}, err
    }
    // Mirror the Rust example: use -j and pass extractor args to impersonate
    // the generic extractor when probing metadata to improve robustness.
    cmd := exec.Command("yt-dlp", "-j", "--extractor-args", "generic:impersonate", "--no-playlist", url)
    out, err := cmd.StdoutPipe()
    if err != nil {
        return MediaInfo{}, err
    }
    if err := cmd.Start(); err != nil {
        return MediaInfo{}, err
    }
    defer cmd.Wait()
    sc := bufio.NewScanner(out)
    for sc.Scan() {
        ln := strings.TrimSpace(sc.Text())
        if ln == "" {
            continue
        }
        // Parse generically to allow missing fields.
        var m map[string]any
        if err := json.Unmarshal([]byte(ln), &m); err != nil {
            continue
        }
        var title string
        if v, ok := m["title"].(string); ok && v != "" {
            title = v
        } else {
            title = url
        }
        var duration int64
        switch dv := m["duration"].(type) {
        case float64:
            duration = int64(dv)
        case int64:
            duration = dv
        }
        var thumb string
        if v, ok := m["thumbnail"].(string); ok {
            thumb = v
        }
        if thumb == "" {
            if arr, ok := m["thumbnails"].([]any); ok && len(arr) > 0 {
                if obj, ok := arr[0].(map[string]any); ok {
                    if u, ok := obj["url"].(string); ok {
                        thumb = u
                    }
                }
            }
        }
        return MediaInfo{Title: title, DurationSec: duration, ThumbnailURL: thumb}, nil
    }
    if err := sc.Err(); err != nil {
        return MediaInfo{}, err
    }
    return MediaInfo{}, errors.New("no_media_info")
}
