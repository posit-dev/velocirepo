package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// githubAPI calls GitHub's REST API for the data repository, with the
// workflow token: never with GITHUB_TOKEN, which is the token velocirepo
// fetches with.
type githubAPI struct {
	Client *http.Client
	Token  string
	Repo   string
	URL    string
}

// PermissionError is a call GitHub refused for lack of a permission.
type PermissionError struct {
	What       string
	Permission string
	Status     int
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("GitHub refused to %s (%d): the workflow needs the %s permission", e.What, e.Status, e.Permission)
}

func (a githubAPI) baseURL() string {
	if a.URL != "" {
		return strings.TrimSuffix(a.URL, "/")
	}
	return "https://api.github.com"
}

// call sends a request to /repos/<repo><path> and decodes the response into
// out. permission names what a 403 means is missing.
func (a githubAPI) call(ctx context.Context, what, permission, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL()+"/repos/"+a.Repo+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if a.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, &PermissionError{What: what, Permission: permission, Status: resp.StatusCode}
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s: GitHub returned %d: %s", what, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s: %w", what, err)
		}
	}
	return resp.StatusCode, nil
}
