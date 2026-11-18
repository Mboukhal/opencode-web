// Package github reads public repository metadata from the GitHub REST API.
//
// Listing uses the unauthenticated API (no token is ever stored). Private
// repositories are reachable only through the SSH deploy key, so they can be
// cloned by name but do not appear in the listing.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"opencodeweb/store"
)

// Client talks to the GitHub REST API.
type Client struct {
	http  *http.Client
	login string
}

// New returns a client bound to one GitHub login.
func New(login string) *Client {
	return &Client{
		login: login,
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

// Login is the account whose repositories are listed.
func (c *Client) Login() string { return c.login }

// repoAPI mirrors the fields we keep from /users/{login}/repos.
type repoAPI struct {
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Private     bool   `json:"private"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Size        int64  `json:"size"`
	PushedAt    string `json:"pushed_at"`
	UpdatedAt   string `json:"updated_at"`
	Archived    bool   `json:"archived"`
	Fork        bool   `json:"fork"`
}

// ListRepos fetches up to 300 repositories for the bound login.
func (c *Client) ListRepos(ctx context.Context) ([]store.CachedRepo, error) {
	var all []repoAPI
	for page := 1; page <= 3; page++ {
		u := fmt.Sprintf("https://api.github.com/users/%s/repos?per_page=100&page=%d&sort=pushed&type=owner",
			url.PathEscape(c.login), page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "opencode-web")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing github repos: %w", err)
		}
		err = func() error {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("github api status %d", resp.StatusCode)
			}
			var batch []repoAPI
			if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
				return fmt.Errorf("decoding github repos: %w", err)
			}
			all = append(all, batch...)
			if len(batch) < 100 {
				return nil
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
		if len(all)%100 != 0 {
			break
		}
	}

	out := make([]store.CachedRepo, 0, len(all))
	for _, r := range all {
		out = append(out, store.CachedRepo{
			Owner:       c.login,
			Repo:        r.Name,
			Private:     r.Private,
			Description: r.Description,
			Language:    r.Language,
			SizeKB:      r.Size,
			PushedAt:    r.PushedAt,
			Archived:    r.Archived,
		})
	}
	return out, nil
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidName reports whether s is a safe GitHub owner/repo component.
func ValidName(s string) bool {
	return nameRe.MatchString(s) && s != "." && s != ".." && !strings.HasSuffix(s, ".git")
}
