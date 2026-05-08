package tf

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// githubAPIBase is overridable for tests. Production always points at the
// real GitHub REST endpoint.
var githubAPIBase = "https://api.github.com"

// httpClient is the package-level client. Timeout caps each HTTP call so
// a flaky network can't hang `bnkctl init` indefinitely.
var httpClient = &http.Client{Timeout: 30 * time.Second}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// ResolveLatestRelease queries GitHub's releases/latest endpoint and
// returns the tag_name (e.g. "v0.6.7"). The "/latest" endpoint already
// filters to non-prerelease, non-draft, so we don't have to.
//
// Auth is optional: GITHUB_TOKEN if set, anonymous otherwise. Anonymous
// works for public repos but shares the 60/hr rate-limit pool — a token
// gets the user 5000/hr.
func ResolveLatestRelease(ctx context.Context, repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("repo is empty")
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", githubAPIBase, repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "bnkctl")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return "", fmt.Errorf("repo %q has no releases (or doesn't exist)", repo)
	case http.StatusForbidden:
		// Rate-limit or permission. Distinguish by header.
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return "", fmt.Errorf("github rate limit exhausted; set GITHUB_TOKEN to authenticate")
		}
		return "", fmt.Errorf("github returned 403 for %s", url)
	default:
		return "", fmt.Errorf("github returned %s for %s", resp.Status, url)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("decoding github response: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("github response had empty tag_name for %s", repo)
	}
	return rel.TagName, nil
}
