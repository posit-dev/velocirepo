package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArtifactPrefix starts the name of the workflow artifact an archive is
// saved in when its upload failed.
const ArtifactPrefix = "velocirepo-archive-"

// ArtifactName is the name of the workflow artifact of a run's archive.
func ArtifactName(runID string) string { return ArtifactPrefix + runID }

// PendingLimit is how long an archive may wait for its upload: its artifact
// is kept for 7 days, and the data in it can't be fetched again.
const PendingLimit = 6 * 24 * time.Hour

// Artifacts lists and downloads the workflow artifacts of archives whose
// upload failed, through the Actions API. The token needs actions: read.
type Artifacts struct {
	Client *http.Client
	Token  string
	Repo   string
	APIURL string
}

// Artifact is a pending archive.
type Artifact struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Expired   bool      `json:"expired"`
}

// RunID returns the run the artifact holds the archive of.
func (a Artifact) RunID() string { return strings.TrimPrefix(a.Name, ArtifactPrefix) }

func (a *Artifacts) store() *ReleaseStore {
	return &ReleaseStore{Client: a.Client, Token: a.Token, Repo: a.Repo, APIURL: a.APIURL}
}

// List returns the archives that are saved as artifacts and haven't expired.
func (a *Artifacts) List(ctx context.Context) ([]Artifact, error) {
	s := a.store()
	var out []Artifact
	for page := 1; ; page++ {
		var resp struct {
			Artifacts []Artifact `json:"artifacts"`
		}
		if _, err := s.json(ctx, "list workflow artifacts", http.MethodGet, fmt.Sprintf("%s/repos/%s/actions/artifacts?per_page=100&page=%d", s.apiURL(), a.Repo, page), nil, &resp, http.StatusOK); err != nil {
			return nil, err
		}
		for _, art := range resp.Artifacts {
			if strings.HasPrefix(art.Name, ArtifactPrefix) && !art.Expired {
				out = append(out, art)
			}
		}
		if len(resp.Artifacts) < 100 {
			return out, nil
		}
	}
}

// Download extracts an artifact's archive into dir/<run-id>.
func (a *Artifacts) Download(ctx context.Context, art Artifact, dir string) (string, error) {
	s := a.store()
	data, status, err := s.do(ctx, "download artifact "+art.Name, http.MethodGet, fmt.Sprintf("%s/repos/%s/actions/artifacts/%d/zip", s.apiURL(), a.Repo, art.ID), nil, "", "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("download artifact %s: GitHub returned %d", art.Name, status)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("artifact %s: %w", art.Name, err)
	}
	runDir := filepath.Join(dir, art.RunID())
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", err
	}
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if f.FileInfo().IsDir() || name != f.Name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		content, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(runDir, name), content, 0o644); err != nil {
			return "", err
		}
	}
	return runDir, nil
}

// CatchUp is what catching up on pending archives did.
type CatchUp struct {
	Uploaded []string
	// Skipped were already uploaded.
	Skipped []string
	Failed  []PendingFailure
}

// PendingFailure is a pending archive that couldn't be uploaded.
type PendingFailure struct {
	RunID string
	Age   time.Duration
	Err   error
}

// Overdue reports whether the archive has waited longer than PendingLimit.
func (f PendingFailure) Overdue() bool { return f.Age >= PendingLimit }

// CatchUpPending uploads every pending archive that has no release asset
// yet. Uploads are idempotent by asset name, so archives that were uploaded
// already are skipped and the artifacts needn't be deleted. An archive that
// contains one of the secrets isn't uploaded.
func CatchUpPending(ctx context.Context, arts *Artifacts, store Store, workDir string, secrets []string, now time.Time) (CatchUp, error) {
	var out CatchUp
	pending, err := arts.List(ctx)
	if err != nil {
		return out, err
	}
	for _, art := range pending {
		runID := art.RunID()
		fail := func(err error) {
			out.Failed = append(out.Failed, PendingFailure{RunID: runID, Age: now.Sub(art.CreatedAt), Err: err})
		}
		done, err := store.Uploaded(ctx, runID)
		if err != nil {
			fail(err)
			continue
		}
		if done {
			out.Skipped = append(out.Skipped, runID)
			continue
		}
		runDir, err := arts.Download(ctx, art, workDir)
		if err != nil {
			fail(err)
			continue
		}
		if found, err := FindSecrets(runDir, secrets); err != nil || len(found) > 0 {
			if err == nil {
				err = fmt.Errorf("the archive contains a token (in %s), so it isn't uploaded", strings.Join(found, ", "))
			}
			fail(err)
			continue
		}
		if err := store.Upload(ctx, runDir); err != nil {
			fail(err)
			continue
		}
		out.Uploaded = append(out.Uploaded, runID)
	}
	return out, nil
}
