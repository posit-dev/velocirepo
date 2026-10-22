package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/spf13/cobra"
)

// repoFlags are the repository the archive is stored in, and the token for
// it: the workflow token, not the GITHUB_TOKEN velocirepo fetches with.
type repoFlags struct {
	repo, token string
}

func (f *repoFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.repo, "repo", "", "the data repository, owner/name (default: $GITHUB_REPOSITORY)")
	cmd.Flags().StringVar(&f.token, "repo-token", "", "a token for the data repository (default: $VELOCIREPO_REPO_TOKEN)")
}

func (f *repoFlags) resolve() (string, string, error) {
	repo, token := f.repo, f.token
	if repo == "" {
		repo = os.Getenv("GITHUB_REPOSITORY")
	}
	if token == "" {
		token = os.Getenv("VELOCIREPO_REPO_TOKEN")
	}
	if repo == "" {
		return "", "", fmt.Errorf("no data repository: use --repo or set GITHUB_REPOSITORY")
	}
	return repo, token, nil
}

func (f *repoFlags) store() (*archive.ReleaseStore, error) {
	repo, token, err := f.resolve()
	if err != nil {
		return nil, err
	}
	return &archive.ReleaseStore{Client: &http.Client{Timeout: 10 * time.Minute}, Token: token, Repo: repo}, nil
}

func tokenValues() []string {
	t := fetch.TokensFromEnv()
	return []string{t.GitHub, t.Plausible, t.YouTube, t.LinkedIn, os.Getenv("VELOCIREPO_REPO_TOKEN")}
}

// appendOutput adds a step output for GitHub Actions.
func appendOutput(path, name, value string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, "%s=%s\n", name, value)
	return err
}

func uploadArchiveCmd() *cobra.Command {
	var repo repoFlags
	var pending bool
	var outputPath string
	cmd := &cobra.Command{
		Use:   "upload-archive [run-dir...]",
		Short: "Store archived runs as release assets of the data repository",
		Long: `Store archived runs as assets of the data repository's monthly raw-YYYY-MM release, a pre-release that's never the latest release. Uploads can be repeated: assets that are stored already are skipped.

With --pending, the archives of earlier runs whose upload failed, saved as workflow artifacts, are uploaded too; one that has waited six days is an error, since its artifact is about to expire. With --output, a failed upload isn't an error: the archive's path and artifact name are written as step outputs, so the workflow can save it.`,
		GroupID: "ci",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := repo.store()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			for _, runDir := range args {
				if found, err := archive.FindSecrets(runDir, tokenValues()); err != nil || len(found) > 0 {
					if err == nil {
						err = fmt.Errorf("%s contains a token (in %s); it's neither uploaded nor saved", runDir, strings.Join(found, ", "))
					}
					return err
				}
				if err := store.Upload(ctx, runDir); err != nil {
					if outputPath == "" {
						return err
					}
					ui.Warnf("upload of %s failed, saving it for the next run: %v", runDir, err)
					runID := filepath.Base(runDir)
					if err := appendOutput(outputPath, "pending-archive", runDir); err != nil {
						return err
					}
					if err := appendOutput(outputPath, "pending-name", archive.ArtifactName(runID)); err != nil {
						return err
					}
					continue
				}
				ui.Infof("Uploaded %s", runDir)
			}
			if pending {
				return catchUp(ctx, store)
			}
			return nil
		},
	}
	repo.add(cmd)
	cmd.Flags().BoolVar(&pending, "pending", false, "also upload the archives saved as workflow artifacts")
	cmd.Flags().StringVar(&outputPath, "output", "", "on a failed upload, write the archive's path to this GitHub Actions output file")
	return cmd
}

func catchUp(ctx context.Context, store *archive.ReleaseStore) error {
	work, err := os.MkdirTemp("", "velocirepo-pending-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	arts := &archive.Artifacts{Client: store.Client, Token: store.Token, Repo: store.Repo}
	result, err := archive.CatchUpPending(ctx, arts, store, work, tokenValues(), time.Now())
	if err != nil {
		return fmt.Errorf("pending archives: %w", err)
	}
	for _, id := range result.Uploaded {
		ui.Infof("Uploaded pending archive %s", id)
	}
	overdue := 0
	for _, f := range result.Failed {
		if f.Overdue() {
			overdue++
			ui.Errorf("archive %s has waited %s for its upload, and its artifact expires soon: %v", f.RunID, f.Age.Round(time.Hour), f.Err)
			continue
		}
		ui.Warnf("pending archive %s: %v", f.RunID, f.Err)
	}
	if overdue > 0 {
		return fmt.Errorf("%d archive(s) pending for six days or more", overdue)
	}
	return nil
}

func downloadArchiveCmd() *cobra.Command {
	var repo repoFlags
	var dir string
	cmd := &cobra.Command{
		Use:     "download-archive <run-id>...",
		Short:   "Download archived runs from the data repository's releases, for replay",
		GroupID: "fetch",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := repo.store()
			if err != nil {
				return err
			}
			if store.Token == "" {
				store.Token = os.Getenv("GITHUB_TOKEN")
			}
			for _, id := range args {
				runDir, err := store.Download(cmd.Context(), id, dir)
				if err != nil {
					return err
				}
				ui.Infof("Downloaded %s", runDir)
			}
			return nil
		},
	}
	repo.add(cmd)
	cmd.Flags().StringVar(&dir, "archive", ".", "the directory to download into")
	return cmd
}
