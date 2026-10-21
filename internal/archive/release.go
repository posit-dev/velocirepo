package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store keeps archived runs.
type Store interface {
	// Upload stores a run's archive. Files that are already stored are
	// skipped, so an upload can be repeated.
	Upload(ctx context.Context, runDir string) error
	// Uploaded reports whether a run's archive is stored completely.
	Uploaded(ctx context.Context, runID string) (bool, error)
	// Download writes a run's archive into dir/<run-id>.
	Download(ctx context.Context, runID, dir string) (string, error)
}

// ReleaseStore keeps archives as assets of the data repository's releases,
// one release per month (raw-YYYY-MM). The releases are pre-releases that are
// never the latest release, so they don't notify watchers and don't change
// what releases/latest points to.
type ReleaseStore struct {
	Client *http.Client
	// Token needs contents: write, such as the workflow token.
	Token string
	// Repo is owner/name.
	Repo string
	// APIURL and UploadURL default to GitHub's.
	APIURL    string
	UploadURL string
	// Attempts is how often a request is tried; 3 by default.
	Attempts int
	// Wait is the pause before a retry; a second by default.
	Wait time.Duration
}

var _ Store = (*ReleaseStore)(nil)

type release struct {
	ID      int64  `json:"id"`
	TagName string `json:"tag_name"`
}

type asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// PermissionError is a request GitHub refused for lack of a permission.
type PermissionError struct {
	Action string
	Status int
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("GitHub refused to %s (%d): the token needs the contents: write permission", e.Action, e.Status)
}

func (s *ReleaseStore) apiURL() string {
	if s.APIURL != "" {
		return strings.TrimSuffix(s.APIURL, "/")
	}
	return "https://api.github.com"
}

func (s *ReleaseStore) uploadURL() string {
	if s.UploadURL != "" {
		return strings.TrimSuffix(s.UploadURL, "/")
	}
	return "https://uploads.github.com"
}

// do sends a request, retrying server errors and network failures.
func (s *ReleaseStore) do(ctx context.Context, action, method, u string, body []byte, contentType string, accept string) ([]byte, int, error) {
	attempts := s.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	wait := s.Wait
	if wait == 0 {
		wait = time.Second
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(wait * time.Duration(i)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		if s.Token != "" {
			req.Header.Set("Authorization", "Bearer "+s.Token)
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		switch {
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
			return nil, resp.StatusCode, &PermissionError{Action: action, Status: resp.StatusCode}
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: GitHub returned %d", action, resp.StatusCode)
			continue
		}
		return data, resp.StatusCode, nil
	}
	return nil, 0, lastErr
}

func (s *ReleaseStore) json(ctx context.Context, action, method, u string, in, out any, ok ...int) (int, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return 0, err
		}
	}
	data, status, err := s.do(ctx, action, method, u, body, "application/json", "application/vnd.github+json")
	if err != nil {
		return status, err
	}
	for _, code := range ok {
		if status == code {
			if out != nil && len(data) > 0 && status < 300 {
				if err := json.Unmarshal(data, out); err != nil {
					return status, fmt.Errorf("%s: %w", action, err)
				}
			}
			return status, nil
		}
	}
	return status, fmt.Errorf("%s: GitHub returned %d: %s", action, status, strings.TrimSpace(string(data)))
}

// releaseFor returns a month's release, creating it if create is set; nil if
// it doesn't exist.
func (s *ReleaseStore) releaseFor(ctx context.Context, tag string, create bool) (*release, error) {
	var r release
	status, err := s.json(ctx, "read release "+tag, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases/tags/%s", s.apiURL(), s.Repo, url.PathEscape(tag)), nil, &r, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK {
		return &r, nil
	}
	if !create {
		return nil, nil
	}
	in := map[string]any{
		"tag_name":    tag,
		"name":        "Raw responses " + strings.TrimPrefix(tag, "raw-"),
		"body":        "Raw API responses archived by velocirepo, for replays. Not a release of this repository.",
		"prerelease":  true,
		"make_latest": "false",
	}
	if _, err := s.json(ctx, "create release "+tag, http.MethodPost, fmt.Sprintf("%s/repos/%s/releases", s.apiURL(), s.Repo), in, &r, http.StatusCreated); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *ReleaseStore) assets(ctx context.Context, id int64) (map[string]asset, error) {
	out := map[string]asset{}
	for page := 1; ; page++ {
		var list []asset
		if _, err := s.json(ctx, "list release assets", http.MethodGet, fmt.Sprintf("%s/repos/%s/releases/%d/assets?per_page=100&page=%d", s.apiURL(), s.Repo, id, page), nil, &list, http.StatusOK); err != nil {
			return nil, err
		}
		for _, a := range list {
			out[a.Name] = a
		}
		if len(list) < 100 {
			return out, nil
		}
	}
}

// runTag returns the release tag of a run, from the date its ID starts with.
func runTag(runID string) (string, error) {
	if len(runID) < 8 {
		return "", fmt.Errorf("run ID %q doesn't start with a date", runID)
	}
	t, err := time.Parse("20060102", runID[:8])
	if err != nil {
		return "", fmt.Errorf("run ID %q doesn't start with a date", runID)
	}
	return ReleaseTag(t), nil
}

func (s *ReleaseStore) Upload(ctx context.Context, runDir string) error {
	m, err := ReadManifest(runDir)
	if err != nil {
		return err
	}
	tag, err := runTag(m.RunID)
	if err != nil {
		return err
	}
	rel, err := s.releaseFor(ctx, tag, true)
	if err != nil {
		return err
	}
	existing, err := s.assets(ctx, rel.ID)
	if err != nil {
		return err
	}
	type upload struct{ local, name string }
	var uploads []upload
	for _, f := range m.Files {
		uploads = append(uploads, upload{filepath.Join(runDir, f.Name), AssetName(m.RunID, f.Source)})
	}
	// The manifest goes last: a run whose manifest is stored is complete.
	uploads = append(uploads, upload{filepath.Join(runDir, "manifest.json"), ManifestAssetName(m.RunID)})
	for _, u := range uploads {
		if _, ok := existing[u.name]; ok {
			continue
		}
		data, err := os.ReadFile(u.local)
		if err != nil {
			return err
		}
		target := fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", s.uploadURL(), s.Repo, rel.ID, url.QueryEscape(u.name))
		_, status, err := s.do(ctx, "upload "+u.name, http.MethodPost, target, data, "application/octet-stream", "application/vnd.github+json")
		if err != nil {
			return err
		}
		// 422 means an asset of that name exists already, uploaded by a
		// run that raced this one.
		if status != http.StatusCreated && status != http.StatusUnprocessableEntity {
			return fmt.Errorf("upload %s: GitHub returned %d", u.name, status)
		}
	}
	return nil
}

func (s *ReleaseStore) Uploaded(ctx context.Context, runID string) (bool, error) {
	tag, err := runTag(runID)
	if err != nil {
		return false, err
	}
	rel, err := s.releaseFor(ctx, tag, false)
	if err != nil || rel == nil {
		return false, err
	}
	existing, err := s.assets(ctx, rel.ID)
	if err != nil {
		return false, err
	}
	_, ok := existing[ManifestAssetName(runID)]
	return ok, nil
}

func (s *ReleaseStore) Download(ctx context.Context, runID, dir string) (string, error) {
	tag, err := runTag(runID)
	if err != nil {
		return "", err
	}
	rel, err := s.releaseFor(ctx, tag, false)
	if err != nil {
		return "", err
	}
	if rel == nil {
		return "", fmt.Errorf("no release %s with archived runs", tag)
	}
	existing, err := s.assets(ctx, rel.ID)
	if err != nil {
		return "", err
	}
	if _, ok := existing[ManifestAssetName(runID)]; !ok {
		return "", fmt.Errorf("run %s isn't in release %s", runID, tag)
	}
	runDir := filepath.Join(dir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", err
	}
	prefix := runID + "-"
	for name, a := range existing {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		// <source>.jsonl.zst or manifest.json, as the writer names them.
		local := rest
		data, status, err := s.do(ctx, "download "+name, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases/assets/%d", s.apiURL(), s.Repo, a.ID), nil, "", "application/octet-stream")
		if err != nil {
			return "", err
		}
		if status != http.StatusOK {
			return "", fmt.Errorf("download %s: GitHub returned %d", name, status)
		}
		if err := os.WriteFile(filepath.Join(runDir, local), data, 0o644); err != nil {
			return "", err
		}
	}
	m, err := ReadManifest(runDir)
	if err != nil {
		return "", fmt.Errorf("downloaded run %s: %w", runID, err)
	}
	for _, f := range m.Files {
		sum, err := fileSHA256(filepath.Join(runDir, f.Name))
		if err != nil {
			return "", fmt.Errorf("downloaded run %s: %w", runID, err)
		}
		if sum != f.SHA256 {
			return "", fmt.Errorf("downloaded run %s: %s doesn't match its checksum", runID, f.Name)
		}
	}
	return runDir, nil
}

// IsPermission reports whether err is a refused permission.
func IsPermission(err error) bool {
	var p *PermissionError
	return errors.As(err, &p)
}
