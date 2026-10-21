package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
)

const githubAPIURL = "https://api.github.com"

// promptOwners asks who is assigned to, or mentioned in, the tracking issue,
// checks that they have access to the repository, and saves them in
// [settings] owners.
func promptOwners(ctx context.Context, out io.Writer, reader *bufio.Reader) error {
	client := &http.Client{Timeout: 10 * time.Second}
	token := os.Getenv("GITHUB_TOKEN")
	defaults := strings.Join(cfg.Settings.Owners, ", ")
	if defaults == "" && token != "" {
		defaults, _ = currentUser(ctx, client, githubAPIURL, token)
	}
	answer, err := prompt(out, reader, "Owners of the tracking issue", defaults, "GitHub users, or teams as @org/team, comma-separated")
	if err != nil {
		return err
	}
	var owners []string
	for _, o := range strings.Split(answer, ",") {
		if o = strings.TrimSpace(o); o != "" {
			owners = append(owners, o)
		}
	}
	if len(owners) == 0 {
		return nil
	}

	repo := originRepo(cfg.Dir)
	if repo == "" || token == "" {
		_, _ = fmt.Fprintln(out, "Couldn't check the owners' access: that needs GITHUB_TOKEN and a GitHub remote named origin.")
	} else {
		for _, o := range owners {
			ok, err := ownerHasAccess(ctx, client, githubAPIURL, token, repo, o)
			switch {
			case err != nil:
				_, _ = fmt.Fprintf(out, "Couldn't check %s's access: %v\n", o, err)
			case !ok:
				_, _ = fmt.Fprintf(out, "Warning: %s has no access to %s, so it can't be assigned the tracking issue.\n", o, repo)
			}
		}
	}
	return config.SetSettingList(cfgFilePath(), "owners", owners)
}

func currentUser(ctx context.Context, client *http.Client, apiURL, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", err
	}
	return user.Login, nil
}

// ownerHasAccess reports whether a user is a collaborator on the repository,
// or a team (@org/team) has access to it.
func ownerHasAccess(ctx context.Context, client *http.Client, apiURL, token, repo, owner string) (bool, error) {
	owner = strings.TrimPrefix(owner, "@")
	u := fmt.Sprintf("%s/repos/%s/collaborators/%s", apiURL, repo, owner)
	if org, team, isTeam := strings.Cut(owner, "/"); isTeam {
		u = fmt.Sprintf("%s/orgs/%s/teams/%s/repos/%s", apiURL, org, team, repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("GitHub returned %d", resp.StatusCode)
}

var githubRemote = regexp.MustCompile(`github\.com[:/]([^/]+/[^/.]+?)(\.git)?$`)

// originRepo returns owner/name of the GitHub remote named origin, or "".
func originRepo(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return ""
	}
	return m[1]
}
