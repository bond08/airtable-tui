package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// latestReleaseURL is GitHub's "latest release" API endpoint, which
// resolves to the newest non-prerelease, non-draft tag.
const latestReleaseURL = "https://api.github.com/repos/bond08/airtable-tui/releases/latest"

// updateCheckTimeout keeps a slow/unreachable GitHub from blocking startup.
const updateCheckTimeout = 5 * time.Second

// updateCheckMsg carries the latest release tag back from checkForUpdate.
// err is non-nil on any failure (offline, rate-limited, no releases yet);
// those are ignored rather than surfaced, since this check is best-effort.
type updateCheckMsg struct {
	latest string
	err    error
}

// checkForUpdate asks GitHub for the latest release tag. Skipped entirely
// for "dev" builds (go run, or a local build without -ldflags), since
// there's no meaningful "current version" to compare against.
func (m Model) checkForUpdate() tea.Cmd {
	if m.version == "" || m.version == "dev" {
		return nil
	}
	return func() tea.Msg {
		client := &http.Client{Timeout: updateCheckTimeout}
		req, err := http.NewRequest(http.MethodGet, latestReleaseURL, nil)
		if err != nil {
			return updateCheckMsg{err: err}
		}
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := client.Do(req)
		if err != nil {
			return updateCheckMsg{err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return updateCheckMsg{err: fmt.Errorf("github API %d", resp.StatusCode)}
		}

		var payload struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return updateCheckMsg{err: err}
		}
		return updateCheckMsg{latest: payload.TagName}
	}
}

// newerVersion reports whether latest is a newer semver-ish tag than
// current. Both are expected in "vX.Y.Z" form (a leading "v" is optional);
// anything that doesn't parse cleanly is treated as not-newer so a
// malformed tag never triggers a false "update available".
func newerVersion(current, latest string) bool {
	cur, ok1 := parseVersion(current)
	lat, ok2 := parseVersion(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
