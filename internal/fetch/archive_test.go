package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
)

// A fetch with an archive attached archives the responses of sources whose
// data can't be fetched again, and records every target's options and
// GitHub's snapshot refs in the manifest.
func TestAttachArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"pkg","full_name":"pkg","analytics":{"install":{"30d":{"pkg":10},"90d":{"pkg":20},"365d":{"pkg":30}}}}`))
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}}
	w, err := archive.NewWriter(t.TempDir(), "run1", "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Now: at(21, 9, 30), NoConcatenate: true}
	AttachArchive(&opts, w)

	client := clientForOpts(&opts)
	job := fetchJob{sourceName: "homebrew", projectID: "proj", targets: []string{"pkg"},
		sources: []source.Source{&source.Homebrew{Client: client, Formula: "pkg", BaseURL: srv.URL}}}
	if _, err := runJobs(context.Background(), cfg, opts, []fetchJob{job}, 1); err != nil {
		t.Fatal(err)
	}
	manifest, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Source != "homebrew" || manifest.Files[0].Exchanges != 1 {
		t.Errorf("files = %+v, want one homebrew exchange", manifest.Files)
	}
	if len(manifest.Jobs) != 1 || manifest.Jobs[0].Target != "pkg" || manifest.Jobs[0].End[""] != "2026-10-20" || manifest.Jobs[0].Zones[""] != "UTC" {
		t.Errorf("jobs = %+v", manifest.Jobs)
	}
	exchanges, err := archive.ReadExchanges(filepath.Join(w.Dir(), manifest.Files[0].Name))
	if err != nil || len(exchanges) != 1 || exchanges[0].Project != "proj" || exchanges[0].Target != "pkg" {
		t.Errorf("exchanges = %+v, %v", exchanges, err)
	}
}
